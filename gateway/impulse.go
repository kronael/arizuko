package gateway

import (
	"sync"
	"time"

	"github.com/onvos/arizuko/core"
)

type ImpulseCfg struct {
	Threshold int            // total weight required to fire; default 100
	Weights   map[string]int // verb → weight; default 100; 0 = never fire alone
	MaxHold   time.Duration  // max hold time before forced flush; default 5m
}

func defaultImpulseCfg() ImpulseCfg {
	return ImpulseCfg{
		Threshold: 100,
		Weights: map[string]int{
			"join":   0,
			"edit":   0,
			"delete": 0,
		},
		MaxHold: 5 * time.Minute,
	}
}

type impulseJID struct {
	weight    int
	lastEvent time.Time
}

type impulseGate struct {
	cfg   ImpulseCfg
	mu    sync.Mutex
	state map[string]*impulseJID
}

func newImpulseGate(cfg ImpulseCfg) *impulseGate {
	return &impulseGate{cfg: cfg, state: make(map[string]*impulseJID)}
}

func (g *impulseGate) weightFor(verb string) int {
	if w, ok := g.cfg.Weights[verb]; ok {
		return w
	}
	return 100
}

// accept adds messages for a JID and returns true if the agent should fire now.
func (g *impulseGate) accept(jid string, msgs []core.Message) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.state[jid]
	if st == nil {
		st = &impulseJID{}
		g.state[jid] = st
	}

	for _, m := range msgs {
		w := g.weightFor(m.Verb)
		st.weight += w
		st.lastEvent = time.Now()
	}

	if st.weight >= g.cfg.Threshold {
		st.weight = 0
		st.lastEvent = time.Time{}
		return true
	}
	return false
}

// flush returns JIDs whose pending weight has exceeded max_hold and resets them.
func (g *impulseGate) flush() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	cutoff := time.Now().Add(-g.cfg.MaxHold)
	var jids []string
	for jid, st := range g.state {
		if st.weight > 0 && !st.lastEvent.IsZero() && st.lastEvent.Before(cutoff) {
			st.weight = 0
			st.lastEvent = time.Time{}
			jids = append(jids, jid)
		}
	}
	return jids
}
