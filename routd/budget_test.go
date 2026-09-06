package routd

import (
	"strings"
	"testing"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// seedUser inserts a user_profiles row + per-user cap into routd's OWN routd.db
// (migration 0011) by reusing the store writers — proving store.CreateAuthUser/
// SetUserCap run verbatim against routd.db, the SAME source gated reads.
func seedUser(t *testing.T, db *DB, sub string, capCents int) {
	t.Helper()
	s := store.New(db.SQL())
	if err := s.CreateAuthUser(sub, sub, sub); err != nil {
		t.Fatalf("create auth user: %v", err)
	}
	if err := s.SetUserCap(sub, capCents); err != nil {
		t.Fatalf("set user cap: %v", err)
	}
}

// TestBudgetGate_HonorsOperatorSetCaps proves the `arizuko budget set` write
// path reaches enforcement: the caps are written through the SAME DB.SetFolderCap
// / DB.SetUserCap the CLI verb calls, and budgetGate must then refuse the turn.
// The verb used to write these caps into the frozen messages.db, so an operator
// capped a folder and nothing changed (BUGS.md Q1).
func TestBudgetGate_HonorsOperatorSetCaps(t *testing.T) {
	db, loop, _ := recLoop(t)
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "team"})
	s := store.New(db.SQL())
	if err := s.CreateAuthUser("google:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Exactly what `arizuko budget set folder team --daily 40` does.
	if err := db.SetFolderCap("team", 40); err != nil {
		t.Fatalf("SetFolderCap: %v", err)
	}
	if err := db.PutCost("team", "t-prior", "", "m", 0, 0, 40); err != nil {
		t.Fatal(err)
	}
	if msg, _ := loop.budgetGate("team", ""); !strings.Contains(msg, "channel spent 40 of 40") {
		t.Errorf("operator folder cap not enforced: budgetGate = %q", msg)
	}

	// And `arizuko budget set user google:bob --daily 10`.
	_ = db.PutGroup(core.Group{Folder: "wide"})
	if err := db.SetUserCap("google:bob", 10); err != nil {
		t.Fatalf("SetUserCap: %v", err)
	}
	if err := db.PutCost("wide", "t-prior2", "google:bob", "m", 0, 0, 10); err != nil {
		t.Fatal(err)
	}
	if msg, _ := loop.budgetGate("wide", "google:bob"); !strings.Contains(msg, "you spent 10 of 10") {
		t.Errorf("operator user cap not enforced: budgetGate = %q", msg)
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
	msg, _ := loop.budgetGate("f", "google:alice")
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
	msg, _ := loop.budgetGate("f", turnCallerSub("github:carol"))
	if msg == "" || !strings.Contains(msg, "you spent 25 of 25") {
		t.Errorf("expected user-scope refusal, got %q", msg)
	}
}

// TestBudgetGate_AdapterSenderFolderOnly: an adapter sender owns no
// user_profiles row, so it is uncapped and only the folder cap binds. It is
// still ATTRIBUTED — the sub reaches the gate verbatim (BUGS L69).
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
	msg, _ := loop.budgetGate("f", turnCallerSub("telegram:user/42"))
	if msg == "" || !strings.Contains(msg, "channel spent 100 of 100") {
		t.Errorf("expected channel-scope refusal, got %q", msg)
	}
}

// TestUserCap_NoProfileRowIsUncapped: the sub that cost attribution actually
// sees is a platform one, and it owns no user_profiles row. That must read as
// uncapped, not as an error the gate logs on every single turn (BUGS L69).
func TestUserCap_NoProfileRowIsUncapped(t *testing.T) {
	db, _, _ := recLoop(t)
	cap, err := db.UserCap("telegram:user/42")
	if err != nil {
		t.Fatalf("UserCap of a sub with no profile row: %v", err)
	}
	if cap != 0 {
		t.Errorf("cap = %d, want 0 (uncapped)", cap)
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
// folder cap is generous — the dispatch site passes turnCallerSub(last.Sender).
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

// TestRecordTurnResult_AttributesPlatformSender drives the REAL cost-attribution
// wiring, not the rule in isolation: a turn triggered from Telegram must leave a
// cost_log row carrying that sender. The fleet had 7188 rows and not one carried
// a user_sub, because an allowlist of authd schemes sat between the trigger and
// the write (BUGS L69). Only an end-to-end assertion catches that — the rule's
// own table test passed the whole time.
func TestRecordTurnResult_AttributesPlatformSender(t *testing.T) {
	s := testServer(t)
	const turnID = "turn-attrib-1"
	const sender = "telegram:user/1112184352"
	if _, err := s.db.PutTurnContext(turnID, "krons", "", "tg:1", sender, ""); err != nil {
		t.Fatalf("put turn context: %v", err)
	}

	if _, err := s.recordTurnResult(turnID, apiv1.TurnResult{
		Status: "success",
		Models: map[string]apiv1.ModelCost{
			"claude-opus-5": {Input: 1000, Output: 200, CostCents: 7},
		},
	}); err != nil {
		t.Fatalf("record turn result: %v", err)
	}

	var got string
	if err := s.db.SQL().QueryRow(
		"SELECT user_sub FROM cost_log WHERE turn_id=?", turnID).Scan(&got); err != nil {
		t.Fatalf("read cost row: %v", err)
	}
	if got != sender {
		t.Errorf("cost_log.user_sub = %q, want %q", got, sender)
	}
	spent, err := s.db.SpendTodayUser(sender)
	if err != nil {
		t.Fatal(err)
	}
	if spent != 7 {
		t.Errorf("SpendTodayUser(%q) = %d, want 7", sender, spent)
	}
}

// A scheduled task has no person behind it and must attribute to the platform,
// never to the empty bucket every unattributed row shares.
func TestRecordTurnResult_AttributesScheduledTaskToRoutd(t *testing.T) {
	s := testServer(t)
	const turnID = "turn-attrib-2"
	if _, err := s.db.PutTurnContext(turnID, "krons", "", "tg:1", "timed", ""); err != nil {
		t.Fatalf("put turn context: %v", err)
	}
	if _, err := s.recordTurnResult(turnID, apiv1.TurnResult{
		Status: "success",
		Models: map[string]apiv1.ModelCost{"claude-opus-5": {CostCents: 3}},
	}); err != nil {
		t.Fatalf("record turn result: %v", err)
	}
	var got string
	if err := s.db.SQL().QueryRow(
		"SELECT user_sub FROM cost_log WHERE turn_id=?", turnID).Scan(&got); err != nil {
		t.Fatalf("read cost row: %v", err)
	}
	if got != "service:routd" {
		t.Errorf("cost_log.user_sub = %q, want service:routd", got)
	}
}
