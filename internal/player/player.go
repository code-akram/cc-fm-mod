// Package player plays a stream through two ffmpeg processes with the player
// in between:
//
//	decoder ffmpeg ──PCM──▶ player (volume, delay, visualizer tap) ──PCM──▶ speaker ffmpeg
//
// The decoder always hands over one fixed format, so when a live stream
// changes format at a track boundary only the decoder rebuilds its filters;
// the speaker side never sees the change and plays one unbroken stream. The
// volume lives in the player, so a rebuild can't reset it either.
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

// The PCM format between the processes: 48 kHz stereo s16le, 4 bytes a frame.
const (
	playRate   = 48000
	frameBytes = 4
)

// The visualizer tap halves the rate; the analyzer must expect exactly that.
const _ = uint(playRate/2-spectrum.SampleRate) + uint(spectrum.SampleRate-playRate/2)

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
	// Output is where the sound goes: audiotoolbox, pulse, alsa, null (no
	// sound, visualizer only) or file:<path> (a WAV recording, for testing).
	Output string
	// Volume is 0–100.
	Volume int
	// DelayMs holds the speakers back so remote bars line up with what you hear.
	DelayMs int
	// YtDlpArgs go before the URL on every yt-dlp run (e.g. --cookies-from-browser).
	YtDlpArgs []string
	// OnSamples receives mono s16 PCM at spectrum.SampleRate.
	OnSamples func([]int16)
	// OnChange receives every status change.
	OnChange func(Status)
	// OnVolume receives each volume set, to keep it for the next run.
	OnVolume func(int)
	// Log receives ffmpeg's warnings, a line at a time; nil drops them.
	Log func(string)
}

type Player struct {
	cfg Config

	mu     sync.Mutex
	st     Status
	cancel context.CancelFunc
	done   chan struct{}
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

// SetVolume changes the volume; the pump ramps to it over its next chunk.
func (p *Player) SetVolume(v int) {
	p.mu.Lock()
	p.st.Volume = clampVolume(v)
	st := p.st
	p.mu.Unlock()
	if p.cfg.OnVolume != nil {
		p.cfg.OnVolume(st.Volume)
	}
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
	p.mu.Unlock()

	// Either process ending takes the other down with it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sinkArgs := speakerArgs(p.cfg.Output)
	// Only a sound card paces the stream; without one, read at the native rate.
	isUnpaced := sinkArgs == nil || strings.HasPrefix(p.cfg.Output, "file:")
	decoder, decoderErr := p.ffmpeg(ctx, decoderArgs(input, isUnpaced))
	decoded, err := decoder.StdoutPipe()
	if err != nil {
		return err
	}
	if err := decoder.Start(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}

	var speaker *exec.Cmd
	var speakerErr *tail
	var speakerIn io.WriteCloser
	if sinkArgs != nil {
		speaker, speakerErr = p.ffmpeg(ctx, sinkArgs)
		if speakerIn, err = speaker.StdinPipe(); err != nil {
			return err
		}
		if err := speaker.Start(); err != nil {
			return fmt.Errorf("ffmpeg: %w", err)
		}
		// Silence up front delays everything after it by the same amount.
		if p.cfg.DelayMs > 0 {
			_, _ = speakerIn.Write(make([]byte, playRate*p.cfg.DelayMs/1000*frameBytes))
		}
	}

	// What ffmpeg says while being told to stop is noise, not news.
	context.AfterFunc(ctx, func() {
		decoderErr.Mute()
		if speakerErr != nil {
			speakerErr.Mute()
		}
	})

	p.pump(decoded, speakerIn)
	cancel()
	decodeExit := decoder.Wait()
	var speakerExit error
	if speaker != nil {
		speakerIn.Close()
		speakerExit = speaker.Wait()
	}

	switch {
	case decodeExit != nil && decoderErr.Last() != "":
		return errors.New(decoderErr.Last())
	case speakerExit != nil && speakerErr.Last() != "":
		return errors.New(speakerErr.Last())
	case decodeExit != nil:
		return fmt.Errorf("ffmpeg: %w", decodeExit)
	}
	return errors.New("stream ended")
}

func (p *Player) ffmpeg(ctx context.Context, args []string) (*exec.Cmd, *tail) {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 3 * time.Second
	stderr := &tail{max: 2048, log: p.cfg.Log}
	cmd.Stderr = stderr
	return cmd, stderr
}

// pump moves PCM from the decoder to the speakers, applying the volume and
// handing the visualizer a mono copy, until the decoder or speakers stop.
func (p *Player) pump(r io.Reader, w io.Writer) {
	buf := make([]byte, 16384)
	var carry []byte
	current := p.gain()
	isFirst := true
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := append(carry, buf[:n]...)
			whole := len(data) &^ (frameBytes - 1)
			target := p.gain()
			mono := applyGain(data[:whole], current, target)
			current = target
			if w != nil {
				if _, err := w.Write(data[:whole]); err != nil {
					return
				}
			}
			carry = append(carry[:0], data[whole:]...)
			if isFirst {
				isFirst = false
				p.setState(Playing, "")
			}
			if p.cfg.OnSamples != nil {
				p.cfg.OnSamples(mono)
			}
		}
		if err != nil {
			return
		}
	}
}

