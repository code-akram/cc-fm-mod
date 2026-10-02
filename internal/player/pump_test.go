package player

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// stallingReader hands over a head start of audio at once, as the decoder
// does while the speakers' queue fills, then audio in real time, stalling
// once along the way.
type stallingReader struct {
	headStart int
	chunks    int
	stall     time.Duration
}

func (s *stallingReader) Read(b []byte) (int, error) {
	if s.chunks == 0 {
		return 0, io.EOF
	}
	s.chunks--
	// 4800 frames is 100 ms of audio.
	n := min(len(b), 4800*frameBytes)
	if s.headStart > 0 {
		s.headStart--
	} else {
		time.Sleep(100 * time.Millisecond)
		if s.chunks == 3 {
			time.Sleep(s.stall)
		}
	}
	clear(b[:n])
	return n, nil
}

func pumpLogs(t *testing.T, stall time.Duration) []string {
	t.Helper()
	var mu sync.Mutex
	var logs []string
	p := New(Config{Log: func(line string) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, line)
	}})
	p.pump(&stallingReader{headStart: 3, chunks: 9, stall: stall}, io.Discard)
	mu.Lock()
	defer mu.Unlock()
	return logs
}

func TestPumpReportsUnderrun(t *testing.T) {
	logs := pumpLogs(t, 600*time.Millisecond)
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "underrun:") {
		t.Fatalf("logs = %q, want one underrun line", logs)
	}
}

func TestPumpQuietWhenOnTime(t *testing.T) {
	if logs := pumpLogs(t, 0); len(logs) != 0 {
		t.Fatalf("logs = %q, want none", logs)
	}
}
