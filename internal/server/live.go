package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Live updates. Pages that show what other people change (the dashboard,
// a production's overview, schedule, mic chart, readiness and needs) listen
// on /live, a Server-Sent Events stream (/events is taken by the schedule).
// When anyone saves something, every listener gets a bare "change" message
// and re-fetches its own page, so the stream never carries data and needs
// no permission checks beyond being signed in. That's checked against the
// stored session only, never User Management (see loadSession), and a
// signed-out stream gets a 401 rather than a redirect to /login.

const (
	maxListeners = 1000
	livePing     = 25 * time.Second
	liveWrite    = 10 * time.Second
)

type hub struct {
	mu     sync.Mutex
	subs   map[chan struct{}]struct{}
	closed bool
}

func newHub() *hub { return &hub{subs: map[chan struct{}]struct{}{}} }

// subscribe returns a channel that receives a value after each change.
// It's false when the server is full or shutting down.
func (h *hub) subscribe() (chan struct{}, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.subs) >= maxListeners {
		return nil, false
	}
	ch := make(chan struct{}, 1)
	h.subs[ch] = struct{}{}
	return ch, true
}

func (h *hub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, ch)
}

// publish tells every listener something changed. A listener that hasn't
// caught up yet already has one waiting, which is all it needs.
func (h *hub) publish() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// close ends every stream, so a graceful shutdown doesn't wait on them.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		close(ch)
	}
	h.subs = map[chan struct{}]struct{}{}
}

// CloseStreams ends the live update streams; call it on shutdown.
func (s *Server) CloseStreams() { s.live.close() }

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.live.subscribe()
	if !ok {
		http.Error(w, "too many listeners", http.StatusServiceUnavailable)
		return
	}
	defer s.live.unsubscribe(ch)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // in case nginx is in front
	rc := http.NewResponseController(w)
	// The server's WriteTimeout would cut the stream; each write gets its
	// own deadline instead, which also drops clients that stop reading.
	send := func(msg string) bool {
		rc.SetWriteDeadline(time.Now().Add(liveWrite))
		if _, err := fmt.Fprint(w, msg); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	// Pings let the page notice a dead connection (a phone waking up).
	if !send("retry: 5000\ndata: ping\n\n") {
		return
	}
	ping := time.NewTicker(livePing)
	defer ping.Stop()
	for {
		var msg string
		select {
		case <-r.Context().Done():
			return
		case _, open := <-ch:
			if !open {
				return
			}
			msg = "data: change\n\n"
		case <-ping.C:
			msg = "data: ping\n\n"
		}
		if !send(msg) {
			return
		}
	}
}

// announceChanges publishes a change after every successful POST that can
// alter what the live pages show.
func (s *Server) announceChanges(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !changesData(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if rec.status < 400 {
			s.live.publish()
		}
	})
}

// changesData is false for posts that only touch accounts and sessions.
func changesData(path string) bool {
	for _, p := range []string{"/login", "/logout", "/setup", "/account/", "/admin/users"} {
		if strings.HasPrefix(path, p) {
			return false
		}
	}
	return true
}
