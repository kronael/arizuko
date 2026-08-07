package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// BUGS F50. handleCreateWorld took the parent folder from the `pending_target`
// cookie and used it verbatim. HttpOnly stops page JS; it does not stop the
// caller, who owns the browser. So an authenticated identity holding no grant
// anywhere posted `Cookie: pending_target=victim/` and got `victim/pwned` plus
// admin over it.
//
// The parent now comes from the invite_redemptions row store.consumeInvite
// wrote. The tests below send the forged cookie anyway — a cookie the fix
// ignores is exactly what proves it is no longer an authority.

// postCreateWorld posts create_world with a forged `pending_target`. Every
// attack case here sends a parent the caller genuinely cannot administer,
// because a probe that sends the honest value proves nothing: the pre-existing
// path would accept it too.
func postCreateWorld(db *sql.DB, sub, username, forgedTarget string) *httptest.ResponseRecorder {
	vals := url.Values{"action": {"create_world"}, "username": {username}, "csrf": {"c"}}
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", sub)
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	if forgedTarget != "" {
		req.AddCookie(&http.Cookie{Name: "pending_target", Value: forgedTarget})
	}
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, config{})
	return w
}

// allFolders lists every groups row. The assertions read this rather than
// looking up an expected name: a breach is a folder that EXISTS somewhere, and
// "SELECT ... WHERE folder = 'newworld'" cannot see "victim/newworld".
func allFolders(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT folder FROM groups ORDER BY folder`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// aclScopes lists every scope granted to a principal. A 303 with no folder is
// not a breach; a folder with no grant is not a breach either. Both are read
// because the escalation was the pair.
func aclScopes(t *testing.T, db *sql.DB, principal string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT scope FROM acl WHERE principal = ? ORDER BY scope`, principal)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func redemptionCount(t *testing.T, db *sql.DB, sub string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM invite_redemptions WHERE user_sub = ?`, sub).Scan(&n); err != nil {
		t.Fatalf("invite_redemptions count: %v", err)
	}
	return n
}

// seedTenant creates a world that already belongs to someone else — the subtree
// the attacker aims at.
func seedTenant(t *testing.T, db *sql.DB, folder, owner string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO groups (folder, added_at) VALUES (?, '2026-01-01')`, folder); err != nil {
		t.Fatalf("seed tenant group: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO acl (principal, action, scope, effect, granted_at)
		 VALUES (?, 'admin', ?, 'allow', '2026-01-01')`, owner, folder+"/**"); err != nil {
		t.Fatalf("seed tenant grant: %v", err)
	}
}

func seedProfile(t *testing.T, db *sql.DB, sub string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES (?, ?, 'User', '2026-01-01')`, sub, sub); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
}

// The escalation, reproduced. github:mallory holds no grant, no invite, no
// admission — the only thing they bring is a cookie they wrote themselves.
//
// Falsifiable: restore `parent := strings.TrimSuffix(cookie.Value, "/")` in
// handleCreateWorld and this goes red on the folder AND on the acl scope.
func TestForgedPendingTargetCreatesNoFolderAndNoGrant(t *testing.T) {
	db := testDB(t)
	seedProfile(t, db, "github:mallory")
	seedTenant(t, db, "victim", "github:victim")

	w := postCreateWorld(db, "github:mallory", "pwned", "victim/")

	if w.Code == http.StatusSeeOther {
		t.Errorf("forged pending_target was accepted (303)")
	}
	if got := allFolders(t, db); len(got) != 1 || got[0] != "victim" {
		t.Errorf("folders = %v, want only the pre-existing [victim]; a forged cookie created one", got)
	}
	if got := aclScopes(t, db, "github:mallory"); len(got) != 0 {
		t.Errorf("mallory holds %v; a caller with no authority must end with none", got)
	}
}

// The same forgery from a caller who is NOT a stranger: github:alice genuinely
// administers acme and forges her way into an unrelated tenant. Holding one
// grant must not turn a client-supplied string into a second one.
func TestGrantHolderCannotForgeAnotherTenantsParent(t *testing.T) {
	db := testDB(t)
	seedProfile(t, db, "github:alice")
	seedTenant(t, db, "acme", "github:alice")
	seedTenant(t, db, "other", "github:other")

	w := postCreateWorld(db, "github:alice", "grabbed", "other/")

	if w.Code == http.StatusSeeOther {
		t.Errorf("a grant on acme let alice create under other/ (303)")
	}
	for _, f := range allFolders(t, db) {
		if strings.HasPrefix(f, "other/") {
			t.Errorf("folder %q created inside a tenant alice cannot administer", f)
		}
	}
	if got := aclScopes(t, db, "github:alice"); len(got) != 1 || got[0] != "acme/**" {
		t.Errorf("alice's grants = %v, want only [acme/**]", got)
	}
}

