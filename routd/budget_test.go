package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// seedUser inserts an auth_users row + per-user cap into routd's OWN routd.db
// (migration 0011) by reusing the store writers — proving store.CreateAuthUser/
// SetUserCap run verbatim against routd.db, the SAME source gated reads.
func seedUser(t *testing.T, db *DB, sub string, capCents int) {
	t.Helper()
	s := store.New(db.SQL())
	if err := s.CreateAuthUser(sub, sub, "x", sub); err != nil {
		t.Fatalf("create auth user: %v", err)
	}
	if err := s.SetUserCap(sub, capCents); err != nil {
		t.Fatalf("set user cap: %v", err)
	}
}

// TestBudgetGate_UserCapBindsLower: a user cap below the folder cap binds first.
// Mirrors gateway_test.TestBudgetGate_UserCapBindsLower.
func TestBudgetGate_UserCapBindsLower(t *testing.T) {
	db, loop, _ := recLoop(t)
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "f"})
	if _, err := db.SQL().Exec("UPDATE groups SET cost_cap_cents_per_day=1000 WHERE folder='f'"); err != nil {
		t.Fatal(err)
	}
	seedUser(t, db, "google:alice", 50)
	if err := db.PutCost("f", "t-prior", "google:alice", "m", 0, 0, 50); err != nil {
		t.Fatal(err)
	}
	msg := loop.budgetGate("f", "google:alice")
	if msg == "" || !strings.Contains(msg, "you spent 50 of 50") {
		t.Errorf("expected user-scope refusal, got %q", msg)
	}
}

// TestBudgetGate_JWTSenderActivatesUserCap: a JWT-derived sender activates the
// per-user branch. Mirrors gateway_test.TestBudgetGate_JWTSenderActivatesUserCap.
func TestBudgetGate_JWTSenderActivatesUserCap(t *testing.T) {
	db, loop, _ := recLoop(t)
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "f"})
	if _, err := db.SQL().Exec("UPDATE groups SET cost_cap_cents_per_day=1000 WHERE folder='f'"); err != nil {
		t.Fatal(err)
	}
	seedUser(t, db, "github:carol", 25)
	if err := db.PutCost("f", "t-prior", "github:carol", "m", 0, 0, 25); err != nil {
		t.Fatal(err)
	}
	msg := loop.budgetGate("f", callerSubOfMsg("github:carol"))
	if msg == "" || !strings.Contains(msg, "you spent 25 of 25") {
		t.Errorf("expected user-scope refusal, got %q", msg)
	}
}

// TestBudgetGate_AdapterSenderFolderOnly: an adapter sender resolves to "" via
// callerSubOfMsg, so only the folder cap binds. Mirrors
// gateway_test.TestBudgetGate_AdapterSenderFolderOnly.
func TestBudgetGate_AdapterSenderFolderOnly(t *testing.T) {
	db, loop, _ := recLoop(t)
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "f"})
	if _, err := db.SQL().Exec("UPDATE groups SET cost_cap_cents_per_day=100 WHERE folder='f'"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutCost("f", "t-prior", "", "m", 0, 0, 100); err != nil {
		t.Fatal(err)
	}
	msg := loop.budgetGate("f", callerSubOfMsg("telegram:user/42"))
	if msg == "" || !strings.Contains(msg, "channel spent 100 of 100") {
		t.Errorf("expected channel-scope refusal, got %q", msg)
	}
}

// TestCallerSubOfMsg: JWT subs classify; adapter/anon/system senders are unbound.
// Mirrors gateway_test.TestCallerSubOfMsg.
func TestCallerSubOfMsg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"google:alice", "google:alice"},
		{"github:bob", "github:bob"},
		{"local:admin", "local:admin"},
		{"anon:dead", ""},
		{"telegram:user/42", ""},
		{"slack:user/U123", ""},
		{"discord:user/9", ""},
		{"bluesky:user/did%3Aplc%3Ax", ""},
		{"email:address/a@b.c", ""},
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

// TestRecordTurnCost_WritesUserSubRow: a turn whose trigger is a JWT sender logs
// a cost_log row carrying that user_sub, so SpendTodayUser aggregates it.
// Mirrors gateway_test.TestRecordTurnCost_WritesPerModelRow's per-user half.
func TestRecordTurnCost_WritesUserSubRow(t *testing.T) {
	db, _, _ := recLoop(t)
	if err := db.PutCost("team", "t1", "google:bob", "claude-opus-4-7", 1000, 200, 5); err != nil {
		t.Fatal(err)
	}
	if err := db.PutCost("team", "t1", "google:bob", "claude-haiku-4-5", 500, 50, 1); err != nil {
		t.Fatal(err)
	}
	folderTotal, err := db.SpendTodayFolder("team")
	if err != nil {
		t.Fatal(err)
	}
	if folderTotal != 6 {
		t.Errorf("folder spend = %d, want 6", folderTotal)
	}
	userTotal, err := db.SpendTodayUser("google:bob")
	if err != nil {
		t.Fatal(err)
	}
	if userTotal != 6 {
		t.Errorf("user spend = %d, want 6", userTotal)
	}
}

// TestBudgetGate_UserCapDispatchParity: end-to-end through processGroupMessages,
// a JWT sender over its user cap is refused pre-spawn (no run), even when the
// folder cap is generous — the dispatch site passes callerSubOfMsg(last.Sender).
func TestBudgetGate_UserCapDispatchParity(t *testing.T) {
	db, loop, rr := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if _, err := db.SQL().Exec("UPDATE groups SET cost_cap_cents_per_day=1000 WHERE folder='demo'"); err != nil {
		t.Fatal(err)
	}
	seedUser(t, db, "google:dave", 30)
	if err := db.PutCost("demo", "t-prior", "google:dave", "claude", 0, 0, 30); err != nil {
		t.Fatal(err)
	}
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "google:dave",
		Content: "expensive question", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("over-user-cap turn dispatched a run: %+v", rr.runs)
	}
	want := "Budget reached for today (you spent 30 of 30 cents). Resumes at 00:00 UTC."
	if got := lastAck(t, dl); got != want {
		t.Fatalf("refusal=%q want %q", got, want)
	}
}
