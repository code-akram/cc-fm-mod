package player

import (
	"os/exec"
	"testing"
)

// The demo graph is a long hand-escaped string; make sure ffmpeg accepts it.
func TestDemoGraphParses(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", demoGraph, "-t", "1", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg rejected the demo graph: %v\n%s", err, out)
	}
}
