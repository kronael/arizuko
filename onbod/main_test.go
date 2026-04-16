package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/onvos/arizuko/auth"
	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE routes (id INTEGER PRIMARY KEY AUTOINCREMENT, seq INTEGER, match TEXT, target TEXT, impulse_config TEXT);
		CREATE TABLE groups (folder TEXT PRIMARY KEY, parent TEXT, name TEXT, added_at TEXT);
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, prompted_at TEXT, created TEXT, token TEXT, token_expires TEXT, user_sub TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, is_from_me INTEGER, is_bot_message INTEGER, source TEXT NOT NULL DEFAULT '');
		CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT, cron TEXT, next_run TEXT, status TEXT, created_at TEXT, context_mode TEXT);
		CREATE TABLE user_jids (user_sub TEXT NOT NULL, jid TEXT NOT NULL, claimed TEXT NOT NULL, PRIMARY KEY (user_sub, jid));
		CREATE UNIQUE INDEX idx_user_jids_jid ON user_jids(jid);
		CREATE TABLE user_groups (user_sub TEXT NOT NULL, folder TEXT NOT NULL, PRIMARY KEY (user_sub, folder));
		CREATE TABLE auth_users (id INTEGER PRIMARY KEY AUTOINCREMENT, sub TEXT UNIQUE, username TEXT, hash TEXT, name TEXT, created_at TEXT);
		CREATE TABLE channels (name TEXT PRIMARY KEY, url TEXT, capabilities TEXT);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPromptUnpromptedSetsToken(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:1', 'awaiting_message', '2026-01-01')`)

	cfg := config{greeting: "Welcome!", authBaseURL: "https://example.com"}
	promptUnprompted(db, cfg)

	var token sql.NullString
	var prompted sql.NullString
	db.QueryRow(`SELECT token, prompted_at FROM onboarding WHERE jid = 'telegram:1'`).Scan(&token, &prompted)
	if !token.Valid || token.String == "" {
		t.Error("expected token to be set")
	}
	if !prompted.Valid {
		t.Error("expected prompted_at to be set")
	}
	if len(token.String) != 64 {
		t.Errorf("expected 64-char hex token, got %d chars", len(token.String))
	}
}

func TestTokenLandingValid(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'abc123', '2099-01-01T00:00:00Z', '2026-01-01')`)

	cfg := config{authBaseURL: "https://example.com"}
	req := httptest.NewRequest("GET", "/onboard?token=abc123", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("want redirect to /auth/login, got %s", loc)
	}

	// Token should be consumed: status='token_used', token=NULL
	var status string
	var token sql.NullString
	db.QueryRow(`SELECT status, token FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status, &token)
	if status != "token_used" {
		t.Errorf("want status=token_used, got %s", status)
	}
	if token.Valid {
		t.Errorf("want token=NULL, got %q", token.String)
	}

	// Second use of same token should fail (show invalid page)
	req2 := httptest.NewRequest("GET", "/onboard?token=abc123", nil)
	w2 := httptest.NewRecorder()
	handleOnboard(w2, req2, db, cfg)
	if w2.Code != http.StatusOK {
		t.Errorf("want 200 (error page) on reuse, got %d", w2.Code)
	}
}

func TestTokenLandingExpired(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'abc123', '2020-01-01T00:00:00Z', '2026-01-01')`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard?token=abc123", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestTokenLandingInvalid(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard?token=nonexistent", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestDashboardLinksJID(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token_expires, created)
		VALUES ('telegram:1', 'token_used', '2099-01-01T00:00:00Z', '2026-01-01')`)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '', '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('github:alice', 'alice')`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "telegram:1"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	// JID should be linked
	var userSub string
	db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = 'telegram:1'`).Scan(&userSub)
	if userSub != "github:alice" {
		t.Errorf("want user_sub=github:alice, got %q", userSub)
	}

	// Onboarding status should be approved
	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "approved" {
		t.Errorf("want approved, got %s", status)
	}
}

func TestDashboardShowsUsernamePicker(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:new', 'github:new', 'New User', '', '2026-01-01')`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:new")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !containsStr(body, "create_world") {
		t.Error("expected username picker form")
	}
}