// applyGain scales stereo s16le frames in place, ramping from one gain to the
// next across the chunk so a volume change never clicks. It returns the
// unscaled audio as mono at half the rate, for the visualizer.
func applyGain(frames []byte, from, to float64) []int16 {
	count := len(frames) / frameBytes
	mono := make([]int16, count/2)
	var pair int
	for i := range count {
		at := i * frameBytes
		l := int(int16(binary.LittleEndian.Uint16(frames[at:])))
		r := int(int16(binary.LittleEndian.Uint16(frames[at+2:])))
		if i%2 == 0 {
			pair = l + r
		} else if i/2 < len(mono) {
			// Averaging each pair of frames is a gentle low-pass before halving the rate.
			mono[i/2] = int16((pair + l + r) / 4)
		}
		g := from + (to-from)*float64(i+1)/float64(count)
		binary.LittleEndian.PutUint16(frames[at:], uint16(scale(l, g)))
		binary.LittleEndian.PutUint16(frames[at+2:], uint16(scale(r, g)))
	}
	return mono
}

func scale(s int, g float64) int16 {
	return int16(min(max(math.Round(float64(s)*g), math.MinInt16), math.MaxInt16))
}

// decoderArgs turns the input into the fixed PCM format on stdout, reading
// the input at its native rate when nothing downstream sets the pace.
func decoderArgs(input []string, isUnpaced bool) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostats", "-nostdin"}
	if isUnpaced {
		args = append(args, "-re")
	}
	args = append(args, input...)
	return append(args, "-vn", "-ac", "2", "-ar", fmt.Sprint(playRate), "-f", "s16le", "pipe:1")
}

// speakerArgs plays the fixed PCM format from stdin; nil for no speakers.
func speakerArgs(output string) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostats",
		"-f", "s16le", "-ar", fmt.Sprint(playRate), "-ch_layout", "stereo", "-i", "pipe:0"}
	switch {
	case output == "audiotoolbox":
		return append(args, "-f", "audiotoolbox", "-")
	case output == "pulse":
		return append(args, "-f", "pulse", "cc-fm")
	case output == "alsa":
		return append(args, "-f", "alsa", "default")
	case strings.HasPrefix(output, "file:"):
		return append(args, "-y", "-f", "wav", strings.TrimPrefix(output, "file:"))
	}
	return nil
}

func (p *Player) gain() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return gain(p.st.Volume)
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

// tail keeps the last max bytes written and passes each whole line to log.
type tail struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	pending []byte
	log     func(string)
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	if t.log != nil {
		t.pending = append(t.pending, b...)
		for {
			i := strings.IndexByte(string(t.pending), '\n')
			if i < 0 {
				break
			}
			if line := strings.TrimSpace(string(t.pending[:i])); line != "" {
				t.log(line)
			}
			t.pending = t.pending[i+1:]
		}
	}
	return len(b), nil
}

// Mute stops passing lines to log; Last still works.
func (t *tail) Mute() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.log = nil
}

// Last is the last line written, the one that usually says what went wrong.
func (t *tail) Last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
