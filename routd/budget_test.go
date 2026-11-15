package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// seedUser inserts a user_profiles row + per-user cap into routd's OWN routd.db
// (migration 0011) by reusing the store writers — proving store.CreateUserProfile/
// SetUserCap run verbatim against routd.db, the SAME source gated reads.
func seedUser(t *testing.T, db *DB, sub string, capCents int) {
	t.Helper()
	s := store.NewOver(db.Obj())
	if err := s.CreateUserProfile(sub, sub, sub); err != nil {
		t.Fatalf("create auth user: %v", err)
	}
	if err := s.SetUserCap(sub, capCents); err != nil {
		t.Fatalf("set user cap: %v", err)
	}
}

// TestReconcileCaps_HonorsOperatorSetCaps proves the `arizuko budget set` write
// path reaches the gateway. The caps are written through the SAME
// DB.SetFolderCap / DB.SetUserCap the CLI verb calls, and reconcileCaps must
// create the rows at the gateway carrying them (BUGS.md Q1).
func TestReconcileCaps_HonorsOperatorSetCaps(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "team")

	// Exactly what `arizuko budget set folder team --daily 40` does.
	if err := db.SetFolderCap("team", 40); err != nil {
		t.Fatalf("SetFolderCap: %v", err)
	}
	if err := loop.reconcileCaps(context.Background(), "team", ""); err != nil {
		t.Fatal(err)
	}
	if cap, ok := fake.teamCap("team"); !ok || cap != 40 {
		t.Errorf("gateway team cap = %d (exists %v), want the operator's 40", cap, ok)
	}

	// And `arizuko budget set user google:bob --daily 10`.
	_ = db.PutGroup(core.Group{Folder: "wide"})
	seedUser(t, db, "google:bob", 10)
	if err := loop.reconcileCaps(context.Background(), "wide", "google:bob"); err != nil {
		t.Fatal(err)
	}
	if cap, ok := fake.userCap("google:bob"); !ok || cap != 10 {
		t.Errorf("gateway user cap = %d (exists %v), want the operator's 10", cap, ok)
	}
}

// A user cap and a folder cap are both pushed; the gateway holds each on its
// own row and enforces whichever binds first.
func TestReconcileCaps_UserAndFolderBothPushed(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "f")
	if err := db.SetFolderCap("f", 1000); err != nil {
		t.Fatal(err)
	}
	seedUser(t, db, "google:alice", 50)
	fake.setTeam("f", 10, 0)
	fake.setUser("google:alice", 50, 0)

	if err := loop.reconcileCaps(context.Background(), "f", "google:alice"); err != nil {
		t.Fatal(err)
	}
	if cap, _ := fake.teamCap("f"); cap != 1000 {
		t.Errorf("team cap = %d, want 1000", cap)
	}
	if cap, _ := fake.userCap("google:alice"); cap != 50 {
		t.Errorf("user cap = %d, want 50", cap)
	}
}

// A JWT-derived sender activates the per-user branch.
func TestReconcileCaps_JWTSenderActivatesUserCap(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "f")
	if err := db.SetFolderCap("f", 1000); err != nil {
		t.Fatal(err)
	}
	seedUser(t, db, "github:carol", 25)

	if err := loop.reconcileCaps(context.Background(), "f", turnCallerSub("github:carol")); err != nil {
		t.Fatal(err)
	}
	if cap, ok := fake.userCap("github:carol"); !ok || cap != 25 {
		t.Errorf("user cap = %d (exists %v), want 25", cap, ok)
	}
}

// An adapter sender owns no user_profiles row, so it is uncapped and only the
// folder cap is pushed. It is still ATTRIBUTED — the sub reaches the mint
// verbatim (BUGS L69).
func TestReconcileCaps_AdapterSenderFolderOnly(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "f")
	if err := db.SetFolderCap("f", 100); err != nil {
		t.Fatal(err)
	}

	if err := loop.reconcileCaps(context.Background(), "f", turnCallerSub("telegram:user/42")); err != nil {
		t.Fatal(err)
	}
	if cap, _ := fake.teamCap("f"); cap != 100 {
		t.Errorf("team cap = %d, want 100", cap)
	}
	if _, ok := fake.userCap("telegram:user/42"); ok {
		t.Error("a user row was created for a sender with no cap")
	}
}

// The sub that cost attribution actually sees is a platform one, and it owns no
// user_profiles row. That must read as uncapped, not as an error reconcileCaps logs
// on every single turn (BUGS L69).
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