// The flow the fix must not break, and the one a `auth.Authorize(sub, "admin",
// parent)` predicate WOULD have broken: consumeInvite writes the acl row only
// for non-slash targets, so bob holds nothing over acme at the moment he is
// asked to pick a username. The redemption row is the whole authority.
func TestRedeemedSubgroupInviteCreatesUnderItsParent(t *testing.T) {
	db := testDB(t)
	seedProfile(t, db, "github:bob")
	seedTenant(t, db, "acme", "github:acmeowner")
	token := createInvite(t, db, "acme/", "github:acmeowner", 1)

	redeemInvite(t, db, token, "github:bob")

	// The premise, asserted rather than assumed: a predicate over acl would find
	// nothing here and refuse the legitimate redeemer.
	if got := aclScopes(t, db, "github:bob"); len(got) != 0 {
		t.Fatalf("subgroup redeemer already holds %v; the fixture no longer models the case", got)
	}
	if n := redemptionCount(t, db, "github:bob"); n != 1 {
		t.Fatalf("consumeInvite recorded %d redemptions, want 1", n)
	}

	t.Setenv("DATA_DIR", t.TempDir())
	w := postCreateWorld(db, "github:bob", "bobsteam", "")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("legitimate subgroup redeemer refused: %d %s", w.Code, w.Body.String())
	}
	if got := allFolders(t, db); len(got) != 2 || got[0] != "acme" || got[1] != "acme/bobsteam" {
		t.Errorf("folders = %v, want [acme acme/bobsteam]", got)
	}
	if got := aclScopes(t, db, "github:bob"); len(got) != 1 || got[0] != "acme/bobsteam/**" {
		t.Errorf("bob's grants = %v, want [acme/bobsteam/**]", got)
	}
}

// One redemption is one world. The row is deleted as the claim, so a replay —
// a resubmitted form, a second tab — derives no parent and creates nothing.
func TestSubgroupRedemptionIsSingleUse(t *testing.T) {
	db := testDB(t)
	seedProfile(t, db, "github:bob")
	seedTenant(t, db, "acme", "github:acmeowner")
	token := createInvite(t, db, "acme/", "github:acmeowner", 1)
	redeemInvite(t, db, token, "github:bob")

	t.Setenv("DATA_DIR", t.TempDir())
	if w := postCreateWorld(db, "github:bob", "first", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("first create refused: %d %s", w.Code, w.Body.String())
	}
	if n := redemptionCount(t, db, "github:bob"); n != 0 {
		t.Errorf("%d redemption(s) survive a successful create; the authority is not spent", n)
	}

	w := postCreateWorld(db, "github:bob", "second", "")

	if w.Code == http.StatusSeeOther {
		t.Errorf("replaying a spent redemption was accepted (303)")
	}
	for _, f := range allFolders(t, db) {
		if f == "acme/second" {
			t.Errorf("a spent redemption created a second world %q", f)
		}
	}
}

// The two authorities must not compose. An approved admission under a
// configured gate is spec 5/18 step 8 and entitles a TOP-LEVEL folder; a cookie
// must not deepen it into someone else's subtree.
func TestApprovedAdmissionPlusForgedCookieStaysTopLevel(t *testing.T) {
	db := testDB(t)
	seedStranger(t, db, "github:new", "approved")
	seedGate(t, db, "github:*", 1)
	seedTenant(t, db, "victim", "github:victim")

	t.Setenv("DATA_DIR", t.TempDir())
	w := postCreateWorld(db, "github:new", "newworld", "victim/")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("step 8 broke: approved + gate was refused: %d %s", w.Code, w.Body.String())
	}
	if got := allFolders(t, db); len(got) != 2 || got[0] != "newworld" || got[1] != "victim" {
		t.Errorf("folders = %v, want [newworld victim] — the cookie deepened the admission", got)
	}
	if got := aclScopes(t, db, "github:new"); len(got) != 1 || got[0] != "newworld/**" {
		t.Errorf("grants = %v, want only [newworld/**]", got)
	}
}

// Hiding the form is not a control, but rendering it for a caller the write
// will refuse is its own bug: both sides read the same two functions.
func TestForgedCookieDoesNotRenderThePicker(t *testing.T) {
	db := testDB(t)
	seedProfile(t, db, "github:mallory")

	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:mallory")
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "victim/"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, config{})

	if strings.Contains(w.Body.String(), "create_world") {
		t.Error("a forged cookie rendered the username picker")
	}
}

// redeemInvite walks the real redemption handler, so the redemption row under
// test is the one production writes — not a hand-INSERTed fixture that could
// disagree with consumeInvite about the column values.
func redeemInvite(t *testing.T, db *sql.DB, token, sub string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", sub)
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, config{authBaseURL: "https://example.com"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("redeem %q: want 303, got %d: %s", token, w.Code, w.Body.String())
	}
}

// seedRedemption gives sub the authority a redeemed invite records, by minting
// and redeeming a real one. Tests whose subject is something else use this
// instead of hand-INSERTing a row: a fixture that disagreed with consumeInvite
// about the columns would make them pass against a parent nothing writes.
// targetGlob "/" is the top-level invite (parent = "").
func seedRedemption(t *testing.T, db *sql.DB, sub, targetGlob string) {
	t.Helper()
	redeemInvite(t, db, createInvite(t, db, targetGlob, "operator", 1), sub)
	if n := redemptionCount(t, db, sub); n != 1 {
		t.Fatalf("seeded %d redemptions for %s, want 1", n, sub)
	}
}

// The hand-written schema in testDB is a model of onbod.db, not onbod.db. This
// runs the real migration chain so a table the tests above rely on cannot exist
// only in the model.
func TestInviteRedemptionsTableMigrates(t *testing.T) {
	db, err := openOwnedDB(filepath.Join(t.TempDir(), "onbod.db"))
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO invite_redemptions (user_sub, target_glob, redeemed_at)
		 VALUES ('github:bob', 'acme/', '2026-08-07T00:00:00Z')`); err != nil {
		t.Fatalf("migrated table does not accept a redemption row: %v", err)
	}
	var glob string
	if err := db.QueryRow(
		`SELECT target_glob FROM invite_redemptions WHERE user_sub = 'github:bob'`).Scan(&glob); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if glob != "acme/" {
		t.Errorf("target_glob = %q, want %q", glob, "acme/")
	}
}
