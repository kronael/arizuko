package main

import (
	"fmt"
	"net/http"
	"sync"
)

// hub is the SSE broker keyed by "folder/topic".
type hub struct {
	mu   sync.Mutex
	subs map[string][]chan string
}

func newHub() *hub {
	return &hub{subs: make(map[string][]chan string)}
}

func hubKey(folder, topic string) string { return folder + "/" + topic }

// subscribe registers a new listener and returns a channel + unsubscribe func.
func (h *hub) subscribe(folder, topic string) (<-chan string, func()) {
	ch := make(chan string, 16)
	k := hubKey(folder, topic)
	h.mu.Lock()
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
	k := hubKey(folder, topic)
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
func serveSSE(w http.ResponseWriter, r *http.Request, ch <-chan string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			if flusher != nil {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}
