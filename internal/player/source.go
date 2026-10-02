package player

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// resolve turns a source into ffmpeg input arguments and a title.
//
//	demo               a built-in synthetic track, no network needed
//	lavfi:<graph>      any ffmpeg-generated signal
//	/path/to/file      a local file, looped
//	YouTube, clau.de   resolved to a direct stream by yt-dlp
//	any other URL      handed to ffmpeg as is
func resolve(ctx context.Context, source string, ytDlpArgs []string) ([]string, string, error) {
	if source == "demo" {
		return []string{"-f", "lavfi", "-i", demoGraph}, "demo signal", nil
	}
	if graph, ok := strings.CutPrefix(source, "lavfi:"); ok {
		return []string{"-f", "lavfi", "-i", graph}, "test signal", nil
	}

	u, err := url.Parse(source)
	if err != nil || u.Scheme == "" {
		if _, err := os.Stat(source); err != nil {
			return nil, "", err
		}
		return []string{"-stream_loop", "-1", "-i", source}, filepath.Base(source), nil
	}

	if !needsYtDlp(u.Hostname()) {
		return []string{"-i", source}, source, nil
	}

	yt, err := YtDlpPath()
	if err != nil {
		return nil, "", err
	}
	args := append(append([]string{}, ytDlpArgs...),
		"--no-warnings", "--no-playlist", "-f", "bestaudio/best",
		"--print", "title", "--print", "urls", source)
	out, err := exec.CommandContext(ctx, yt, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, "", fmt.Errorf("yt-dlp: %s", lastLine(string(exit.Stderr)))
		}
		return nil, "", fmt.Errorf("yt-dlp: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, "", errors.New("yt-dlp printed no stream URL")
	}

	return []string{"-i", strings.TrimSpace(lines[1])}, strings.TrimSpace(lines[0]), nil
}

func needsYtDlp(host string) bool {
	switch strings.TrimPrefix(host, "www.") {
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be", "clau.de":
		return true
	}
	return false
}

// YtDlpPath prefers the yt-dlp on PATH (a package manager keeps it current)
// and falls back to the copy cc-fm keeps in ~/.cc-fm/bin.
func YtDlpPath() (string, error) {
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".cc-fm", "bin", "yt-dlp")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("yt-dlp not found: install it (brew install yt-dlp / pacman -S yt-dlp)")
}

// DetectOutput picks the audio device ffmpeg should play to on this machine.
// It returns null, visualizer only, where there is nothing to play to.
func DetectOutput() string {
	switch runtime.GOOS {
	case "darwin":
		return "audiotoolbox"
	case "linux":
		if exec.Command("pactl", "info").Run() == nil {
			return "pulse"
		}
		if cards, err := os.ReadFile("/proc/asound/cards"); err == nil && !strings.Contains(string(cards), "no soundcards") && len(strings.TrimSpace(string(cards))) > 0 {
			return "alsa"
		}
	}
	return "null"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// demoGraph is a synthetic track for testing without the network: a kick on
// every beat, a sawtooth chord walking through four roots, off-beat hats and
// a quiet pink-noise bed, so every band has something in it.
const demoGraph = "aevalsrc=exprs=" +
	"0.55*sin(2*PI*52*t)*exp(-11*mod(t\\,0.5))" +
	"+0.07*(2*mod(t*(110+27.5*floor(mod(t\\,8)/2))\\,1)-1)" +
	"+0.05*(2*mod(t*1.5*(110+27.5*floor(mod(t\\,8)/2))\\,1)-1)" +
	"+0.04*(2*mod(t*2*(110+27.5*floor(mod(t\\,8)/2))\\,1)-1)" +
	":s=44100[k];anoisesrc=color=pink:amplitude=0.6[n];" +
	"anoisesrc=color=white:amplitude=0.5,highpass=f=6000,volume='if(isnan(t)\\,0\\,0.6*exp(-40*mod(t+0.25\\,0.5)))':eval=frame[h];" +
	"[k][n][h]amix=inputs=3:normalize=0:weights=1 0.015 1[out0]"
