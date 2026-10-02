// Command cc-fm plays claude.fm on this machine and serves its bars to
// Claude Code sessions, here or across SSH.
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/code-akram/cc-fm-mod/internal/player"
	"github.com/code-akram/cc-fm-mod/internal/server"
)

// version is set at release time with -ldflags "-X main.version=..."; a
// `go install …@vX.Y.Z` build reads it from the module instead.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(info.Main.Version, "v") {
		version = strings.TrimPrefix(info.Main.Version, "v")
	}
}

const usage = `cc-fm — claude.fm for Claude Code

usage:
  cc-fm serve [flags]      run the player (one per machine)
  cc-fm play [source]      start playing (default: https://clau.de/radio)
  cc-fm stop               stop playing
  cc-fm vol <0-100>        set the volume
  cc-fm status             print the player's status as JSON
  cc-fm watch              draw the bars in this terminal
  cc-fm doctor             check ffmpeg, yt-dlp and the audio device
  cc-fm version

The socket is $CC_FM_SOCKET, or ~/.cc-fm/fm.sock. Run "cc-fm serve -h" for its flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "play":
		source := strings.Join(args, " ")
		err = call("POST", "/v1/play", map[string]string{"source": source})
	case "stop":
		err = call("POST", "/v1/stop", nil)
	case "vol", "volume":
		var v int
		if len(args) != 1 {
			err = errors.New("usage: cc-fm vol <0-100>")
		} else if v, err = strconv.Atoi(args[0]); err == nil {
			err = call("POST", "/v1/volume", map[string]int{"volume": v})
		}
	case "status":
		err = call("GET", "/v1/status", nil)
	case "watch":
		err = watch()
	case "doctor":
		err = doctor()
	case "version", "--version", "-v":
		fmt.Println("cc-fm", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cc-fm:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sock := fs.String("socket", socketPath(), "unix socket to listen on")
	tcp := fs.String("tcp", "", "also listen on this TCP address (e.g. 127.0.0.1:47130)")
	output := fs.String("output", "auto", "audio device: auto, audiotoolbox, pulse, alsa, null, or file:<path> to record a WAV")
	volume := fs.Int("volume", 70, "starting volume, 0-100 (default: the last volume set, else 70)")
	delay := fs.Int("delay-ms", 0, "hold the speakers back so remote bars line up with the sound")
	bands := fs.Int("bands", 32, "bars per frame")
	autoplay := fs.String("autoplay", "", `start playing this source at once ("default" for claude.fm)`)
	ytArgs := fs.String("yt-dlp-args", "", `extra yt-dlp arguments, e.g. "--cookies-from-browser firefox"`)
	record := fs.String("record", "", "debugging: also write what plays to this file, as 24 kHz mono s16le")
	_ = fs.Parse(args)

	// The last volume set wins over the default, but not over --volume.
	saved := loadState()
	isVolumeSet := false
	fs.Visit(func(f *flag.Flag) { isVolumeSet = isVolumeSet || f.Name == "volume" })
	if !isVolumeSet && saved.Volume != nil {
		*volume = *saved.Volume
	}

	if *output == "auto" {
		*output = player.DetectOutput()
	}

	ln, err := listenUnix(*sock)
	if err != nil {
		return err
	}
	defer removeIfOurs(*sock)

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.SetPrefix("cc-fm: ")
	var recording io.Writer
	if *record != "" {
		f, err := os.Create(*record)
		if err != nil {
			return err
		}
		defer f.Close()
		recording = f
		log.Printf("recording what plays to %s (24 kHz mono s16le) from the first sample on", *record)
	}
	srv := server.New(player.Config{
		Output:    *output,
		Volume:    *volume,
		DelayMs:   *delay,
		YtDlpArgs: strings.Fields(*ytArgs),
		Log:       func(line string) { log.Print(line) },
		Record:    recording,
		OnVolume: func(v int) {
			if err := saveState(state{Volume: &v}); err != nil {
				log.Print("saving the volume: ", err)
			}
		},
	}, *bands, version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Run(ctx)

	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() { _ = httpSrv.Serve(ln) }()
	if *tcp != "" {
		tln, err := net.Listen("tcp", *tcp)
		if err != nil {
			return err
		}
		go func() { _ = httpSrv.Serve(tln) }()
	}

	fmt.Fprintf(os.Stderr, "cc-fm %s listening on %s (audio: %s)\n", version, *sock, *output)
	if *output == "null" {
		fmt.Fprintln(os.Stderr, "cc-fm: no audio device here; serving bars only")
	}
	switch *autoplay {
	case "":
	case "default":
		srv.Player.Play("")
	default:
		srv.Player.Play(*autoplay)
	}

	<-ctx.Done()
	srv.Player.Stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdown)
	return nil
}

// listenUnix listens on path, owner-only. A socket left behind by a dead
// player is replaced; one a live player answers on is an error, so there is
// only ever one player per socket.
func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("a player is already running on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	// The listener mustn't unlink the path itself on Close; removeIfOurs does.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	owned, _ = os.Stat(path)
	return ln, nil
}

// removeIfOurs deletes the socket on exit unless another player has since
// taken the path over: a quick restart must not unlink its successor's socket.
func removeIfOurs(path string) {
	if owned == nil {
		return
	}
	if now, err := os.Stat(path); err == nil && os.SameFile(owned, now) {
		os.Remove(path)
	}
}

// owned is the socket file this process created.
var owned os.FileInfo

// state is what the player remembers between runs, in ~/.cc-fm/state.json.
type state struct {
	Volume *int `json:"volume,omitempty"`
}

func statePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cc-fm", "state.json")
}

// loadState reads the saved state; missing or unreadable, it starts empty.
func loadState() state {
	var s state
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.Volume != nil && (*s.Volume < 0 || *s.Volume > 100) {
		s.Volume = nil
	}
	return s
}

// saveState writes the state through a temporary file, so a crash mid-write
// never leaves a half-written one behind.
func saveState(s state) error {
	path := statePath()
	if path == "" {
		return errors.New("no home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func socketPath() string {
	if p := os.Getenv("CC_FM_SOCKET"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "cc-fm.sock")
	}
	return filepath.Join(home, ".cc-fm", "fm.sock")
}

func client() *http.Client {
	sock := socketPath()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

func call(method, path string, body any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, "http://cc-fm"+path, r)
	resp, err := client().Do(req)
	if err != nil {
		return fmt.Errorf("no player on %s (run: cc-fm serve)", socketPath())
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return errors.New(strings.TrimSpace(string(out)))
	}
	fmt.Print(string(out))
	return nil
}

// watch draws the stream's bars on one line of this terminal.
func watch() error {
	resp, err := client().Get("http://cc-fm/v1/stream")
	if err != nil {
		return fmt.Errorf("no player on %s (run: cc-fm serve)", socketPath())
	}
	defer resp.Body.Close()

	const glyphs = " ▁▂▃▄▅▆▇█"
	ramp := []rune(glyphs)
	status := ""
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "S "):
			var info server.Info
			if json.Unmarshal([]byte(line[2:]), &info) == nil {
				status = fmt.Sprintf("%s · vol %d · %d listening · %s", info.State, info.Volume, info.Listeners, info.Title)
				if info.Error != "" {
					status += " · " + info.Error
				}
				fmt.Printf("\r\033[K%s", status)
			}
		case strings.HasPrefix(line, "B "):
			bars, err := hex.DecodeString(line[2:])
			if err != nil {
				continue
			}
			var b strings.Builder
			for i, v := range bars {
				r, g, bl := gradient(float64(i) / float64(max(len(bars)-1, 1)))
				fmt.Fprintf(&b, "\033[38;2;%d;%d;%dm%c", r, g, bl, ramp[int(v)*(len(ramp)-1)/255])
			}
			fmt.Printf("\r\033[K%s\033[0m  %s", b.String(), status)
		}
	}
	fmt.Println()
	return sc.Err()
}

// gradient runs from Claude's coral to a soft violet.
func gradient(t float64) (int, int, int) {
	lerp := func(a, b int) int { return a + int(float64(b-a)*t) }
	return lerp(0xd9, 0x9b), lerp(0x77, 0x87), lerp(0x57, 0xf5)
}

func doctor() error {
	ok := true
	check := func(name, detail string, err error) {
		if err != nil {
			ok = false
			fmt.Printf("✗ %-8s %v\n", name, err)
			return
		}
		fmt.Printf("✓ %-8s %s\n", name, detail)
	}

	ff, err := exec.LookPath("ffmpeg")
	check("ffmpeg", ff, err)
	yt, err := player.YtDlpPath()
	if err == nil {
		out, verr := exec.Command(yt, "--version").Output()
		check("yt-dlp", fmt.Sprintf("%s (%s)", yt, strings.TrimSpace(string(out))), verr)
	} else {
		check("yt-dlp", "", err)
	}
	out := player.DetectOutput()
	if out == "null" {
		fmt.Println("! audio    no device found; the player will serve bars only")
	} else {
		fmt.Printf("✓ audio    %s\n", out)
	}
	sock := socketPath()
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		c.Close()
		fmt.Printf("✓ player   answering on %s\n", sock)
	} else {
		fmt.Printf("- player   not running on %s\n", sock)
	}
	if !ok {
		return errors.New("some checks failed")
	}
	return nil
}
