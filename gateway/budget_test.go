package gateway

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

func TestBudgetGate_DisabledLetsThrough(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.CostCapsEnabled = false

	if err := s.PutGroup(core.Group{Folder: "f", Name: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderCap("f", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCost(store.CostRow{Folder: "f", Model: "m", Cents: 200}); err != nil {
		t.Fatal(err)
	}
	if msg := gw.budgetGate("f", ""); msg != "" {
		t.Errorf("disabled gate refused: %q", msg)
	}
}

func TestBudgetGate_NoCapLetsThrough(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.CostCapsEnabled = true

	if err := s.PutGroup(core.Group{Folder: "f", Name: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCost(store.CostRow{Folder: "f", Model: "m", Cents: 999}); err != nil {
		t.Fatal(err)
	}
	if msg := gw.budgetGate("f", ""); msg != "" {
		t.Errorf("uncapped gate refused: %q", msg)
	}
}

func TestBudgetGate_UnderCapLetsThrough(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.CostCapsEnabled = true

	if err := s.PutGroup(core.Group{Folder: "f", Name: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderCap("f", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCost(store.CostRow{Folder: "f", Model: "m", Cents: 50}); err != nil {
		t.Fatal(err)
	}
	if msg := gw.budgetGate("f", ""); msg != "" {
		t.Errorf("under-cap gate refused: %q", msg)
	}
}

func TestBudgetGate_OverCapRefuses(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.CostCapsEnabled = true

	if err := s.PutGroup(core.Group{Folder: "f", Name: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderCap("f", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCost(store.CostRow{Folder: "f", Model: "m", Cents: 100}); err != nil {
		t.Fatal(err)
	}
	msg := gw.budgetGate("f", "")
	if msg == "" {
		t.Fatal("exhausted gate let through")
	}
	for _, want := range []string{"Budget reached", "100"} {
		if !strings.Contains(msg, want) {
			t.Errorf("msg missing %q: %s", want, msg)
		}
	}
}

func TestBudgetGate_UserCapBindsLower(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.CostCapsEnabled = true

	if err := s.PutGroup(core.Group{Folder: "f", Name: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderCap("f", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthUser("google:alice", "alice", "x", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserCap("google:alice", 50); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCost(store.CostRow{
		Folder: "f", UserSub: "google:alice", Model: "m", Cents: 50,
	}); err != nil {
		t.Fatal(err)
	}
	msg := gw.budgetGate("f", "google:alice")
	if msg == "" || !strings.Contains(msg, "you spent 50 of 50") {
		t.Errorf("expected user-scope refusal, got %q", msg)
	}
}

func TestRecordTurnCost_WritesPerModelRow(t *testing.T) {
	gw, s := testGateway(t)
	gw.recordTurnCost("team", "google:bob", map[string]ipc.ModelUsage{
		"claude-opus-4-7": {InputTokens: 1000, OutputTokens: 200, CostCents: 5},
		"claude-haiku-4-5-20251001": {InputTokens: 500, OutputTokens: 50, CostCents: 1},
	})
	total, err := s.SpendTodayFolder("team")
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 {
		t.Errorf("folder spend = %d, want 6", total)
	}
	userTotal, err := s.SpendTodayUser("google:bob")
	if err != nil {
		t.Fatal(err)
	}
	if userTotal != 6 {
		t.Errorf("user spend = %d, want 6", userTotal)
	}
}

func TestRecordTurnCost_EmptyModelsIsNoop(t *testing.T) {
	gw, s := testGateway(t)
	gw.recordTurnCost("team", "", nil)
	got, _ := s.SpendTodayFolder("team")
	if got != 0 {
		t.Errorf("got %d, want 0 for empty models", got)
	}
}
