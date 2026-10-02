package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/code-akram/cc-fm-mod/internal/player"
)

func getFrames(t *testing.T, url string) framesReply {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r framesReply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFramesWhileStoppedAnswersEmptyAfterWait(t *testing.T) {
	s := New(player.Config{Output: "null"}, 32, "test")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	start := time.Now()
	r := getFrames(t, srv.URL+"/v1/frames?after=0&wait=150&client=a")
	if took := time.Since(start); took < 140*time.Millisecond || took > time.Second {
		t.Fatalf("answered after %v, want about the 150ms wait", took)
	}
	if r.Frame != "" || r.Status.State != player.Stopped {
		t.Fatalf("got frame %q state %s, want no frame while stopped", r.Frame, r.Status.State)
	}
	if r.Status.Listeners != 1 {
		t.Fatalf("listeners = %d, want the poller counted", r.Status.Listeners)
	}
}

func TestFramesWhilePlayingAnswersFresh(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	s := New(player.Config{Output: "null"}, 32, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	s.Player.Play("demo")
	defer s.Player.Stop()

	var r framesReply
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if r = getFrames(t, srv.URL+"/v1/frames?after=0&wait=500"); r.Frame != "" {
			break
		}
	}
	if len(r.Frame) != 64 || r.Status.State != player.Playing {
		t.Fatalf("frame %q (state %s), want a 32-band hex frame while playing", r.Frame, r.Status.State)
	}

	// Asking for frames after the one just seen waits for the next, not long.
	start := time.Now()
	next := getFrames(t, srv.URL+"/v1/frames?after="+strconv.FormatUint(r.Seq, 10)+"&wait=2000")
	if next.Seq <= r.Seq || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("next seq %d after %d took %v, want a newer frame within a few ticks", next.Seq, r.Seq, time.Since(start))
	}
}
