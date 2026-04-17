package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Caps to bound resource use under unauthenticated-subscriber flood.
// maxHubKeys limits distinct (folder,topic) pairs; maxSubsPerKey limits
// subscribers on a single key. Both are deliberately generous so normal
// multi-tab usage is never impacted.
const (
	maxHubKeys    = 10000
	maxSubsPerKey = 256
)

// sseKeepalive is the heartbeat interval written as an SSE comment
// (`: ping`) to detect half-open TCP connections and keep load balancers
// from idling the stream out.
const sseKeepalive = 15 * time.Second

// sseWriteTimeout bounds any single write (including the flush). A
// client that stops reading will block writes; without a deadline the
// goroutine + buffered channel leak for minutes.
const sseWriteTimeout = 10 * time.Second

// hub is the SSE broker keyed by "folder/topic".
type hub struct {
	mu   sync.Mutex
	subs map[string][]chan string
}

func newHub() *hub {
	return &hub{subs: make(map[string][]chan string)}
}

// canSubscribe returns true if the hub has headroom for another
// subscription. Callers check this before subscribe() to reject floods
// with a clean 503 rather than OOMing.
func (h *hub) canSubscribe() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) < maxHubKeys
}

// subscribe registers a new listener and returns a channel + unsubscribe func.
// Returns (nil, nil) if the per-key subscriber cap is reached.
func (h *hub) subscribe(folder, topic string) (<-chan string, func()) {
	ch := make(chan string, 16)
	k := folder + "/" + topic
	h.mu.Lock()
	if len(h.subs[k]) >= maxSubsPerKey {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subs[k] = append(h.subs[k], ch)
	h.mu.Unlock()
	unsub := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		list := h.subs[k]
		for i, c := range list {
			if c == ch {
				h.subs[k] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(h.subs[k]) == 0 {
			delete(h.subs, k)
		}
		close(ch)
	}
	return ch, unsub
}

// publish sends an SSE event to all subscribers of folder/topic.
func (h *hub) publish(folder, topic, event, data string) {
	k := folder + "/" + topic
	h.mu.Lock()
	list := make([]chan string, len(h.subs[k]))
	copy(list, h.subs[k])
	h.mu.Unlock()
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	for _, ch := range list {
		select {
		case ch <- msg:
		default: // slow client — drop
		}
	}
}

// serveSSE writes SSE events from ch to w until the client disconnects.
// Emits a periodic keepalive comment and applies a per-write deadline so
// a stuck client can't pin the goroutine indefinitely.
func serveSSE(w http.ResponseWriter, r *http.Request, ch <-chan string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	rc := http.NewResponseController(w)

	writeWithDeadline := func(s string) error {
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if _, err := fmt.Fprint(w, s); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	if writeWithDeadline(": ok\n\n") != nil {
		return
	}

	tick := time.NewTicker(sseKeepalive)
	defer tick.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if writeWithDeadline(msg) != nil {
				return
			}
		case <-tick.C:
			if writeWithDeadline(": ping\n\n") != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
