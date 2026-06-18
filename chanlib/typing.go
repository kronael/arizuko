package chanlib

import (
	"sync"
	"time"
)

// DefaultTypingMaxTTL caps runaway typing when the agent stalls.
const DefaultTypingMaxTTL = 10 * time.Minute

// TypingRefresher re-emits a "composing" indicator every refreshRate until
// Set(jid, false), Stop, or maxTTL. send returning false in the loop cancels
// it (permanent failure — 403, bad JID). An initial send failure in Set is
// treated as transient: the loop starts anyway and retries on the next tick.
// clear is called on stop/TTL; pass nil if the platform has no stop primitive.
type TypingRefresher struct {
	send        func(jid string) bool
	clear       func(jid string)
	refreshRate time.Duration
	maxTTL      time.Duration

	mu     sync.Mutex
	active map[string]chan struct{}
}

func NewTypingRefresher(refreshRate, maxTTL time.Duration, send func(jid string) bool, clear func(jid string)) *TypingRefresher {
	return &TypingRefresher{
		send:        send,
		clear:       clear,
		refreshRate: refreshRate,
		maxTTL:      maxTTL,
		active:      make(map[string]chan struct{}),
	}
}

func (r *TypingRefresher) Set(jid string, on bool) {
	r.mu.Lock()
	if stop, ok := r.active[jid]; ok {
		close(stop)
		delete(r.active, jid)
	}
	if !on {
		r.mu.Unlock()
		if r.clear != nil {
			r.clear(jid)
		}
		return
	}
	stop := make(chan struct{})
	r.active[jid] = stop
	r.mu.Unlock()

	r.send(jid) // eager best-effort; loop retries on next tick if this fails
	go r.loop(jid, stop)
}

func (r *TypingRefresher) loop(jid string, stop chan struct{}) {
	deadline := time.After(r.maxTTL)
	t := time.NewTicker(r.refreshRate)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-deadline:
			r.mu.Lock()
			if cur, ok := r.active[jid]; ok && cur == stop {
				delete(r.active, jid)
			}
			r.mu.Unlock()
			if r.clear != nil {
				r.clear(jid)
			}
			return
		case <-t.C:
			select {
			case <-stop:
				return
			default:
				if !r.send(jid) {
					r.mu.Lock()
					if cur, ok := r.active[jid]; ok && cur == stop {
						delete(r.active, jid)
					}
					r.mu.Unlock()
					return
				}
			}
		}
	}
}

func (r *TypingRefresher) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for jid, stop := range r.active {
		close(stop)
		delete(r.active, jid)
	}
}
