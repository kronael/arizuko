package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
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
	if err := store.New(db).CreateUserProfile(sub, sub, "Stranger"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedAdmission(t, db, store.ArchiveOnboardingRow{
		JID: "telegram:group/1", Status: status, UserSub: sub})
	got := admissionStatus(t, db, "telegram:group/1")
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
	seedGateRow(t, db, gate, 10, enabled == 1)
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
	return folderName(t, db, folder) != ""
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
	for _, f := range allFolders(t, db) {
		if strings.Contains(f, "/") {
			t.Errorf("approved admission created a NESTED folder %q; it authorizes top-level only", f)
		}
	}

	scopes := adminScopes(t, db, "github:new")
	if len(scopes) != 1 {
		t.Fatalf("admin grants for the created world = %v, want exactly one", scopes)
	}
	if scopes[0] != "newworld/**" {
		t.Errorf("admin scope = %q, want %q", scopes[0], "newworld/**")
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

// A caller who LOSES the race for a folder must not be left renamed, and must
// not be handed a grant on somebody else's world.
//
// That guarantee used to come from a transaction the four writes shared. It now
// comes from the ORDER: the groups row, the member edge and the admin grant go
// in one guarded batch whose only conditional write is the create-if-absent
// groups row, so a loser writes nothing at all — and the rename runs afterwards,
// on the folder it won, rather than before the folder is decided.
//
// It posts nothing, deliberately: the handler has a TOCTOU fast path that
// answers "username taken" before it ever writes, so a form post proves that
// check and not this ordering. writeWorld is the unit that owns the guarantee,
// and moving store.SetUsername back above its batch turns this red.
func TestALostRaceForAFolderLeavesTheCallerUnnamed(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")
	// Somebody else got there first.
	if err := store.New(db).PutGroupRow(core.Group{Folder: "taken", AddedAt: time.Now()}); err != nil {
		t.Fatalf("seed the folder the caller loses: %v", err)
	}

	if err := writeWorld(db, "taken", "taken", "github:new"); err == nil {
		t.Fatal("creating a folder somebody else holds reported success")
	}

	name, _, err := store.New(db).Username("github:new")
	if err != nil {
		t.Fatalf("read the caller's name back: %v", err)
	}
	if name != "github:new" {
		t.Errorf("the caller is named %q after losing the race, want the seeded github:new: a "+
			"rename that runs before the folder is decided renames a person for a world "+
			"that is not theirs", name)
	}
	if scopes := adminScopes(t, db, "github:new"); len(scopes) != 0 {
		t.Errorf("the loser holds %v on a folder somebody else created", scopes)
	}
}

// The winner IS named, and holds the grant and the member edge its folder
// implies — the other half of the same ordering.
func TestTheWinnerIsNamedAndHoldsWhatTheFolderImplies(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")

	if err := writeWorld(db, "newworld", "newworld", "github:new"); err != nil {
		t.Fatalf("create the world: %v", err)
	}

	name, _, err := store.New(db).Username("github:new")
	if err != nil {
		t.Fatal(err)
	}
	if name != "newworld" {
		t.Errorf("the winner is named %q, want newworld", name)
	}
	if scopes := adminScopes(t, db, "github:new"); len(scopes) != 1 || scopes[0] != "newworld/**" {
		t.Errorf("admin scopes = %v, want one newworld/**", scopes)
	}
	// A groups row implies the member edge (BUGS L119): without it the world
	// expands to nothing and comes up mute.
	if up := store.New(db).Ancestors("folder:newworld"); !slices.Contains(up, store.RoleMember) {
		t.Errorf("folder:newworld sits under %v, want role:member among them", up)
	}
}

// A WORLD IS GRANTED ITS REACH AND ITS HOLD, in the same act that creates it. A
// world granted no tools cannot create a cron and nobody can tell why; a world
// granted every tool with no hold acts unwatched. Two rows carry both, and they
// are scoped to the world's own subtree so neither reaches a sibling tenant.
func TestAWorldIsGrantedItsReachAndItsHold(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")

	if err := writeWorld(db, "newworld", "newworld", "github:new"); err != nil {
		t.Fatalf("create the world: %v", err)
	}

	rows, err := store.New(db).ListACL("folder:newworld")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"mcp:*":                "newworld/**",
		store.HoldPrefix + "*": "newworld/**",
	}
	for _, r := range rows {
		scope, ok := want[r.Action]
		if !ok {
			t.Errorf("the world was granted %q on %q, which nothing asked for", r.Action, r.Scope)
			continue
		}
		if r.Scope != scope || r.Effect != store.EffectAllow {
			t.Errorf("%s = (%q, %q), want (%q, allow)", r.Action, r.Scope, r.Effect, scope)
		}
		delete(want, r.Action)
	}
	for action := range want {
		t.Errorf("the world holds no %q row: it can %s", action,
			map[string]string{
				"mcp:*":                "reach no tool it was not separately granted",
				store.HoldPrefix + "*": "act without its owner ever being asked",
			}[action])
	}
}
