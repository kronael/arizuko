package chanlib

import (
	"sync"
	"time"
)

// TypingRefresher keeps platform typing indicators alive across long agent
// runs. Adapters wrap their one-shot "send composing" call; the refresher
// drives a per-JID ticker that re-emits at refreshRate and auto-stops
// after maxTTL so a lost off-signal can't leak an indicator forever.
//
// Send is called once immediately on Set(jid, true) and then every
// refreshRate until Set(jid, false), shutdown, or maxTTL elapses. Clear
// is called once on Set(jid, false); pass nil if the platform has no
// explicit paused/stop presence (Telegram, Discord).
type TypingRefresher struct {
	send        func(jid string)
	clear       func(jid string)
	refreshRate time.Duration
	maxTTL      time.Duration

	mu     sync.Mutex
	active map[string]chan struct{}
}

func NewTypingRefresher(refreshRate, maxTTL time.Duration, send, clear func(jid string)) *TypingRefresher {
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

	r.send(jid)
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
			return
		case <-t.C:
			// Double-check stop to avoid a race where the ticker and the
			// stop signal fire simultaneously and select picks the ticker.
			select {
			case <-stop:
				return
			default:
				r.send(jid)
			}
		}
	}
}

// Stop cancels every active refresh loop. Call on adapter shutdown.
func (r *TypingRefresher) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for jid, stop := range r.active {
		close(stop)
		delete(r.active, jid)
	}
}
