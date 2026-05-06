package gateway

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/onvos/arizuko/core"
)

type ImpulseCfg struct {
	Threshold int            // total weight required to fire; default 100
	Weights   map[string]int // verb → weight; default 100; 0 = never fire alone
	MaxHold   time.Duration  // max hold time before forced flush; default 5m
}

func ParseImpulseCfg(raw string) ImpulseCfg {
	cfg := defaultImpulseCfg()
	if raw == "" {
		return cfg
	}
	var partial struct {
		Threshold *int            `json:"threshold"`
		Weights   map[string]int  `json:"weights"`
		MaxHold   *int            `json:"max_hold_ms"`
	}
	if err := json.Unmarshal([]byte(raw), &partial); err != nil {
		return cfg
	}
	if partial.Threshold != nil {
		cfg.Threshold = *partial.Threshold
	}
	if partial.Weights != nil {
		for k, v := range partial.Weights {
			cfg.Weights[k] = v
		}
	}
	if partial.MaxHold != nil {
		cfg.MaxHold = time.Duration(*partial.MaxHold) * time.Millisecond
	}
	return cfg
}

func defaultImpulseCfg() ImpulseCfg {
	return ImpulseCfg{
		Threshold: 100,
		Weights: map[string]int{
			"observe": 0,
			"join":    0,
			"edit":    0,
			"delete":  0,
		},
		MaxHold: 5 * time.Minute,
	}
}

type impulseJID struct {
	weight    int
	lastEvent time.Time
}

type impulseGate struct {
	mu    sync.Mutex
	state map[string]*impulseJID
}

func newImpulseGate() *impulseGate {
	return &impulseGate{state: make(map[string]*impulseJID)}
}

func weightFor(cfg ImpulseCfg, verb string) int {
	if w, ok := cfg.Weights[verb]; ok {
		return w
	}
	return 100
}

func (g *impulseGate) accept(jid string, msgs []core.Message, cfg ImpulseCfg) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.state[jid]
	if st == nil {
		st = &impulseJID{}
		g.state[jid] = st
	}

	for _, m := range msgs {
		w := weightFor(cfg, m.Verb)
		st.weight += w
		st.lastEvent = time.Now()
	}

	if st.weight >= cfg.Threshold {
		st.weight = 0
		st.lastEvent = time.Time{}
		return true
	}
	return false
}

func (g *impulseGate) flush(cfgFor func(jid string) ImpulseCfg) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	var jids []string
	for jid, st := range g.state {
		if st.weight <= 0 || st.lastEvent.IsZero() {
			continue
		}
		cfg := cfgFor(jid)
		if st.lastEvent.Before(now.Add(-cfg.MaxHold)) {
			st.weight = 0
			st.lastEvent = time.Time{}
			jids = append(jids, jid)
		}
	}
	return jids
}
