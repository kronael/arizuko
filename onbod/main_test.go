package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

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
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, prompted_at TEXT, created TEXT, token TEXT, token_expires TEXT, user_sub TEXT, channel TEXT NOT NULL DEFAULT '');
		CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, is_from_me INTEGER, is_bot_message INTEGER, source TEXT NOT NULL DEFAULT '');
		CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT, cron TEXT, next_run TEXT, status TEXT, created_at TEXT, context_mode TEXT);
		CREATE TABLE user_jids (user_sub TEXT NOT NULL, jid TEXT NOT NULL, claimed TEXT NOT NULL, PRIMARY KEY (user_sub, jid));
		CREATE TABLE user_groups (user_sub TEXT NOT NULL, folder TEXT NOT NULL, PRIMARY KEY (user_sub, folder));
		CREATE TABLE auth_users (id INTEGER PRIMARY KEY AUTOINCREMENT, sub TEXT UNIQUE, username TEXT, hash TEXT, name TEXT, created_at TEXT);
		CREATE TABLE channels (name TEXT PRIMARY KEY, url TEXT, capabilities TEXT);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestIsTier0True(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO groups (folder, parent) VALUES ('main', NULL)`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'platform=telegram room=123', 'main')`)
	if !isTier0(db, "telegram:123") {
		t.Error("expected isTier0=true for root group sender")
	}
}

func TestIsTier0False(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO groups (folder, parent) VALUES ('sub', 'main')`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'platform=telegram room=456', 'sub')`)
	if isTier0(db, "telegram:456") {
		t.Error("expected isTier0=false for child group sender")
	}
}

func TestIsTier0NotFound(t *testing.T) {
	db := testDB(t)
	if isTier0(db, "telegram:999") {
		t.Error("expected isTier0=false for unknown sender")
	}
}

