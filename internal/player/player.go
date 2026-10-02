// Package player runs one ffmpeg that plays a stream to the speakers and,
// from the same decode, hands mono PCM to the visualizer.
package player

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/code-akram/cc-fm-mod/internal/spectrum"
)

// DefaultSource is the link Claude Code's own /radio command opens.
const DefaultSource = "https://clau.de/radio"

type State string

const (
	Stopped    State = "stopped"
	Connecting State = "connecting"
	Playing    State = "playing"
	Retrying   State = "retrying"
)

// Status is what the player reports to every listener.
type Status struct {
	State  State  `json:"state"`
	Source string `json:"source,omitempty"`
	Title  string `json:"title,omitempty"`
	Volume int    `json:"volume"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

type Config struct {
	// Output is the ffmpeg audio device: audiotoolbox, pulse, alsa or null.
	Output string
	// Volume is 0–100.
	Volume int
	// DelayMs holds the speakers back so remote bars line up with what you hear.
	DelayMs int
	// YtDlpArgs go before the URL on every yt-dlp run (e.g. --cookies-from-browser).
	YtDlpArgs []string
	// OnSamples receives mono s16 PCM at spectrum.SampleRate, from ffmpeg's goroutine.
	OnSamples func([]int16)
	// OnChange receives every status change.
	OnChange func(Status)
}

type Player struct {
	cfg Config

	mu     sync.Mutex
	st     Status
	cancel context.CancelFunc
	done   chan struct{}
	stdin  io.Writer
}

func New(cfg Config) *Player {
	return &Player{
		cfg: cfg,
		st:  Status{State: Stopped, Volume: clampVolume(cfg.Volume), Output: cfg.Output},
	}
}

func (p *Player) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

// Play stops whatever plays and starts source (DefaultSource when empty).
func (p *Player) Play(source string) {
	p.halt()
	if source == "" {
		source = DefaultSource
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.mu.Lock()
	p.cancel, p.done = cancel, done
	p.st.Source, p.st.Title = source, ""
	p.mu.Unlock()
	p.setState(Connecting, "")

	go func() {
		defer close(done)
		p.run(ctx, source)
	}()
}

// Stop ends playback and waits for ffmpeg to exit.
func (p *Player) Stop() {
	if p.halt() {
		p.setState(Stopped, "")
	}
}

// halt cancels the running stream, if any, and waits for it to wind down.
func (p *Player) halt() bool {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel, p.done = nil, nil
	p.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	<-done
	return true
}

// SetVolume changes the volume, live when ffmpeg is running.
func (p *Player) SetVolume(v int) {
	p.mu.Lock()
	p.st.Volume = clampVolume(v)
	if p.stdin != nil {
		// ffmpeg's interactive 'c' command: target, time (-1 = now), command, argument.
		fmt.Fprintf(p.stdin, "cvolume -1 volume %.4f\n", gain(p.st.Volume))
	}
	st := p.st
	p.mu.Unlock()
	p.notify(st)
}

func (p *Player) run(ctx context.Context, source string) {
	backoff := 2 * time.Second
	for {
		started := time.Now()
		err := p.once(ctx, source)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		p.setState(Retrying, errText(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (p *Player) once(ctx context.Context, source string) error {
	input, title, err := resolve(ctx, source, p.cfg.YtDlpArgs)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.st.Title = title
	vol := p.st.Volume
	p.mu.Unlock()

	cmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs(input, p.cfg.Output, gain(vol), p.cfg.DelayMs)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 3 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &tail{max: 2048}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}

	p.mu.Lock()
	p.stdin = stdin
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.stdin = nil
		p.mu.Unlock()
	}()

	p.pump(stdout)

	if err := cmd.Wait(); err != nil {
		if msg := stderr.String(); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("ffmpeg: %w", err)
	}
	return errors.New("stream ended")
}

// pump reads s16le PCM until ffmpeg closes stdout, marking the player
// playing once the first samples arrive.
func (p *Player) pump(r io.Reader) {
	buf := make([]byte, 4096)
	var carry []byte
	isFirst := true
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := append(carry, buf[:n]...)
			whole := len(data) &^ 1
			samples := make([]int16, whole/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(data[2*i:]))
			}
			carry = append(carry[:0], data[whole:]...)
			if isFirst {
				isFirst = false
				p.setState(Playing, "")
			}
			if p.cfg.OnSamples != nil {
				p.cfg.OnSamples(samples)
			}
		}
		if err != nil {
			return
		}
	}
}

func ffmpegArgs(input []string, output string, gain float64, delayMs int) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostats"}
	if output == "null" {
		// No sound card to pace the decode, so read at the native rate instead.
		args = append(args, "-re")
	}
	args = append(args, input...)

	play := fmt.Sprintf("[p]volume=%.4f", gain)
	if delayMs > 0 {
		play += fmt.Sprintf(",adelay=%d:all=1", delayMs)
	}
	graph := "[0:a]asplit=2[p][v];" + play + "[po];" +
		fmt.Sprintf("[v]aresample=%d,aformat=sample_fmts=s16:channel_layouts=mono[vo]", spectrum.SampleRate)
	args = append(args, "-filter_complex", graph, "-map", "[po]")

	switch output {
	case "audiotoolbox":
		args = append(args, "-f", "audiotoolbox", "-")
	case "pulse":
		args = append(args, "-f", "pulse", "cc-fm")
	case "alsa":
		args = append(args, "-f", "alsa", "default")
	default:
		args = append(args, "-f", "null", "-")
	}

	return append(args, "-map", "[vo]", "-f", "s16le", "pipe:1")
}

func (p *Player) setState(s State, errMsg string) {
	p.mu.Lock()
	p.st.State, p.st.Error = s, errMsg
	st := p.st
	p.mu.Unlock()
	p.notify(st)
}

func (p *Player) notify(st Status) {
	if p.cfg.OnChange != nil {
		p.cfg.OnChange(st)
	}
}

// gain maps 0–100 onto a curve that sounds even: 50 is about -12 dB.
func gain(v int) float64 { return math.Pow(float64(v)/100, 2) }

func clampVolume(v int) int { return min(max(v, 0), 100) }

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// tail keeps the last max bytes written, trimmed to whole lines.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
