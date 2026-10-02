package player

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// burstyReader hands over a head start at once, stalls, then delivers in
// real time until it has given total audio.
type burstyReader struct {
	headStart, stall, total time.Duration
	given                   time.Duration
	stalled                 bool
}

func (b *burstyReader) Read(p []byte) (int, error) {
	if b.given >= b.total {
		return 0, io.EOF
	}
	const step = 100 * time.Millisecond
	n := min(len(p), int(step*playRate*frameBytes/time.Second))
	switch {
	case b.given < b.headStart:
	case !b.stalled:
		b.stalled = true
		time.Sleep(b.stall)
	default:
		time.Sleep(step)
	}
	b.given += time.Duration(n) * time.Second / (playRate * frameBytes)
	clear(p[:n])
	return n, nil
}

// pacedWriter takes audio in real time, as a sound card does.
type pacedWriter struct{ played time.Duration }

func (w *pacedWriter) Write(p []byte) (int, error) {
	d := time.Duration(len(p)) * time.Second / (playRate * frameBytes)
	time.Sleep(d)
	w.played += d
	return len(p), nil
}

func runBuffered(t *testing.T, r *burstyReader) (logs []string, played time.Duration) {
	t.Helper()
	var mu sync.Mutex
	p := New(Config{Log: func(line string) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, line)
	}})
	w := &pacedWriter{}
	p.bufferedPump(context.Background(), r, w)
	mu.Lock()
	defer mu.Unlock()
	return logs, w.played
}

func TestBufferAbsorbsStall(t *testing.T) {
	// 3 s queued, then a 1.5 s stall: the speakers never notice.
	logs, played := runBuffered(t, &burstyReader{headStart: 3 * time.Second, stall: 1500 * time.Millisecond, total: 4 * time.Second})
	if len(logs) != 0 {
		t.Fatalf("logs = %q, want none", logs)
	}
	if played < 3900*time.Millisecond {
		t.Fatalf("played %v, want all 4s", played)
	}
}

func TestBufferRebuffersAfterLongStall(t *testing.T) {
	// Barely the prefill queued, then a stall longer than it: one clean pause.
	logs, played := runBuffered(t, &burstyReader{headStart: 2200 * time.Millisecond, stall: 3 * time.Second, total: 3500 * time.Millisecond})
	if len(logs) != 1 || !strings.Contains(logs[0], "rebuffer") {
		t.Fatalf("logs = %q, want one rebuffer line", logs)
	}
	if played < 3400*time.Millisecond {
		t.Fatalf("played %v, want all 3.5s, nothing dropped", played)
	}
}

func TestURLInputStartsBehindLiveEdge(t *testing.T) {
	args := urlInput("https://manifest.googlevideo.com/api/manifest/hls_playlist/expire/1/index.m3u8")
	if !strings.Contains(strings.Join(args, " "), "-live_start_index -6") {
		t.Fatalf("args = %q, want -live_start_index -6", args)
	}
	if strings.Contains(strings.Join(urlInput("https://example.com/a.mp3"), " "), "live_start_index") {
		t.Fatal("a plain URL got an HLS-only option")
	}
}