func TestHandleSendMalformedJSON(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	req := httptest.NewRequest("POST", "/send", bytes.NewReader([]byte("{bad")))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandleSendBadAuth(t *testing.T) {
	db := testDB(t)
	cfg := config{secret: "sekret"}
	body, _ := json.Marshal(map[string]string{"jid": "x", "text": "/approve y z"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestHandleSendUnknownCommand(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	body, _ := json.Marshal(map[string]string{"jid": "telegram:1", "text": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandleSendApproveNotTier0(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	body, _ := json.Marshal(map[string]string{"jid": "telegram:1", "text": "/approve somejid somefolder"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}

func TestHandleSendApproveMissingFolder(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	body, _ := json.Marshal(map[string]string{"jid": "telegram:1", "text": "/approve somejid"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
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
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'tok123', '2099-01-01T00:00:00Z', '2026-01-01')`)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '', '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('github:alice', 'alice')`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: "onboard_token", Value: "tok123"})
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

func TestApproveInTxFlatFolder(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:1', 'pending', '2026-01-01')`)

	if err := approveInTx(db, "telegram:1", "myroom"); err != nil {
		t.Fatalf("approveInTx: %v", err)
	}

	var folder string
	db.QueryRow(`SELECT folder FROM groups WHERE folder = 'myroom'`).Scan(&folder)
	if folder != "myroom" {
		t.Errorf("group not created, got %q", folder)
	}

	var match, target string
	db.QueryRow(`SELECT match, target FROM routes WHERE target = 'myroom'`).Scan(&match, &target)
	if match != "room=1" || target != "myroom" {
		t.Errorf("route: got match=%q target=%q", match, target)
	}

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "approved" {
		t.Errorf("onboarding status: want approved, got %s", status)
	}

	var taskCount int
	db.QueryRow(`SELECT COUNT(*) FROM scheduled_tasks WHERE owner = 'myroom'`).Scan(&taskCount)
	if taskCount != len(defaultTasks) {
		t.Errorf("scheduled tasks: want %d, got %d", len(defaultTasks), taskCount)
	}
}

func TestApproveInTxNestedFolder(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:2', 'pending', '2026-01-01')`)

	if err := approveInTx(db, "telegram:2", "world/house/room"); err != nil {
		t.Fatalf("approveInTx: %v", err)
	}

	for _, want := range []string{"world", "world/house", "world/house/room"} {
		var got string
		db.QueryRow(`SELECT folder FROM groups WHERE folder = ?`, want).Scan(&got)
		if got != want {
			t.Errorf("missing group %q", want)
		}
	}

	var parent sql.NullString
	db.QueryRow(`SELECT parent FROM groups WHERE folder = 'world/house/room'`).Scan(&parent)
	if !parent.Valid || parent.String != "world/house" {
		t.Errorf("parent: got %v", parent)
	}
}

func TestApproveInTxIdempotentRoute(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:3', 'pending', '2026-01-01')`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=3', 'myroom')`)

	if err := approveInTx(db, "telegram:3", "myroom"); err != nil {
		t.Fatalf("approveInTx: %v", err)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'myroom'`).Scan(&n)
	if n != 1 {
		t.Errorf("route count: want 1, got %d", n)
	}
}

func TestHandleRejectUpdatesStatus(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO groups (folder, parent) VALUES ('main', NULL)`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'platform=telegram room=1', 'main')`)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:999', 'pending', '2026-01-01')`)

	cfg := config{}
	body, _ := json.Marshal(map[string]string{"jid": "telegram:1", "text": "/reject telegram:999"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:999'`).Scan(&status)
	if status != "rejected" {
		t.Errorf("want rejected, got %s", status)
	}
}

func TestHandleRejectNotTier0(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	body, _ := json.Marshal(map[string]string{"jid": "telegram:1", "text": "/reject telegram:999"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}

func TestRouteMatchesJIDLiteral(t *testing.T) {
	if !routeMatchesJID("platform=telegram room=123", "telegram", "123") {
		t.Error("literal match should succeed")
	}
	if routeMatchesJID("platform=telegram room=123", "telegram", "456") {
		t.Error("room mismatch should fail")
	}
	if routeMatchesJID("platform=telegram room=123", "whatsapp", "123") {
		t.Error("platform mismatch should fail")
	}
}

func TestRouteMatchesJIDWildcardRejected(t *testing.T) {
	if routeMatchesJID("room=1*", "telegram", "123") {
		t.Error("wildcard routes must not match")
	}
	if routeMatchesJID("room=?", "telegram", "1") {
		t.Error("wildcard ? must not match")
	}
}

func TestJidFromMatch(t *testing.T) {
	cases := []struct{ in, want string }{
		{"platform=telegram room=123", "telegram:123"},
		{"room=456", "456"},
		{"chat_jid=telegram:789", "telegram:789"},
		{"room=1*", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := jidFromMatch(c.in); got != c.want {
			t.Errorf("jidFromMatch(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRootJIDsDedup(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO groups (folder, parent) VALUES ('main', NULL)`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES
		(0, 'platform=telegram room=1', 'main'),
		(1, 'platform=telegram room=1', 'main'),
		(2, 'platform=telegram room=2', 'main')`)

	jids := rootJIDs(db)
	if len(jids) != 2 {
		t.Errorf("want 2 unique jids, got %d: %v", len(jids), jids)
	}
}

func TestHandleApproveTargetNotPending(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO groups (folder, parent) VALUES ('main', NULL)`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'platform=telegram room=1', 'main')`)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:999', 'approved', '2026-01-01')`)

	cfg := config{}
	body, _ := json.Marshal(map[string]string{
		"jid":  "telegram:1",
		"text": "/approve telegram:999 myroom",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleSend(w, req, db, cfg)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestSeedDefaultTasksIdempotent(t *testing.T) {
	db := testDB(t)
	tx, _ := db.Begin()
	seedDefaultTasksTx(tx, "myroom", "telegram:1")
	tx.Commit()

	tx2, _ := db.Begin()
	seedDefaultTasksTx(tx2, "myroom", "telegram:1")
	tx2.Commit()

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM scheduled_tasks WHERE owner = 'myroom'`).Scan(&n)
	if n != len(defaultTasks) {
		t.Errorf("after 2 seeds: want %d, got %d (INSERT OR IGNORE expected)", len(defaultTasks), n)
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
	db.Exec(`INSERT INTO onboarding (jid, status, token, created) VALUES ('telegram:1', 'awaiting_message', 'tok', '2026-01-01')`)

	linkJID(db, "tok", "telegram:1", "github:alice")

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
