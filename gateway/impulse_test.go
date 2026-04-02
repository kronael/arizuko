package gateway

import (
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
)

func TestParseImpulseCfg_Empty(t *testing.T) {
	cfg := ParseImpulseCfg("")
	if cfg.Threshold != 100 {
		t.Errorf("threshold = %d, want 100", cfg.Threshold)
	}
	if cfg.MaxHold != 5*time.Minute {
		t.Errorf("max_hold = %v, want 5m", cfg.MaxHold)
	}
	if cfg.Weights["join"] != 0 {
		t.Errorf("join weight = %d, want 0", cfg.Weights["join"])
	}
}

func TestParseImpulseCfg_InvalidJSON(t *testing.T) {
	cfg := ParseImpulseCfg("{bad json")
	if cfg.Threshold != 100 {
		t.Errorf("threshold = %d, want 100 on bad json", cfg.Threshold)
	}
}

func TestParseImpulseCfg_OverrideThreshold(t *testing.T) {
	cfg := ParseImpulseCfg(`{"threshold": 50}`)
	if cfg.Threshold != 50 {
		t.Errorf("threshold = %d, want 50", cfg.Threshold)
	}
	// defaults preserved
	if cfg.MaxHold != 5*time.Minute {
		t.Errorf("max_hold = %v, want 5m", cfg.MaxHold)
	}
	if cfg.Weights["join"] != 0 {
		t.Errorf("join weight should still be 0")
	}
}

func TestParseImpulseCfg_OverrideMaxHold(t *testing.T) {
	cfg := ParseImpulseCfg(`{"max_hold_ms": 60000}`)
	if cfg.MaxHold != time.Minute {
		t.Errorf("max_hold = %v, want 1m", cfg.MaxHold)
	}
	if cfg.Threshold != 100 {
		t.Errorf("threshold = %d, want 100", cfg.Threshold)
	}
}

func TestParseImpulseCfg_MergeWeights(t *testing.T) {
	cfg := ParseImpulseCfg(`{"weights": {"join": 50, "custom": 10}}`)
	if cfg.Weights["join"] != 50 {
		t.Errorf("join = %d, want 50", cfg.Weights["join"])
	}
	if cfg.Weights["custom"] != 10 {
		t.Errorf("custom = %d, want 10", cfg.Weights["custom"])
	}
	// unmentioned defaults preserved
	if cfg.Weights["edit"] != 0 {
		t.Errorf("edit = %d, want 0 (default preserved)", cfg.Weights["edit"])
	}
	if cfg.Weights["delete"] != 0 {
		t.Errorf("delete = %d, want 0 (default preserved)", cfg.Weights["delete"])
	}
}

func TestParseImpulseCfg_AllFields(t *testing.T) {
	cfg := ParseImpulseCfg(`{"threshold": 200, "max_hold_ms": 120000, "weights": {"join": 10}}`)
	if cfg.Threshold != 200 {
		t.Errorf("threshold = %d, want 200", cfg.Threshold)
	}
	if cfg.MaxHold != 2*time.Minute {
		t.Errorf("max_hold = %v, want 2m", cfg.MaxHold)
	}
	if cfg.Weights["join"] != 10 {
		t.Errorf("join = %d, want 10", cfg.Weights["join"])
	}
}

func TestParseImpulseCfg_ZeroThreshold(t *testing.T) {
	cfg := ParseImpulseCfg(`{"threshold": 0}`)
	if cfg.Threshold != 0 {
		t.Errorf("threshold = %d, want 0", cfg.Threshold)
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
