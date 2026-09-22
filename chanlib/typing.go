package chanlib

import (
	"sync"
	"time"
)

// DefaultTypingMaxTTL is the backstop that stops typing when routd's
// Typing(off) at turn end never arrives. It is 90% of the run wall, read from
// the adapter's own RUNED_RUN_TIMEOUT, and MUST land strictly below it: a
// backstop equal to the wall fires no earlier than the container's own
// SIGKILL, so a stalled turn showed "typing" for twenty minutes and then went
// silent (BUGS T1). An indicator that stops early is honest about not knowing.
var DefaultTypingMaxTTL = typingMaxTTL(EnvDur("RUNED_RUN_TIMEOUT", 20*time.Minute))

// minTypingMaxTTL floors the derivation so an absurd RUNED_RUN_TIMEOUT cannot
// kill the indicator before the first refresh.
const minTypingMaxTTL = time.Minute

func typingMaxTTL(runTTL time.Duration) time.Duration {
	return max(runTTL*9/10, minTypingMaxTTL)
}

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
	// Stop WAITS on wg: closing the stop channel only asks, and a loop mid-send
	// would keep sending after Stop returns (BUGS L168).
	wg sync.WaitGroup
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
	r.wg.Add(1)
	go r.loop(jid, stop)
}

func (r *TypingRefresher) loop(jid string, stop chan struct{}) {
	defer r.wg.Done()
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

// Stop ends every live indicator and WAITS for its loop to return.
func (r *TypingRefresher) Stop() {
	r.mu.Lock()
	for jid, stop := range r.active {
		close(stop)
		delete(r.active, jid)
	}
	r.mu.Unlock()
	r.wg.Wait()
}
