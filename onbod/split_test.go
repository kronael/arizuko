package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// In the split topology onbod opens TWO DBs: obdb (its OWNED tables —
// onboarding/invites/onboarding_gates) and xdb (the routd-OWNED CROSS tables —
// acl/acl_membership/groups/user_profiles/routes). The handlers take (xdb, obdb).
// These tests pass two distinct DBs (not the monolith db==db) to prove the
// cross reads/writes hit routd.db, never the onbod-owned DB.

// TestSplitDashboardReadsCrossFromRoutd seeds user_profiles + acl + groups ONLY in
// xdb (routd.db) and asserts the dashboard finds the user and renders their
// world — i.e. it read the cross tables from xdb, not obdb.
func TestSplitDashboardReadsCrossFromRoutd(t *testing.T) {
	xdb := testDB(t)  // routd.db side
	obdb := testDB(t) // onbod.db side

	if err := xdb.CreateUserProfile("github:alice", "alice", "Alice"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedFolder(t, xdb, "alice")
	seedGrant(t, xdb, "github:alice", "admin", "alice")

	// The onbod-owned DB must NOT carry the cross rows (proving the read is xdb).
	if _, known, _ := obdb.Username("github:alice"); known {
		t.Fatalf("test setup: user_profiles leaked into obdb")
	}

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	handleOnboard(w, req, xdb, obdb, cfg)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 dashboard, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "alice") {
		t.Errorf("dashboard did not render the routd.db user; body: %s", w.Body.String())
	}
}

