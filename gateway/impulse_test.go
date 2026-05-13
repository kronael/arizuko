package gateway

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestParseImpulseCfg(t *testing.T) {
	// want=-1 means "don't check this field"
	cases := []struct {
		name       string
		json       string
		threshold  int
		maxHold    time.Duration
		joinWeight int // -1 = skip
		extra      func(*testing.T, ImpulseCfg)
	}{
		{"empty", "", 100, 5 * time.Minute, 0, nil},
		{"invalid json", "{bad json", 100, 5 * time.Minute, -1, nil},
		{"override threshold", `{"threshold": 50}`, 50, 5 * time.Minute, 0, nil},
		{"override max_hold", `{"max_hold_ms": 60000}`, 100, time.Minute, -1, nil},
		{"merge weights", `{"weights": {"join": 50, "custom": 10}}`, 100, 5 * time.Minute, 50,
			func(t *testing.T, c ImpulseCfg) {
				if c.Weights["custom"] != 10 {
					t.Errorf("custom = %d, want 10", c.Weights["custom"])
				}
				if c.Weights["edit"] != 0 || c.Weights["delete"] != 0 {
					t.Errorf("defaults not preserved: %v", c.Weights)
				}
			}},
		{"all fields", `{"threshold": 200, "max_hold_ms": 120000, "weights": {"join": 10}}`, 200, 2 * time.Minute, 10, nil},
		{"zero threshold", `{"threshold": 0}`, 0, 5 * time.Minute, -1, nil},
	}
	for _, c := range cases {
		cfg := ParseImpulseCfg(c.json)
		if cfg.Threshold != c.threshold {
			t.Errorf("%s: threshold = %d, want %d", c.name, cfg.Threshold, c.threshold)
		}
		if cfg.MaxHold != c.maxHold {
			t.Errorf("%s: max_hold = %v, want %v", c.name, cfg.MaxHold, c.maxHold)
		}
		if c.joinWeight >= 0 && cfg.Weights["join"] != c.joinWeight {
			t.Errorf("%s: join weight = %d, want %d", c.name, cfg.Weights["join"], c.joinWeight)
		}
		if c.extra != nil {
			c.extra(t, cfg)
		}
	}
}

func TestWeightFor_Known(t *testing.T) {
	cfg := defaultImpulseCfg()
	if w := weightFor(cfg, "join"); w != 0 {
		t.Errorf("join = %d, want 0", w)
	}
	if w := weightFor(cfg, "edit"); w != 0 {
		t.Errorf("edit = %d, want 0", w)
	}
}

func TestWeightFor_Unknown(t *testing.T) {
	cfg := defaultImpulseCfg()
	if w := weightFor(cfg, "message"); w != 100 {
		t.Errorf("unknown verb = %d, want 100", w)
	}
	if w := weightFor(cfg, "anything"); w != 100 {
		t.Errorf("unknown verb = %d, want 100", w)
	}
}

func TestImpulseGate_AcceptFiresAtThreshold(t *testing.T) {
	g := newImpulseGate()
	cfg := defaultImpulseCfg()

	msgs := []core.Message{{Verb: "message"}}
	if !g.accept("jid1", msgs, cfg) {
		t.Error("should fire: single message has weight 100 = threshold")
	}
}

func TestImpulseGate_AcceptZeroWeight(t *testing.T) {
	g := newImpulseGate()
	cfg := defaultImpulseCfg()

	msgs := []core.Message{{Verb: "join"}}
	if g.accept("jid1", msgs, cfg) {
		t.Error("should not fire: join has weight 0")
	}
}

func TestImpulseGate_AcceptAccumulates(t *testing.T) {
	g := newImpulseGate()
	cfg := ImpulseCfg{
		Threshold: 200,
		Weights:   map[string]int{"ping": 50},
		MaxHold:   5 * time.Minute,
	}

	msgs := []core.Message{{Verb: "ping"}}
	if g.accept("jid1", msgs, cfg) {
		t.Error("should not fire: 50 < 200")
	}
	if g.accept("jid1", msgs, cfg) {
		t.Error("should not fire: 100 < 200")
	}
	if g.accept("jid1", msgs, cfg) {
		t.Error("should not fire: 150 < 200")
	}
	if !g.accept("jid1", msgs, cfg) {
		t.Error("should fire: 200 >= 200")
	}

	// After fire, weight resets
	if g.accept("jid1", msgs, cfg) {
		t.Error("should not fire after reset: 50 < 200")
	}
}

func TestImpulseGate_FlushExpired(t *testing.T) {
	g := newImpulseGate()
	cfg := ImpulseCfg{
		Threshold: 200,
		Weights:   map[string]int{},
		MaxHold:   time.Millisecond,
	}

	msgs := []core.Message{{Verb: "message"}}
	g.accept("jid1", msgs, cfg)

	// Manually set lastEvent to past
	g.mu.Lock()
	g.state["jid1"].lastEvent = time.Now().Add(-time.Second)
	g.mu.Unlock()

	flushed := g.flush(func(jid string) ImpulseCfg { return cfg })
	if len(flushed) != 1 || flushed[0] != "jid1" {
		t.Errorf("flush = %v, want [jid1]", flushed)
	}

	// After flush, should not flush again
	flushed = g.flush(func(jid string) ImpulseCfg { return cfg })
	if len(flushed) != 0 {
		t.Errorf("second flush = %v, want empty", flushed)
	}
}