func TestCreateWorldValidUsername(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:new', 'github:new', 'New User', '', '2026-01-01')`)

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"alice"}}
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d", w.Code)
	}

	// Group created
	var folder string
	db.QueryRow(`SELECT folder FROM groups WHERE folder = 'alice'`).Scan(&folder)
	if folder != "alice" {
		t.Errorf("group not created")
	}

	// user_groups granted
	var ug string
	db.QueryRow(`SELECT folder FROM user_groups WHERE user_sub = 'github:new'`).Scan(&ug)
	if ug != "alice" {
		t.Errorf("user_groups not set, got %q", ug)
	}

	// username updated
	var un string
	db.QueryRow(`SELECT username FROM auth_users WHERE sub = 'github:new'`).Scan(&un)
	if un != "alice" {
		t.Errorf("username not updated, got %q", un)
	}
}

func TestCreateWorldInvalidUsername(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"A!"}}
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestCreateWorldDuplicateUsername(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO groups (folder, name, parent, added_at) VALUES ('alice', 'alice', NULL, '2026-01-01')`)
	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"alice"}}
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestUsernameValidation(t *testing.T) {
	valid := []string{"abc", "alice", "my-world", "a12", "abcdefghijklmnopqrstuvwxyz1234"}
	for _, u := range valid {
		if !usernameRe.MatchString(u) {
			t.Errorf("expected valid: %q", u)
		}
	}
	invalid := []string{"ab", "1abc", "ABC", "a!", "a b", ""}
	for _, u := range invalid {
		if usernameRe.MatchString(u) {
			t.Errorf("expected invalid: %q", u)
		}
	}
}

func TestLinkJID(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:1', 'token_used', '2026-01-01')`)

	linkJID(db, "telegram:1", "github:alice")

	var userSub string
	db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = 'telegram:1'`).Scan(&userSub)
	if userSub != "github:alice" {
		t.Errorf("want github:alice, got %q", userSub)
	}

	var status, sub string
	db.QueryRow(`SELECT status, user_sub FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status, &sub)
	if status != "approved" {
		t.Errorf("want approved, got %s", status)
	}
	if sub != "github:alice" {
		t.Errorf("want github:alice, got %q", sub)
	}
}

func containsStr(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && bytes.Contains([]byte(s), []byte(sub))
}

func TestTokenDoubleUse(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'tok-once', '2099-01-01T00:00:00Z', '2026-01-01')`)

	cfg := config{authBaseURL: "https://example.com"}

	req1 := httptest.NewRequest("GET", "/onboard?token=tok-once", nil)
	w1 := httptest.NewRecorder()
	handleOnboard(w1, req1, db, cfg)

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "token_used" {
		t.Fatalf("first use: expected token_used, got %s", status)
	}

	req2 := httptest.NewRequest("GET", "/onboard?token=tok-once", nil)
	w2 := httptest.NewRecorder()
	handleOnboard(w2, req2, db, cfg)

	if w2.Code != http.StatusOK {
		t.Errorf("second use: want 200 (error page), got %d", w2.Code)
	}
}

func TestLinkJIDIdempotent(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:5', 'token_used', '2026-01-01')`)

	linkJID(db, "telegram:5", "github:alice")
	linkJID(db, "telegram:5", "github:alice")

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM user_jids WHERE jid = 'telegram:5'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 user_jids row, got %d", n)
	}
}

