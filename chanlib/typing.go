package chanlib

import (
	"sync"
	"time"
)

// DefaultTypingMaxTTL is the backstop that stops typing when the normal
// terminator — routd's Typing(off) at turn end (turns.go, dispatch.go) — never
// arrives.
//
// It is derived from the run wall, not fixed, and it MUST land strictly below
// it. At a flat 20m it equalled the RUNED_RUN_TIMEOUT default, so it fired no
// earlier than the container's own SIGKILL: a stalled turn showed "typing" for
// the full twenty minutes and then went silent with no reply, which is what a
// user reported as "the typing indication again" (BUGS T1). A backstop that
// only fires when the thing it guards is already dead is not a backstop.
//
// 90% of the wall. That still cuts a healthy turn's indicator ~2m short of the
// agent's own query timeout (the wall minus 30s), which is the deliberate
// trade: an indicator that stops early is honest about not knowing, one that
// runs to the wall is a twenty-minute lie.
//
// RUNED_RUN_TIMEOUT is read here from the adapter's own env — the same var and
// the same 20m default runed and routd use — so an instance that widens the
// wall widens the backstop with it.
var DefaultTypingMaxTTL = typingMaxTTL(EnvDur("RUNED_RUN_TIMEOUT", 20*time.Minute))

// minTypingMaxTTL floors the derivation so a tiny or absurd RUNED_RUN_TIMEOUT
// cannot produce a backstop that kills the indicator before the first refresh.
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
	// wg counts the live loops. Stop WAITS on it: closing the stop channel only
	// asks, and a loop mid-send keeps sending after Stop returns — an outbound
	// call that outlives the shutdown that was supposed to end it (BUGS L168).
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

// Stop ends every live indicator and WAITS for its loop to return. The wait is
// the point: closing the stop channel only asks, and a loop already inside send
// keeps calling the platform after Stop returns.
func (r *TypingRefresher) Stop() {
	r.mu.Lock()
	for jid, stop := range r.active {
		close(stop)
		delete(r.active, jid)
	}
	r.mu.Unlock()
	r.wg.Wait()
}
