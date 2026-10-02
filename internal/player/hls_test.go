package player

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noLog(string, ...any) {}

// slowServer serves a live playlist that gains a segment every segDur, and
// answers every segment request only after delay, the way googlevideo can.
func slowServer(t *testing.T, segDur, delay time.Duration, window int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	start := time.Now()
	var inFlight, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newest := 100 + int64(time.Since(start)/segDur)
		if r.URL.Path == "/live.m3u8" {
			first := newest - int64(window) + 1
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%g\n#EXT-X-MEDIA-SEQUENCE:%d\n",
				segDur.Seconds(), first)
			for seq := first; seq <= newest; seq++ {
				fmt.Fprintf(w, "#EXTINF:%g,\nseg/%d\n", segDur.Seconds(), seq)
			}
			return
		}
		seq := strings.TrimPrefix(r.URL.Path, "/seg/")
		if n := inFlight.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		defer inFlight.Add(-1)
		time.Sleep(delay)
		fmt.Fprintf(w, "%s\n", seq)
	}))
	t.Cleanup(srv.Close)
	return srv, &peak
}

func TestLiveHLSKeepsUpDespiteSlowSegments(t *testing.T) {
	// Every request takes 2.5x as long as its segment plays: one at a time
	// would fall further behind with each segment.
	segDur, delay := 200*time.Millisecond, 500*time.Millisecond
	srv, peak := slowServer(t, segDur, delay, 15)
	h := &liveHLS{client: srv.Client(), playlist: srv.URL + "/live.m3u8", inFlight: 4, behind: 6, log: noLog}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var out bytes.Buffer
	_ = h.run(ctx, &out)

	var seqs []int64
	for _, line := range strings.Fields(out.String()) {
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			t.Fatalf("bad segment body %q", line)
		}
		seqs = append(seqs, n)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("segments out of order or skipped: %v", seqs)
		}
	}
	// In 4 s the playlist gains 20 segments; keeping up means writing about
	// that many, plus the 6 started behind, less those still in flight.
	if len(seqs) < 18 {
		t.Fatalf("wrote %d segments in 4s, want at least 18 to keep up (got %v)", len(seqs), seqs)
	}
	if peak.Load() < 2 {
		t.Fatalf("at most %d request in flight, want several", peak.Load())
	}
}

func TestLiveHLSStartsBehindNewest(t *testing.T) {
	srv, _ := slowServer(t, time.Hour, 0, 15) // a playlist that holds still: 86..100
	h := &liveHLS{client: srv.Client(), playlist: srv.URL + "/live.m3u8", inFlight: 4, behind: 6, log: noLog}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	_ = h.run(ctx, &out)
	if got := strings.Fields(out.String()); len(got) != 6 || got[0] != "95" || got[5] != "100" {
		t.Fatalf("wrote %v, want the newest 6 segments, 95..100, once each", got)
	}
}

func TestParseMediaPlaylist(t *testing.T) {
	pl, err := parseMediaPlaylist("https://host/a/live.m3u8", `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:4799501
#EXT-X-MAP:URI="init.mp4"
#EXTINF:2.0,
seg/1
#EXTINF:2.0,
https://other/seg/2
#EXT-X-ENDLIST
`)
	if err != nil {
		t.Fatal(err)
	}
	if pl.target != 2*time.Second || pl.sequence != 4799501 || !pl.isEnded {
		t.Fatalf("parsed %+v", pl)
	}
	if pl.initURI != "https://host/a/init.mp4" {
		t.Fatalf("init %q", pl.initURI)
	}
	if len(pl.segments) != 2 || pl.segments[0] != "https://host/a/seg/1" || pl.segments[1] != "https://other/seg/2" {
		t.Fatalf("segments %q", pl.segments)
	}
	if _, err := parseMediaPlaylist("https://host/x", "not a playlist"); err == nil {
		t.Fatal("accepted a non-playlist")
	}
}

// TestLiveHLSDecodes feeds real HLS audio segments, fetched in parallel and
// joined end to end, to ffmpeg, as the player does.
func TestLiveHLSDecodes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	gen := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=f=440:d=6",
		"-c:a", "aac", "-f", "hls", "-hls_time", "1", "-hls_list_size", "0",
		"-hls_playlist_type", "vod", dir+"/live.m3u8")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("making HLS: %v\n%s", err, out)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	h := &liveHLS{client: srv.Client(), playlist: srv.URL + "/live.m3u8", inFlight: 4, behind: 1000, log: noLog}
	dec := exec.Command("ffmpeg", "-v", "error", "-i", "pipe:0", "-ac", "2", "-ar", "48000", "-f", "s16le", "pipe:1")
	feed, _ := dec.StdinPipe()
	var pcm, stderr bytes.Buffer
	dec.Stdout, dec.Stderr = &pcm, &stderr
	if err := dec.Start(); err != nil {
		t.Fatal(err)
	}
	if err := h.run(context.Background(), feed); err != nil {
		t.Fatal(err)
	}
	feed.Close()
	if err := dec.Wait(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, stderr.String())
	}
	if secs := float64(pcm.Len()) / (playRate * frameBytes); secs < 5.8 || secs > 6.2 {
		t.Fatalf("decoded %.2fs, want about 6s", secs)
	}
}