// TestSplitInviteGrantLandsInRoutd: redeeming a non-subgroup invite must write
// the acl admin grant to xdb (routd.db), NOT obdb (onbod.db). The invite row
// itself stays in obdb.
func TestSplitInviteGrantLandsInRoutd(t *testing.T) {
	xdb := testDB(t)
	obdb := testDB(t)

	if err := xdb.CreateUserProfile("github:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedFolder(t, xdb, "alice")
	seedMembership(t, xdb, "telegram:99", "github:bob", "")

	token := createInvite(t, obdb, "alice", "telegram:1", 1)

	cfg := config{authBaseURL: "https://example.com"}
	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, xdb, obdb, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	// acl grant must be in routd.db (xdb), absent from onbod.db (obdb).
	if got := aclScopes(t, xdb, "github:bob"); len(got) != 1 || got[0] != "alice" {
		t.Errorf("acl grant not in routd.db, got %v", got)
	}
	if got := aclScopes(t, obdb, "github:bob"); len(got) != 0 {
		t.Errorf("acl grant leaked into onbod.db, got %v", got)
	}

	// invites used_count increments in onbod.db (obdb). Keyed by ref, not the
	// raw token (I1: onbod.db stores only the hash — a raw-token lookup would
	// find nothing against the post-I1 schema).
	used := inviteUsed(t, obdb, store.TokenRef(token))
	if used != 1 {
		t.Errorf("want used_count=1 in onbod.db, got %d", used)
	}

	// Redemption grants authority and writes NO route (5/18 step 7). It used to
	// route every paired JID at the target; /onboard's step-6 branch routes the
	// one unrouted JID instead, as an attributed act.
	if routes := countRoutes(t, xdb); routes != 0 {
		t.Errorf("invite redemption wrote %d route(s); want 0", routes)
	}
}

// TestSplitCreateWorldWritesCrossToRoutd: create_world's groups/acl/routes
// writes all land in xdb (routd.db), not obdb.
func TestSplitCreateWorldWritesCrossToRoutd(t *testing.T) {
	xdb := testDB(t)
	obdb := testDB(t)

	if err := xdb.CreateUserProfile("github:new", "github:new", "New"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedMembership(t, xdb, "telegram:10", "github:new", "")
	// The create authority is onbod-OWNED: the redemption is read from obdb even
	// though every write below lands in xdb.
	seedRedemption(t, obdb, "github:new", "/")

	w := postOnboardSplit(xdb, obdb, "github:new", url.Values{
		"action": {"create_world"}, "username": {"newworld"}, "csrf": {"c"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	if folder := folderName(t, xdb, "newworld"); folder != "newworld" {
		t.Errorf("group not created in routd.db")
	}
	if scope := firstScope(t, xdb, "github:new"); scope != "newworld/**" {
		t.Errorf("acl grant should be subtree-scoped in routd.db, got %q", scope)
	}
	// create_world writes NO route (5/18 step 7) — the caller asked for a world,
	// not for their chats to move. The redirect lands on /onboard, which routes
	// the one unrouted JID.
	if routes := countRoutes(t, xdb); routes != 0 {
		t.Errorf("create_world wrote %d route(s); want 0", routes)
	}
	if folderName(t, obdb, "newworld") != "" {
		t.Errorf("group leaked into onbod.db")
	}
}

// TestSplitInviteGrantFailureRollsBack: when the acl grant write to routd.db
// (xdb) fails AFTER ConsumeInviteNoGrant succeeded on onbod.db (obdb), there's
// no shared tx — handleInvite must RestoreInvite so used_count goes back to 0
// (no burned invite = permanent lockout) and return a non-2xx Setup-Failed
// page instead of redirecting as success. Guards the silent-lockout bug.
func TestSplitInviteGrantFailureRollsBack(t *testing.T) {
	xdb := testDB(t)
	obdb := testDB(t)

	if err := xdb.CreateUserProfile("github:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedFolder(t, xdb, "alice")
	// Break the routd.db acl write so PutACLRow errors after the consume.
	if _, err := xdb.DB().Exec(`DROP TABLE acl`); err != nil {
		t.Fatalf("drop acl: %v", err)
	}

	token := createInvite(t, obdb, "alice", "telegram:1", 1)

	cfg := config{authBaseURL: "https://example.com"}
	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, xdb, obdb, cfg)

	if w.Code == http.StatusSeeOther {
		t.Fatalf("grant failure returned 303 success; want Setup-Failed page")
	}
	if !strings.Contains(w.Body.String(), "Setup Failed") {
		t.Errorf("want Setup-Failed page, got body: %s", w.Body.String())
	}

	// The consume must be rolled back: used_count back to 0, invite not burned.
	used := inviteUsed(t, obdb, store.TokenRef(token))
	if used != 0 {
		t.Errorf("invite burned without grant: used_count=%d want 0 (RestoreInvite)", used)
	}
}

func postOnboardSplit(xdb, obdb *store.Store, sub string, vals url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", sub)
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, xdb, obdb, config{})
	return w
}

// The invite-accept acl grant must be audited. It writes to routd.db (xdb)
// while onbod's own audit.Init points at onbod.db (obdb) — but AddACLRow emits
// by appending the record to its own batch, so the audit row lands
// in xdb beside the grant. That cross-DB shape is what the audit-free choice was
// justified by; it was never a reason.
//
// Falsifiable: swap the AddACLRow back to the audit-free PutACLRow and the grant
// still lands (TestSplitInviteGrantLandsInRoutd stays green) while this case
// fails, finding no acl.add row.
func TestSplitInviteGrantAudited(t *testing.T) {
	xdb := testDB(t)
	obdb := testDB(t)

	if err := xdb.CreateUserProfile("github:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedFolder(t, xdb, "alice")

	token := createInvite(t, obdb, "alice", "telegram:1", 1)

	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, xdb, obdb, config{authBaseURL: "https://example.com"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	granted := auditRowsFor(t, xdb, "acl.add")
	if len(granted) == 0 {
		t.Fatal("invite grant left no acl.add row in routd.db")
	}
	// GrantedBy stands in as the actor for callers that never set AsUser.
	if granted[0].Actor != "invite" {
		t.Errorf("acl.add actor = %q, want invite", granted[0].Actor)
	}
	if granted[0].Folder != "alice" {
		t.Errorf("acl.add folder = %q, want alice", granted[0].Folder)
	}

	// The audit row belongs with the grant, in routd.db — not onbod.db.
	if inOnbod := auditRowsFor(t, obdb, "acl.add"); len(inOnbod) != 0 {
		t.Errorf("acl.add audit row landed in onbod.db (%d rows); want it in routd.db", len(inOnbod))
	}
}
