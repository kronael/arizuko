package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// In the split topology onbod opens TWO DBs: obdb (its OWNED tables —
// onboarding/invites/onboarding_gates) and xdb (the routd-OWNED CROSS tables —
// acl/acl_membership/groups/auth_users/routes). The handlers take (xdb, obdb).
// These tests pass two distinct DBs (not the monolith db==db) to prove the
// cross reads/writes hit routd.db, never the onbod-owned DB.

// TestSplitDashboardReadsCrossFromRoutd seeds auth_users + acl + groups ONLY in
// xdb (routd.db) and asserts the dashboard finds the user and renders their
// world — i.e. it read the cross tables from xdb, not obdb.
func TestSplitDashboardReadsCrossFromRoutd(t *testing.T) {
	xdb := testDB(t)  // routd.db side
	obdb := testDB(t) // onbod.db side

	xdb.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '', '2026-01-01')`)
	xdb.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	xdb.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at)
		VALUES ('github:alice', 'admin', 'alice', 'allow', '2026-01-01')`)

	// The onbod-owned DB must NOT carry the cross rows (proving the read is xdb).
	var n int
	obdb.QueryRow(`SELECT COUNT(*) FROM auth_users WHERE sub = 'github:alice'`).Scan(&n)
	if n != 0 {
		t.Fatalf("test setup: auth_users leaked into obdb")
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

	xdb.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:bob', 'bob', 'Bob', '', '2026-01-01')`)
	xdb.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	xdb.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:bob', 'telegram:99', '2026-01-01')`)

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
	var inRoutd int
	xdb.QueryRow(`SELECT COUNT(*) FROM acl WHERE principal = 'github:bob' AND scope = 'alice'`).Scan(&inRoutd)
	if inRoutd != 1 {
		t.Errorf("acl grant not in routd.db, got %d rows", inRoutd)
	}
	var inOnbod int
	obdb.QueryRow(`SELECT COUNT(*) FROM acl WHERE principal = 'github:bob'`).Scan(&inOnbod)
	if inOnbod != 0 {
		t.Errorf("acl grant leaked into onbod.db, got %d rows", inOnbod)
	}

	// invites used_count increments in onbod.db (obdb).
	var used int
	obdb.QueryRow(`SELECT used_count FROM invites WHERE token = ?`, token).Scan(&used)
	if used != 1 {
		t.Errorf("want used_count=1 in onbod.db, got %d", used)
	}

	// route to bob's JID lands in routd.db.
	var routes int
	xdb.QueryRow(`SELECT COUNT(*) FROM routes WHERE match = 'room=99' AND target = 'alice'`).Scan(&routes)
	if routes != 1 {
		t.Errorf("want 1 route in routd.db, got %d", routes)
	}
}

// TestSplitCreateWorldWritesCrossToRoutd: create_world's groups/acl/routes
// writes all land in xdb (routd.db), not obdb.
func TestSplitCreateWorldWritesCrossToRoutd(t *testing.T) {
	xdb := testDB(t)
	obdb := testDB(t)

	xdb.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:new', 'github:new', 'New', '', '2026-01-01')`)
	xdb.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:new', 'telegram:10', '2026-01-01')`)

	w := postOnboardSplit(xdb, obdb, "github:new", url.Values{
		"action": {"create_world"}, "username": {"newworld"}, "csrf": {"c"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	var folder string
	xdb.QueryRow(`SELECT folder FROM groups WHERE folder = 'newworld'`).Scan(&folder)
	if folder != "newworld" {
		t.Errorf("group not created in routd.db")
	}
	var scope string
	xdb.QueryRow(`SELECT scope FROM acl WHERE principal = 'github:new'`).Scan(&scope)
	if scope != "newworld" {
		t.Errorf("acl grant not in routd.db, got %q", scope)
	}
	var routes int
	xdb.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'newworld'`).Scan(&routes)
	if routes != 1 {
		t.Errorf("want 1 route in routd.db, got %d", routes)
	}
	var leaked int
	obdb.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder = 'newworld'`).Scan(&leaked)
	if leaked != 0 {
		t.Errorf("group leaked into onbod.db")
	}
}

func postOnboardSplit(xdb, obdb *sql.DB, sub string, vals url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", sub)
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, xdb, obdb, config{})
	return w
}