// COST_CAPS_ENABLED off is the operator's escape hatch, and it has to reach the
// thing doing the enforcing. Leaving the gateway's budget standing would keep it
// refusing turns arizuko no longer caps, with no arizuko surface showing why.
func TestReconcileCaps_DisablingCapsClearsTheGatewaysBudget(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	loop.costCapsEnabled = false
	if err := db.SetFolderCap("demo", 500); err != nil {
		t.Fatal(err)
	}
	fake.setTeam("demo", 900, 500)

	if err := loop.reconcileCaps(context.Background(), "demo", "user:a"); err != nil {
		t.Fatal(err)
	}
	if cap, _ := fake.teamCap("demo"); cap != 0 {
		t.Errorf("gateway still enforcing %d cents after caps were disabled", cap)
	}
}

// End-to-end through processGroupMessages: a caller at their user cap is
// spawned, and the cap reaches the gateway as the user's row — the dispatch
// site passes turnCallerSub(last.Sender).
func TestReconcileCaps_UserCapDispatchParity(t *testing.T) {
	db, loop, runner, fake := gatewayLoop(t, "demo")
	if err := db.SetFolderCap("demo", 1000); err != nil {
		t.Fatal(err)
	}
	seedUser(t, db, "google:dave", 30)
	fake.setTeam("demo", 0, 1000)
	fake.setUser("google:dave", 30, 20)
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "web:demo", Sender: "google:dave",
		Content: "expensive question", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if runner.req.TurnID == "" {
		t.Fatal("over-user-cap turn was not spawned; the gateway refuses the model call, routd does not")
	}
	if cap, _ := fake.userCap("google:dave"); cap != 30 {
		t.Errorf("gateway user cap = %d, want the operator's 30", cap)
	}
}

// The real attribution wiring, not the rule in isolation: a turn triggered from
// Telegram must reach the gateway carrying that sender, because the gateway is
// what meters and caps per person now. The fleet once had 7188 cost rows and not
// one carried a user_sub, because an allowlist of authd schemes sat between the
// trigger and the write (BUGS L69). Only an end-to-end assertion catches that.
func TestDispatch_AttributesPlatformSenderToTheGateway(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "krons")
	const sender = "telegram:user/1112184352"
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "web:krons", Sender: sender,
		Content: "hi", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:krons"); err != nil {
		t.Fatalf("process: %v", err)
	}
	minted, _ := fake.snapshot()
	if len(minted) == 0 {
		t.Fatal("no key minted")
	}
	if got := minted[len(minted)-1]["user_id"]; got != sender {
		t.Errorf("key user_id = %v, want %q — the platform sender must reach the meter verbatim", got, sender)
	}
}

// A scheduled task has no person behind it and must attribute to the platform,
// never to the empty bucket every unattributed turn would share.
func TestDispatch_AttributesScheduledTaskToRoutd(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "krons")
	_ = db.PutMessage(core.Message{ID: "b", ChatJID: "web:krons", Sender: "timed",
		Content: "cron", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:krons"); err != nil {
		t.Fatalf("process: %v", err)
	}
	minted, _ := fake.snapshot()
	if len(minted) == 0 {
		t.Fatal("no key minted")
	}
	if got := minted[len(minted)-1]["user_id"]; got != "service:routd" {
		t.Errorf("key user_id = %v, want service:routd", got)
	}
}

// TestReconcileCaps_RemovingAUserCapClearsItAtTheGateway pins BUGS `L402`. The folder
// half has always pushed the zero — `b.CapCents != capCents` is true for 0 too — and
// the user half returned early on `capCents == 0`, so it never read the row and never
// cleared it.
//
// The effect: take a person's cap away and the gateway keeps refusing them, with no cap
// left anywhere in arizuko to explain why. The sibling test above covers the folder.
func TestReconcileCaps_RemovingAUserCapClearsItAtTheGateway(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	if err := db.SetFolderCap("demo", 1000); err != nil {
		t.Fatal(err)
	}
	fake.setTeam("demo", 0, 1000)
	// The person held a cap, spent it, and the operator then took the cap away.
	seedUser(t, db, "google:dave", 0)
	fake.setUser("google:dave", 30, 30)

	if err := loop.reconcileCaps(context.Background(), "demo", "google:dave"); err != nil {
		t.Fatal(err)
	}
	if cap, _ := fake.userCap("google:dave"); cap != 0 {
		t.Errorf("gateway still enforcing %d cents after the cap was removed", cap)
	}
}

// The dual: someone who never held a cap costs no create. A row exists only to carry
// a budget, so an absent row plus an absent cap is the common path and must stay inert.
func TestReconcileCaps_AnUncappedPersonGetsNoGatewayRow(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	if err := db.SetFolderCap("demo", 1000); err != nil {
		t.Fatal(err)
	}
	fake.setTeam("demo", 0, 1000)

	if err := loop.reconcileCaps(context.Background(), "demo", "google:erin"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.userCap("google:erin"); ok {
		t.Error("a row was created for someone who holds no cap")
	}
}
