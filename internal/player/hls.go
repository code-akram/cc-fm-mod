package player

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// liveHLS fetches a live HLS media playlist's segments with several requests
// in flight and writes their bodies to w in order, for ffmpeg to decode from
// a pipe.
//
// ffmpeg's own HLS reader fetches one segment at a time. googlevideo can take
// seconds to start answering each request, longer than the segment plays,
// so one at a time falls behind; overlapping the waits keeps up.
type liveHLS struct {
	client   *http.Client
	playlist string
	// inFlight is how many segment requests may run at once.
	inFlight int
	// behind is how many segments behind the newest to start, so the
	// player's buffer has something to read ahead into.
	behind int
	log    func(string, ...any)
}

type mediaPlaylist struct {
	target   time.Duration
	sequence int64
	segments []string
	initURI  string
	isEnded  bool
}

// fetchJob is one segment on its way; jobs are written out in order.
type fetchJob struct {
	seq  int64
	done chan struct{}
	body []byte
	err  error
}

func (h *liveHLS) run(ctx context.Context, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan *fetchJob, h.inFlight)
	produced := make(chan error, 1)
	go func() { produced <- h.produce(ctx, jobs) }()

	for job := range jobs {
		select {
		case <-job.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if job.err != nil {
			return job.err
		}
		if _, err := w.Write(job.body); err != nil {
			return err
		}
	}
	return <-produced
}

// produce follows the playlist and starts a fetch for each new segment,
// blocking while inFlight are already queued. It closes jobs when done.
func (h *liveHLS) produce(ctx context.Context, jobs chan<- *fetchJob) error {
	defer close(jobs)
	next := int64(-1)
	initURI := ""
	for {
		pl, err := h.fetchPlaylist(ctx)
		if err != nil {
			return err
		}
		if pl.initURI != "" && pl.initURI != initURI {
			initURI = pl.initURI
			if !h.enqueue(ctx, jobs, -1, pl.initURI) {
				return ctx.Err()
			}
		}

		first := pl.sequence
		last := first + int64(len(pl.segments)) - 1
		switch {
		case next < 0:
			next = max(first, last-int64(h.behind)+1)
		case next < first:
			h.log("hls: fell %d segments behind the playlist; skipping ahead", first-next)
			next = first
		}
		for ; next <= last; next++ {
			if !h.enqueue(ctx, jobs, next, pl.segments[next-first]) {
				return ctx.Err()
			}
		}
		if pl.isEnded {
			return nil
		}

		// New segments appear about once per target duration.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(max(pl.target/2, 500*time.Millisecond)):
		}
	}
}

func (h *liveHLS) enqueue(ctx context.Context, jobs chan<- *fetchJob, seq int64, uri string) bool {
	job := &fetchJob{seq: seq, done: make(chan struct{})}
	select {
	case jobs <- job:
	case <-ctx.Done():
		return false
	}
	go func() {
		defer close(job.done)
		job.body, job.err = h.fetchSegment(ctx, uri)
	}()
	return true
}

func (h *liveHLS) fetchSegment(ctx context.Context, uri string) ([]byte, error) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		body, err := h.get(ctx, uri)
		if err == nil {
			return body, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("hls segment: %w", lastErr)
}

func (h *liveHLS) fetchPlaylist(ctx context.Context) (mediaPlaylist, error) {
	body, err := h.get(ctx, h.playlist)
	if err != nil {
		return mediaPlaylist{}, fmt.Errorf("hls playlist: %w", err)
	}
	return parseMediaPlaylist(h.playlist, string(body))
}

func (h *liveHLS) get(ctx context.Context, uri string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// parseMediaPlaylist reads the parts of an HLS media playlist the fetcher
// uses, resolving segment URIs against base.
func parseMediaPlaylist(base, text string) (mediaPlaylist, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return mediaPlaylist{}, err
	}
	resolve := func(ref string) string {
		u, err := baseURL.Parse(strings.TrimSpace(ref))
		if err != nil {
			return ref
		}
		return u.String()
	}

	pl := mediaPlaylist{target: 2 * time.Second}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	isHeader := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case isHeader && line != "#EXTM3U":
			return mediaPlaylist{}, errors.New("hls playlist: missing #EXTM3U")
		case line == "#EXTM3U":
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			if n, err := strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64); err == nil {
				pl.target = time.Duration(n * float64(time.Second))
			}
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			pl.sequence, _ = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			if uri := attribute(line, "URI"); uri != "" {
				pl.initURI = resolve(uri)
			}
		case line == "#EXT-X-ENDLIST":
			pl.isEnded = true
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			pl.segments = append(pl.segments, resolve(line))
		}
		isHeader = false
	}
	if err := sc.Err(); err != nil {
		return mediaPlaylist{}, err
	}
	if isHeader {
		return mediaPlaylist{}, errors.New("hls playlist: empty")
	}
	return pl, nil
}

// attribute reads a quoted attribute from an HLS tag line: URI="init.mp4".
func attribute(line, name string) string {
	key := name + `="`
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}
