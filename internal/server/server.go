// Package server exposes one player to any number of listeners over HTTP,
// usually on a unix socket that SSH can forward to a remote machine.
//
// The wire protocol is described in PROTOCOL.md at the repository root.
package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/code-akram/cc-fm-mod/internal/player"
	"github.com/code-akram/cc-fm-mod/internal/spectrum"
)

const (
	// ProtocolVersion is the first line of every stream.
	ProtocolVersion = 1
	// FPS is how many bar frames a second the stream carries while playing.
	FPS = 30
	// pollerTimeout is how long a /v1/frames client counts as listening.
	pollerTimeout = 5 * time.Second
)

// Info is the status as listeners see it: the player's, plus the server's own.
type Info struct {
	player.Status
	Version   string `json:"version"`
	Bands     int    `json:"bands"`
	FPS       int    `json:"fps"`
	Listeners int    `json:"listeners"`
}

type Server struct {
	Player  *player.Player
	version string

	anMu sync.Mutex
	an   *spectrum.Analyzer

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	// pollers are /v1/frames clients by id, with when each last asked.
	pollers map[string]time.Time

	// The latest bar frame for /v1/frames, its sequence number, and a channel
	// closed (and replaced) each time a new one lands, to wake waiting polls.
	frameMu sync.Mutex
	latest  string
	seq     uint64
	fresh   chan struct{}
}

// New builds the player and the server around it; cfg's callbacks are set here.
func New(cfg player.Config, bands int, version string) *Server {
	s := &Server{
		an:      spectrum.New(bands),
		version: version,
		clients: map[chan []byte]struct{}{},
		pollers: map[string]time.Time{},
		fresh:   make(chan struct{}),
	}
	cfg.OnSamples = func(samples []int16) {
		s.anMu.Lock()
		s.an.Push(samples)
		s.anMu.Unlock()
	}
	// The player starts stopped; only changes from there are worth a line.
	last := player.Status{State: player.Stopped}
	cfg.OnChange = func(st player.Status) {
		if st.State != last.State || st.Error != last.Error {
			log.Printf("%s %s%s", st.State, st.Title, prefixed(" · ", st.Error))
		}
		last = st
		s.broadcast(s.statusLine())
	}
	s.Player = player.New(cfg)
	return s
}

// Run sends bar frames while playing and keepalives otherwise, until ctx ends.
func (s *Server) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second / FPS)
	defer tick.Stop()
	var frame []byte
	idle := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if s.Player.Status().State != player.Playing {
			if idle++; idle%(5*FPS) == 0 {
				s.broadcast([]byte(".\n"))
			}
			continue
		}
		idle = 0
		s.anMu.Lock()
		frame = s.an.Frame(frame)
		s.anMu.Unlock()
		encoded := hex.EncodeToString(frame)
		s.publish(encoded)
		s.broadcast([]byte("B " + encoded + "\n"))
	}
}

// publish makes a frame the latest for /v1/frames and wakes waiting polls.
func (s *Server) publish(frame string) {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	s.latest = frame
	s.seq++
	close(s.fresh)
	s.fresh = make(chan struct{})
}

// frames answers GET /v1/frames?after=N&wait=MS&client=ID: the latest frame
// once one newer than N exists, or none after wait ms. For clients that make
// one request at a time (a Claude Code mod's $.http), where /v1/stream's
// endless response can't be read.
func (s *Server) frames(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseUint(q.Get("after"), 10, 64)
	wait := time.Second
	if ms, err := strconv.Atoi(q.Get("wait")); err == nil {
		wait = time.Duration(min(max(ms, 0), 5000)) * time.Millisecond
	}
	if id := q.Get("client"); id != "" && len(id) <= 64 {
		s.mu.Lock()
		s.pollers[id] = time.Now()
		s.mu.Unlock()
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		s.frameMu.Lock()
		seq, latest, fresh := s.seq, s.latest, s.fresh
		s.frameMu.Unlock()
		if seq > after && s.Player.Status().State == player.Playing {
			writeJSON(w, framesReply{Seq: seq, Frame: latest, Status: s.info()})
			return
		}
		select {
		case <-fresh:
		case <-timer.C:
			writeJSON(w, framesReply{Seq: seq, Status: s.info()})
			return
		case <-r.Context().Done():
			return
		}
	}
}

type framesReply struct {
	Seq    uint64 `json:"seq"`
	Frame  string `json:"frame"`
	Status Info   `json:"status"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.info())
	})
	mux.HandleFunc("POST /v1/play", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Source string `json:"source"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if err := player.CheckRemote(body.Source); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		s.anMu.Lock()
		s.an.Reset()
		s.anMu.Unlock()
		s.Player.Play(body.Source)
		writeJSON(w, s.info())
	})
	mux.HandleFunc("POST /v1/stop", func(w http.ResponseWriter, r *http.Request) {
		s.Player.Stop()
		writeJSON(w, s.info())
	})
	mux.HandleFunc("POST /v1/volume", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Volume *int `json:"volume"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if body.Volume == nil {
			http.Error(w, `want {"volume": 0-100}`, http.StatusBadRequest)
			return
		}
		s.Player.SetVolume(*body.Volume)
		writeJSON(w, s.info())
	})
	mux.HandleFunc("GET /v1/stream", s.stream)
	mux.HandleFunc("GET /v1/frames", s.frames)
	return mux
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	ch := make(chan []byte, 16)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
		s.broadcast(s.statusLine())
	}()
	// Everyone hears about the new listener, this one included.
	s.broadcast(s.statusLine())

	if _, err := io.WriteString(w, "cc-fm 1\n"); err != nil {
		return
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			if _, err := w.Write(line); err != nil {
				return
			}
			if len(ch) == 0 {
				flusher.Flush()
			}
		}
	}
}

// broadcast queues a line for every listener, dropping it for any that lag.
func (s *Server) broadcast(line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- line:
		default:
		}
	}
}

func (s *Server) info() Info {
	s.mu.Lock()
	listeners := len(s.clients)
	for id, seen := range s.pollers {
		if time.Since(seen) > pollerTimeout {
			delete(s.pollers, id)
			continue
		}
		listeners++
	}
	s.mu.Unlock()
	return Info{
		Status:    s.Player.Status(),
		Version:   s.version,
		Bands:     s.an.Bands(),
		FPS:       FPS,
		Listeners: listeners,
	}
}

func (s *Server) statusLine() []byte {
	b, _ := json.Marshal(s.info())
	return append(append([]byte("S "), b...), '\n')
}

func prefixed(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON decodes an optional JSON body; an empty body leaves v as is.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil && err != io.EOF {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