func TestLinkJIDDifferentUserRejected(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:6', 'token_used', '2026-01-01')`)

	linkJID(db, "telegram:6", "github:alice")
	linkJID(db, "telegram:6", "github:eve")

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM user_jids WHERE jid = 'telegram:6'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 user_jids row, got %d", n)
	}

	var userSub string
	db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = 'telegram:6'`).Scan(&userSub)
	if userSub != "github:alice" {
		t.Errorf("want github:alice, got %q", userSub)
	}
}

func TestCreateWorldRoutesLinkedJIDs(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:new', 'github:new', 'New User', '', '2026-01-01')`)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed) VALUES ('github:new', 'telegram:10', '2026-01-01')`)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed) VALUES ('github:new', 'discord:20', '2026-01-01')`)

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"newworld"}}
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'newworld'`).Scan(&n)
	if n != 2 {
		t.Errorf("expected 2 routes for linked JIDs, got %d", n)
	}

	var match1, match2 string
	rows, _ := db.Query(`SELECT match FROM routes WHERE target = 'newworld' ORDER BY match`)
	defer rows.Close()
	if rows.Next() {
		rows.Scan(&match1)
	}
	if rows.Next() {
		rows.Scan(&match2)
	}
	if match1 != "room=10" || match2 != "room=20" {
		t.Errorf("expected room=10 and room=20, got %q and %q", match1, match2)
	}
}

func TestCreateWorldNoLinkedJIDs(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:lonely', 'github:lonely', 'Lonely', '', '2026-01-01')`)

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"lonely"}}
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:lonely")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	var folder string
	db.QueryRow(`SELECT folder FROM groups WHERE folder = 'lonely'`).Scan(&folder)
	if folder != "lonely" {
		t.Error("group not created")
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'lonely'`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 routes, got %d", n)
	}
}

// A user who already has a world adds a new JID via a second auth link:
// the JID gets linked AND a route to the existing world is inserted
// automatically. The dashboard renders normally (no username picker).
func TestSecondJIDAutoLink(t *testing.T) {
	db := testDB(t)
	// Existing user with a world "alice/".
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, name, parent, added_at)
		VALUES ('alice', 'alice', NULL, '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder)
		VALUES ('github:alice', 'alice')`)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed)
		VALUES ('github:alice', 'telegram:1', '2026-01-01')`)
	// Existing route for first JID.
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=1', 'alice')`)

	// Second JID (discord:42) arrives, onboarding token consumed, cookie set.
	db.Exec(`INSERT INTO onboarding (jid, status, token_expires, created)
		VALUES ('discord:42', 'token_used', '2099-01-01T00:00:00Z', '2026-01-01')`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "discord:42"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (dashboard), got %d", w.Code)
	}
	body := w.Body.String()
	if containsStr(body, "create_world") {
		t.Error("should not show username picker on second-JID link")
	}

	// discord:42 should be linked.
	var userSub string
	db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = 'discord:42'`).Scan(&userSub)
	if userSub != "github:alice" {
		t.Errorf("want user_sub=github:alice for discord:42, got %q", userSub)
	}
	// And auto-routed to existing world.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE match = 'room=42' AND target = 'alice'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 auto-route for second JID to alice, got %d", n)
	}
}

// Operator (user_groups has "**") can create world for any folder: the
// authorization check short-circuits on operator and routes are written.
func TestCreateWorldOperatorAllowed(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:op', 'op', 'Op', '', '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('github:op', '**')`)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed)
		VALUES ('github:op', 'telegram:99', '2026-01-01')`)

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"opworld"}}
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:op")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'opworld'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 route, got %d", n)
	}
}

// userGroups helper reports operator=true for "**" rows and returns the
// literal folder list otherwise.
func TestUserGroupsHelper(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('u:alice', 'alice')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('u:alice', 'pub/*')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('u:op', '**')`)

	if allowed, op := userGroups(db, "u:alice"); op {
		t.Errorf("alice should not be operator")
	} else if len(allowed) != 2 {
		t.Errorf("alice: want 2 patterns, got %v", allowed)
	}
	if _, op := userGroups(db, "u:op"); !op {
		t.Errorf("** should mark operator")
	}
	if allowed, op := userGroups(db, "u:none"); op || len(allowed) != 0 {
		t.Errorf("unknown user should return empty, got %v op=%v", allowed, op)
	}
}

// A non-operator user attempting to place routes into a folder outside their
// user_groups grant list is rejected with 403. We exercise this by placing an
// entry in user_groups so groupCount>0 (skipping the username picker path is
// not the subject here — we hit handleCreateWorld directly with a mismatched
// allowed list). create_world always self-inserts the folder into user_groups
// first, so to see the denial we must first revoke that. Simulate by
// intercepting: create a user with an explicit non-matching grant pattern
// and check that attempting a DIFFERENT folder is blocked.
func TestRouteCreationDeniedWithoutGrant(t *testing.T) {
	// We test the gate directly by calling the helper+MatchGroups combo the
	// handler uses. This avoids racing against the handler's own INSERT into
	// user_groups. Assertion: for user with grant ["alice"], folder "bob"
	// is denied while folder "alice" is allowed.
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('u:alice', 'alice')`)

	allowed, isOp := userGroups(db, "u:alice")
	if isOp {
		t.Fatal("expected non-operator")
	}
	if !auth.MatchGroups(allowed, "alice") {
		t.Error("alice should be allowed for alice")
	}
	if auth.MatchGroups(allowed, "bob") {
		t.Error("alice should NOT be allowed for bob")
	}
}
