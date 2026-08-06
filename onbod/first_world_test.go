package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Spec 5/18 step 8. A chat-onboarded stranger who administers no world may
// create a top-level one, on TWO conditions that must hold together: the
// admission queue approved them, AND an operator configured at least one
// enabled gate.
//
// The conjunction is the decision, not an implementation detail. admitJID
// approves EVERY paired identity when no gate is enabled and `arizuko create`
// seeds none, so `approved` on its own would flip every existing deployment
// from invite-only world creation to open signup. Each half is therefore
// tested refusing on its own — those two are the cases that keep the posture.

// seedStranger creates the caller's profile and an onboarding row in the given
// status. Every seed is error-checked and read back: a fixture whose INSERT
// silently failed (a missing NOT NULL column, a typo'd table) would otherwise
// make the refusal tests below pass for the wrong reason — they would be
// asserting a refusal caused by an absent row rather than by the gate rule.
func seedStranger(t *testing.T, db *sql.DB, sub, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES (?, ?, 'Stranger', '2026-01-01')`, sub, sub); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO onboarding (jid, status, user_sub, created)
		VALUES ('telegram:group/1', ?, ?, '2026-01-01')`, status, sub); err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}
	var got string
	if err := db.QueryRow(
		`SELECT status FROM onboarding WHERE user_sub = ?`, sub).Scan(&got); err != nil {
		t.Fatalf("onboarding seed did not land: %v", err)
	}
	if got != status {
		t.Fatalf("onboarding seed landed as %q, want %q", got, status)
	}
}

// seedGate configures a gate and reads it back through loadGates — the SAME
// reader the production path uses. Asserting on loadGates rather than on a
// COUNT means a gate this helper "configured" but which loadGates cannot see
// (wrong enabled value, wrong column) fails the seed instead of the assertion.
func seedGate(t *testing.T, db *sql.DB, gate string, enabled int) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO onboarding_gates (gate, limit_per_day, enabled) VALUES (?, 10, ?)`,
		gate, enabled); err != nil {
		t.Fatalf("seed gate: %v", err)
	}
	visible := len(loadGates(db)) > 0
	if visible != (enabled == 1) {
		t.Fatalf("gate %q enabled=%d is %v to loadGates; seed does not model the case",
			gate, enabled, visible)
	}
}

// createWorld posts the create_world form with NO pending_target cookie, which
// is the whole point: the cookie is the invite's authority, and this exercises
// the approved-admission authority that must stand on its own.
func createWorld(t *testing.T, db *sql.DB, sub, username string) *httptest.ResponseRecorder {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	return postOnboard(db, config{}, sub,
		url.Values{"action": {"create_world"}, "username": {username}})
}

func folderExists(t *testing.T, db *sql.DB, folder string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = ?`, folder).Scan(&n); err != nil {
		t.Fatalf("groups lookup: %v", err)
	}
	return n > 0
}

func TestApprovedStrangerWithGateSeesPicker(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")
	seedGate(t, db, "github:*", 1)

	w := getOnboard(db, "github:new")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "create_world") {
		t.Errorf("an approved stranger under a configured gate got no username picker; body: %s",
			w.Body.String())
	}
}

// The write is the control. A picker that renders is only a convenience; this
// posts without ever loading the page, which is what a caller guessing the URL
// does.
func TestApprovedStrangerWithGateCreatesTopLevelWorld(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")
	seedGate(t, db, "github:*", 1)

	w := createWorld(t, db, "github:new", "newworld")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", w.Code, w.Body.String())
	}
	if !folderExists(t, db, "newworld") {
		t.Fatal("approved stranger got 303 but no world was created")
	}

	// Containment: the approved admission authorizes a TOP-LEVEL folder. Assert
	// on every folder that exists, not just on the expected name — a folder
	// created at "victim/newworld" would satisfy a lookup for the suffix.
	rows, err := db.Query(`SELECT folder FROM groups`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(f, "/") {
			t.Errorf("approved admission created a NESTED folder %q; it authorizes top-level only", f)
		}
	}

	var scope string
	if err := db.QueryRow(
		`SELECT scope FROM acl WHERE principal = ? AND action = 'admin'`,
		"github:new").Scan(&scope); err != nil {
		t.Fatalf("no admin grant for the created world: %v", err)
	}
	if scope != "newworld/**" {
		t.Errorf("admin scope = %q, want %q", scope, "newworld/**")
	}
}

// Adversarial half 1: approved, but the operator configured no gate. This is
// the default posture of every existing deployment — admitJID approved this
// caller only because there was nothing to match against — so the write MUST
// refuse. Deleting the loadGates condition from mayCreateFirstWorld turns this
// red and nothing else.
func TestApprovedStrangerWithoutAnyGateIsRefusedAtTheWrite(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")

	w := createWorld(t, db, "github:new", "newworld")

	if w.Code == http.StatusSeeOther {
		t.Fatal("approved with NO gate configured created a world: open signup by default")
	}
	if folderExists(t, db, "newworld") {
		t.Fatal("no 303, but the world exists anyway")
	}
	if !strings.Contains(w.Body.String(), "invite") &&
		!strings.Contains(w.Body.String(), "Invite") {
		t.Errorf("refusal is not the honest invite page; body: %s", w.Body.String())
	}
}

// Adversarial half 2: a gate is configured, but this caller was not approved —
// they are still queued. Deleting the status='approved' condition turns this
// red and nothing else.
func TestUnapprovedStrangerUnderAGateIsRefusedAtTheWrite(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "queued")
	seedGate(t, db, "github:*", 1)

	w := createWorld(t, db, "github:new", "newworld")

	if w.Code == http.StatusSeeOther {
		t.Fatal("a queued (unapproved) caller created a world")
	}
	if folderExists(t, db, "newworld") {
		t.Fatal("no 303, but the world exists anyway")
	}
}

// A DISABLED gate is not a configured gate: admitJID reads gates through
// loadGates, which filters enabled=1, so with only this row it still approves
// everyone. Checking `COUNT(*) FROM onboarding_gates` in mayCreateFirstWorld
// instead of calling loadGates would unlock the picker here while admission
// stayed indiscriminate — the exact drift this shares one reader to prevent.
func TestDisabledGateDoesNotUnlockWorldCreation(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")
	seedGate(t, db, "github:*", 0)

	if w := createWorld(t, db, "github:new", "newworld"); w.Code == http.StatusSeeOther {
		t.Fatal("a disabled gate unlocked world creation")
	}
	if folderExists(t, db, "newworld") {
		t.Fatal("a disabled gate let a world be created")
	}
	if body := getOnboard(db, "github:new").Body.String(); strings.Contains(body, "create_world") {
		t.Error("a disabled gate rendered the username picker")
	}
}

// The refusal must stay a readable page, not a 500 or a blank body — a caller
// who is genuinely not entitled is the common case, not an error case.
func TestRefusedStrangerGetsTheHonestPage(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved") // no gate → not entitled

	w := getOnboard(db, "github:new")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 on the refusal page, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "create_world") {
		t.Fatal("the picker rendered for a caller the write would refuse")
	}
	if !strings.Contains(body, "invite") && !strings.Contains(body, "Invite") {
		t.Errorf("refusal page does not tell the caller what to do; body: %s", body)
	}
}
