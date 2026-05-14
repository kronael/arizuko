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
		"claude-opus-4-7":           {Input: 1000, Output: 200, CostCents: 5},
		"claude-haiku-4-5-20251001": {Input: 500, Output: 50, CostCents: 1},
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

func TestCallerSubOfMsg(t *testing.T) {
	cases := []struct{ in, want string }{
		// JWT-derived auth subs → recognised user_sub.
		{"google:alice", "google:alice"},
		{"github:bob", "github:bob"},
		{"local:admin", "local:admin"},
		// Anon slink and adapter senders → unbound (folder cap only).
		{"anon:dead", ""},
		{"telegram:user/42", ""},
		{"slack:user/U123", ""},
		{"discord:user/9", ""},
		{"bluesky:user/did%3Aplc%3Ax", ""},
		{"email:address/a@b.c", ""},
		// System/internal senders.
		{"", ""},
		{"system", ""},
		{"delegate", ""},
		{"timed-isolated:task-1", ""},
	}
	for _, c := range cases {
		if got := callerSubOfMsg(c.in); got != c.want {
			t.Errorf("callerSubOfMsg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Folder cap binds when the sender is adapter-derived (per-user cap is
// inactive for adapter senders today — auth_users only holds JWT subs).
func TestBudgetGate_AdapterSenderFolderOnly(t *testing.T) {
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
	// Adapter sender resolves to "" via callerSubOfMsg → only folder cap
	// binds; we exhausted it, so refusal fires.
	msg := gw.budgetGate("f", callerSubOfMsg("telegram:user/42"))
	if msg == "" || !strings.Contains(msg, "channel spent 100 of 100") {
		t.Errorf("expected channel-scope refusal, got %q", msg)
	}
}

// JWT-derived sender activates the per-user branch.
func TestBudgetGate_JWTSenderActivatesUserCap(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.CostCapsEnabled = true

	if err := s.PutGroup(core.Group{Folder: "f", Name: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderCap("f", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthUser("github:carol", "carol", "x", "Carol"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserCap("github:carol", 25); err != nil {
		t.Fatal(err)
	}
	if err := s.LogCost(store.CostRow{
		Folder: "f", UserSub: "github:carol", Model: "m", Cents: 25,
	}); err != nil {
		t.Fatal(err)
	}
	msg := gw.budgetGate("f", callerSubOfMsg("github:carol"))
	if msg == "" || !strings.Contains(msg, "you spent 25 of 25") {
		t.Errorf("expected user-scope refusal, got %q", msg)
	}
}
