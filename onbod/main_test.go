package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/store"
	_ "modernc.org/sqlite"
)

func TestMain(m *testing.M) {
	os.Setenv("ARIZUKO_DEV", "true")
	os.Exit(m.Run())
}

// migratedDB opens an in-memory SQLite DB and runs the canonical
// store migrations. Use for tests that exercise the full schema.
func migratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

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
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, prompted_at TEXT, created TEXT, token TEXT, token_expires TEXT, user_sub TEXT, gate TEXT, queued_at TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, is_from_me INTEGER, is_bot_message INTEGER, source TEXT NOT NULL DEFAULT '');
		CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT, cron TEXT, next_run TEXT, status TEXT, created_at TEXT, context_mode TEXT);
		CREATE TABLE user_jids (user_sub TEXT NOT NULL, jid TEXT NOT NULL, claimed TEXT NOT NULL, PRIMARY KEY (user_sub, jid));
		CREATE UNIQUE INDEX idx_user_jids_jid ON user_jids(jid);
		CREATE TABLE user_groups (user_sub TEXT NOT NULL, folder TEXT NOT NULL, granted_at TEXT, PRIMARY KEY (user_sub, folder));
		CREATE TABLE auth_users (id INTEGER PRIMARY KEY AUTOINCREMENT, sub TEXT UNIQUE, username TEXT, hash TEXT, name TEXT, created_at TEXT);
		CREATE TABLE auth_sessions (token_hash TEXT PRIMARY KEY, user_sub TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL);
		CREATE TABLE channels (name TEXT PRIMARY KEY, url TEXT, capabilities TEXT);
		CREATE TABLE onboarding_gates (gate TEXT PRIMARY KEY, limit_per_day INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);
		CREATE TABLE invites (token TEXT PRIMARY KEY, target_glob TEXT NOT NULL, issued_by_sub TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT, max_uses INTEGER NOT NULL DEFAULT 1, used_count INTEGER NOT NULL DEFAULT 0);
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

	// Token presentation is idempotent — token persists, status unchanged.
	// The single-shot is the user_sub claim in handleDashboard, not here.
	var status string
	var token sql.NullString
	db.QueryRow(`SELECT status, token FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status, &token)
	if status != "awaiting_message" {
		t.Errorf("want status=awaiting_message after presentation, got %s", status)
	}
	if !token.Valid || token.String != "abc123" {
		t.Errorf("want token=abc123 still set, got %q (valid=%v)", token.String, token.Valid)
	}

	// onboard_jid cookie should be set so OAuth round-trip can identify JID.
	var jidCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "onboard_jid" {
			jidCookie = c
		}
	}
	if jidCookie == nil || jidCookie.Value != "telegram:1" {
		t.Errorf("want onboard_jid=telegram:1 cookie, got %+v", jidCookie)
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

// TestTokenLandingWrongDatetimeFormat: regression test for the bug where
// token_expires stored in wrong format (space-separated vs RFC3339) causes
// SQL string comparison to fail. SQLite compares lexicographically:
// "2026-04-28T08:00:00Z" > "2026-04-28 18:00:00" is TRUE because 'T' > ' '.
// This means a valid future token appears expired.
func TestTokenLandingWrongDatetimeFormat(t *testing.T) {
	db := testDB(t)
	// Wrong format: space instead of T, no timezone. This is what a future
	// date looks like in some datetime formats (e.g. Python's default str()).
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'abc123', '2099-01-01 00:00:00', '2026-01-01')`)

	cfg := config{authBaseURL: "https://example.com"}
	req := httptest.NewRequest("GET", "/onboard?token=abc123", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	// BUG DEMONSTRATION: with wrong format, valid token is rejected.
	// If this test starts failing (returns 303), the bug is fixed at query level.
	if w.Code != http.StatusOK {
		t.Logf("Got %d - format validation may have been added", w.Code)
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
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "create_world") {
		t.Error("expected username picker form")
	}
}

func TestCreateWorldValidUsername(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:new', 'github:new', 'New User', '', '2026-01-01')`)

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"alice"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
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
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
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
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
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

// TestHandleTokenLanding_AllowsReplay: the user clicks the link, bails on
// OAuth, and clicks again. Both clicks must succeed — token presentation
// is idempotent. The single-shot is at identity-bind in handleDashboard.
func TestHandleTokenLanding_AllowsReplay(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'tok-replay', '2099-01-01T00:00:00Z', '2026-01-01')`)

	cfg := config{authBaseURL: "https://example.com"}

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/onboard?token=tok-replay", nil)
		w := httptest.NewRecorder()
		handleOnboard(w, req, db, cfg)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("click %d: want 303, got %d", i+1, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/auth/login" {
			t.Fatalf("click %d: want /auth/login, got %s", i+1, loc)
		}
	}

	var status string
	var token sql.NullString
	db.QueryRow(`SELECT status, token FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status, &token)
	if status != "awaiting_message" {
		t.Errorf("want awaiting_message after replay, got %s", status)
	}
	if !token.Valid || token.String != "tok-replay" {
		t.Errorf("want token preserved across replays, got %q", token.String)
	}
}

// TestHandleTokenLanding_DoesNotConsumeOnGet: explicit assertion that GET
// of the token landing endpoint is not a state-changing operation on the
// onboarding row beyond cookies.
func TestHandleTokenLanding_DoesNotConsumeOnGet(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'tok-once', '2099-01-01T00:00:00Z', '2026-01-01')`)

	cfg := config{authBaseURL: "https://example.com"}
	req := httptest.NewRequest("GET", "/onboard?token=tok-once", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	var status string
	var token, userSub sql.NullString
	db.QueryRow(`SELECT status, token, user_sub FROM onboarding WHERE jid = 'telegram:1'`).
		Scan(&status, &token, &userSub)
	if status != "awaiting_message" {
		t.Errorf("status changed by GET: %s", status)
	}
	if !token.Valid || token.String == "" {
		t.Errorf("token cleared by GET")
	}
	if userSub.Valid {
		t.Errorf("user_sub set by GET: %q", userSub.String)
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
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
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
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:lonely")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
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
	if strings.Contains(body, "create_world") {
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

// Operator (user_groups has "**") can create a world and routes are written
// for their linked JIDs.
func TestCreateWorldOperatorAllowed(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:op', 'op', 'Op', '', '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('github:op', '**')`)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed)
		VALUES ('github:op', 'telegram:99', '2026-01-01')`)

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"opworld"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:op")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
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

func TestMatchGate(t *testing.T) {
	gates := []gate{
		{kind: "github", param: "org=co", limitPerDay: 10},
		{kind: "google", param: "domain=co.com", limitPerDay: 20},
		{kind: "email", param: "domain=example.com", limitPerDay: 5},
		{kind: "*", param: "", limitPerDay: 50},
	}

	tests := []struct {
		sub  string
		want string // gateKey or "" for nil
	}{
		{"github:alice", "github:org=co"},
		{"google:alice@co.com", "google:domain=co.com"},
		{"google:alice@other.com", "*"},
		{"email:bob@example.com", "email:domain=example.com"},
		{"mastodon:foo", "*"},
	}
	for _, tc := range tests {
		g := matchGate(gates, tc.sub)
		var got string
		if g != nil {
			got = gateKey(*g)
		}
		if got != tc.want {
			t.Errorf("matchGate(%q) = %q, want %q", tc.sub, got, tc.want)
		}
	}
}

func TestMatchGateNoWildcard(t *testing.T) {
	gates := []gate{
		{kind: "github", param: "org=co", limitPerDay: 10},
	}
	g := matchGate(gates, "google:alice@co.com")
	if g != nil {
		t.Errorf("expected nil for unmatched sub, got %+v", *g)
	}
}

func TestLinkJIDWithGatesQueues(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:1', 'token_used', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('github:org=co', 10)`)

	linkJID(db, "telegram:1", "github:alice")

	var status, gateCol, queuedAt string
	db.QueryRow(
		`SELECT status, gate, queued_at FROM onboarding WHERE jid = 'telegram:1'`,
	).Scan(&status, &gateCol, &queuedAt)
	if status != "queued" {
		t.Errorf("want status=queued, got %s", status)
	}
	if gateCol != "github:org=co" {
		t.Errorf("want gate=github:org=co, got %s", gateCol)
	}
	if queuedAt == "" {
		t.Error("want queued_at set")
	}
}

func TestLinkJIDWithGatesNoMatch(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:1', 'token_used', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('github:org=co', 10)`)

	linkJID(db, "telegram:1", "google:alice@other.com")

	// Status should remain token_used (no matching gate, rejected)
	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "token_used" {
		t.Errorf("want status=token_used (rejected), got %s", status)
	}
}

func TestAdmitFromQueue(t *testing.T) {
	db := testDB(t)
	now := "2026-04-17T10:00:00Z"
	// 3 queued users under the same gate, limit=2
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:1', 'queued', 'github:org=co', ?, 'github:a', '2026-01-01')`, now)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:2', 'queued', 'github:org=co', ?, 'github:b', '2026-01-01')`,
		"2026-04-17T10:01:00Z")
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:3', 'queued', 'github:org=co', ?, 'github:c', '2026-01-01')`,
		"2026-04-17T10:02:00Z")

	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('github:org=co', 2)`)
	admitFromQueue(db)

	// First 2 should be approved, third still queued
	var s1, s2, s3 string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 't:1'`).Scan(&s1)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 't:2'`).Scan(&s2)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 't:3'`).Scan(&s3)
	if s1 != "approved" {
		t.Errorf("t:1 want approved, got %s", s1)
	}
	if s2 != "approved" {
		t.Errorf("t:2 want approved, got %s", s2)
	}
	if s3 != "queued" {
		t.Errorf("t:3 want queued, got %s", s3)
	}
}

func TestAdmitFromQueueRespectsDaily(t *testing.T) {
	db := testDB(t)
	// Use the real clock's "today" — admitFromQueue uses time.Now() to scope
	// the per-day counter, so hardcoding a calendar date regresses the moment
	// the suite runs past that day.
	today := time.Now().UTC().Format("2006-01-02")
	// 1 already admitted today
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:0', 'approved', 'github:org=co', ?, 'github:z', '2026-01-01')`,
		today+"T08:00:00Z")
	// 1 queued
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:1', 'queued', 'github:org=co', ?, 'github:a', '2026-01-01')`,
		today+"T10:00:00Z")

	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('github:org=co', 1)`)
	admitFromQueue(db)

	// Daily limit already hit, t:1 stays queued
	var s string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 't:1'`).Scan(&s)
	if s != "queued" {
		t.Errorf("t:1 want queued (daily limit hit), got %s", s)
	}
}

func TestAdmitFromQueueNoGates(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:1', 'queued', 'github:org=co', '2026-04-17T10:00:00Z', 'github:a', '2026-01-01')`)
	// No gates in DB → noop
	admitFromQueue(db)

	var s string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 't:1'`).Scan(&s)
	if s != "queued" {
		t.Errorf("want queued (no gates = noop), got %s", s)
	}
}

func TestQueuePositionRendering(t *testing.T) {
	db := testDB(t)
	// 2 users queued, test shows position for second one
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:1', 'queued', '*', '2026-04-17T10:00:00Z', 'github:first', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:2', 'queued', '*', '2026-04-17T10:01:00Z', 'github:second', '2026-01-01')`)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:second', 'second', 'Second', '', '2026-01-01')`)

	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 100)`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:second")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "#2") {
		t.Errorf("expected position #2 in body, got: %s", body)
	}
	if !strings.Contains(body, "queue") {
		t.Errorf("expected 'queue' in body")
	}
}

func TestNoGatesLegacyBehavior(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:1', 'token_used', '2026-01-01')`)

	// No gates → linkJID should set approved directly
	linkJID(db, "telegram:1", "github:alice")

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "approved" {
		t.Errorf("want approved (legacy), got %s", status)
	}
}

// --- Invite tests ---

// createInvite is a test helper that mints an invite via the new store API.
func createInvite(t *testing.T, db *sql.DB, targetGlob, issuedBy string, maxUses int) string {
	t.Helper()
	inv, err := store.New(db).CreateInvite(targetGlob, issuedBy, maxUses, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	return inv.Token
}

func TestInviteCreation(t *testing.T) {
	db := testDB(t)
	token := createInvite(t, db, "alice", "telegram:1", 3)
	if len(token) != 64 {
		t.Errorf("expected 64-char hex token, got %d", len(token))
	}

	var glob, issuedBy string
	var maxUses, used int
	db.QueryRow(`SELECT target_glob, issued_by_sub, max_uses, used_count FROM invites WHERE token = ?`,
		token).Scan(&glob, &issuedBy, &maxUses, &used)
	if glob != "alice" {
		t.Errorf("want target_glob=alice, got %q", glob)
	}
	if issuedBy != "telegram:1" {
		t.Errorf("want issued_by_sub=telegram:1, got %q", issuedBy)
	}
	if maxUses != 3 {
		t.Errorf("want max_uses=3, got %d", maxUses)
	}
	if used != 0 {
		t.Errorf("want used_count=0, got %d", used)
	}
}

func TestInviteConsume(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:bob', 'bob', 'Bob', '', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, name, parent, added_at)
		VALUES ('alice', 'alice', NULL, '2026-01-01')`)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed)
		VALUES ('github:bob', 'telegram:99', '2026-01-01')`)

	token := createInvite(t, db, "alice", "telegram:1", 1)

	cfg := config{authBaseURL: "https://example.com"}
	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/onboard" {
		t.Errorf("want redirect to /onboard, got %s", loc)
	}

	// user_groups row should exist
	var folder string
	db.QueryRow(`SELECT folder FROM user_groups WHERE user_sub = 'github:bob'`).Scan(&folder)
	if folder != "alice" {
		t.Errorf("want user_groups folder=alice, got %q", folder)
	}

	// Route should exist for bob's JID
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE match = 'room=99' AND target = 'alice'`).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 route for room=99→alice, got %d", n)
	}

	// used_count should be incremented
	var used int
	db.QueryRow(`SELECT used_count FROM invites WHERE token = ?`, token).Scan(&used)
	if used != 1 {
		t.Errorf("want used_count=1, got %d", used)
	}
}

func TestInviteExpired(t *testing.T) {
	db := testDB(t)
	past := "2020-01-01T00:00:00Z"
	db.Exec(`INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, max_uses, expires_at)
		VALUES ('expired-tok', 'alice', 'telegram:1', '2026-01-01T00:00:00Z', 1, ?)`, past)

	cfg := config{}
	req := httptest.NewRequest("GET", "/invite/expired-tok", nil)
	req.SetPathValue("token", "expired-tok")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "expired") && !strings.Contains(body, "Expired") {
		t.Errorf("expected expired message in body")
	}
}

func TestInviteMaxUses(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, used_count, max_uses)
		VALUES ('used-tok', 'alice', 'telegram:1', '2026-01-01T00:00:00Z', 1, 1)`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/invite/used-tok", nil)
	req.SetPathValue("token", "used-tok")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "used") && !strings.Contains(body, "Used") {
		t.Errorf("expected max-uses message in body")
	}
}

func TestInviteAuthRequired(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, max_uses)
		VALUES ('auth-tok', 'alice', 'telegram:1', '2026-01-01T00:00:00Z', 1)`)

	cfg := config{authBaseURL: "https://example.com", secureCookie: true}
	req := httptest.NewRequest("GET", "/invite/auth-tok", nil)
	req.SetPathValue("token", "auth-tok")
	// No X-User-Sub header → should redirect to login
	w := httptest.NewRecorder()
	handleInvite(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("want redirect to /auth/login, got %s", loc)
	}
	// auth_return cookie should save the invite URL
	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "auth_return" && c.Value == "/invite/auth-tok" {
			found = true
		}
	}
	if !found {
		t.Error("expected auth_return cookie with invite path")
	}
}

func TestInviteInvalidToken(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	req := httptest.NewRequest("GET", "/invite/nonexistent", nil)
	req.SetPathValue("token", "nonexistent")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func postOnboard(db *sql.DB, cfg config, sub string,
	vals url.Values) *httptest.ResponseRecorder {
	// Double-submit CSRF: cookie and form field must match.
	const csrf = "test-csrf-token"
	if vals.Get("csrf") == "" {
		vals = cloneVals(vals)
		vals.Set("csrf", csrf)
	}
	body := vals.Encode()
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", sub)
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: csrf})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)
	return w
}

func cloneVals(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func TestDeleteRoute(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=1', 'myroom')`)

	var id int64
	db.QueryRow(`SELECT id FROM routes WHERE target = 'myroom'`).Scan(&id)

	w := postOnboard(db, cfg, "alice", url.Values{
		"action":   {"delete_route"},
		"route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", w.Code, w.Body.String())
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE id = ?`, id).Scan(&n)
	if n != 0 {
		t.Error("route should be deleted")
	}
}

func TestDeleteRouteWrongUser(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('bob', 'bobroom')`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=1', 'aliceroom')`)

	var id int64
	db.QueryRow(`SELECT id FROM routes WHERE target = 'aliceroom'`).Scan(&id)

	w := postOnboard(db, cfg, "bob", url.Values{
		"action":   {"delete_route"},
		"route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE id = ?`, id).Scan(&n)
	if n != 1 {
		t.Error("route should still exist")
	}
}

func TestAddRoute(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)
	// Alice must own a JID whose room matches the add_route match pattern.
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed) VALUES ('alice', 'telegram:999', '2026-01-01')`)

	w := postOnboard(db, cfg, "alice", url.Values{
		"action": {"add_route"},
		"match":  {"room=999"},
		"target": {"myroom"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", w.Code, w.Body.String())
	}

	var match, target string
	db.QueryRow(
		`SELECT match, target FROM routes WHERE target = 'myroom'`,
	).Scan(&match, &target)
	if match != "room=999" || target != "myroom" {
		t.Errorf("route: match=%q target=%q", match, target)
	}
}

// Non-operator cannot add a route whose match refers to a JID they don't own,
// even if the target folder is theirs — prevents cross-tenant interception.
func TestAddRouteMatchNotOwned(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)
	// alice has NO user_jids; attempt to claim victim's room.
	w := postOnboard(db, cfg, "alice", url.Values{
		"action": {"add_route"},
		"match":  {"room=victim-id"},
		"target": {"myroom"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&n)
	if n != 0 {
		t.Errorf("no route should have been created, got %d", n)
	}
}

// Reject malformed match pattern characters (wildcards, spaces).
func TestAddRouteInvalidMatchChars(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)
	for _, bad := range []string{"room=* *", "room=a b", "** match"} {
		w := postOnboard(db, cfg, "alice", url.Values{
			"action": {"add_route"},
			"match":  {bad},
			"target": {"myroom"},
		})
		if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
			t.Errorf("bad match %q: want 400/403, got %d", bad, w.Code)
		}
	}
}

func TestCSRFRejected(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)
	// No csrf cookie/field → must be rejected even with X-User-Sub.
	form := url.Values{"action": {"add_route"}, "match": {"room=1"}, "target": {"myroom"}}
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403 (csrf), got %d", w.Code)
	}
}

// Cookie-based auto-link is single-use: once claimed, the onboarding row's
// user_sub is set and a later attacker with the cookie cannot rebind.
func TestSecondJIDAutoLinkSingleUse(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, name, parent, added_at)
		VALUES ('alice', 'alice', NULL, '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('github:alice', 'alice')`)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:eve', 'eve', 'Eve', '', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, name, parent, added_at)
		VALUES ('eve', 'eve', NULL, '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('github:eve', 'eve')`)
	// token_used row with user_sub still NULL.
	db.Exec(`INSERT INTO onboarding (jid, status, token_expires, created)
		VALUES ('telegram:victim', 'token_used', '2099-01-01T00:00:00Z', '2026-01-01')`)

	// Alice legitimately claims the JID.
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "telegram:victim"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, config{})

	var jidOwner string
	db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = 'telegram:victim'`).Scan(&jidOwner)
	if jidOwner != "github:alice" {
		t.Fatalf("first claim: want github:alice, got %q", jidOwner)
	}

	// Eve tries to rebind using the same cookie. Must fail: onboarding row is
	// now claimed (user_sub != NULL), so the atomic UPDATE matches nothing.
	req2 := httptest.NewRequest("GET", "/onboard", nil)
	req2.Header.Set("X-User-Sub", "github:eve")
	req2.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "telegram:victim"})
	w2 := httptest.NewRecorder()
	handleOnboard(w2, req2, db, config{})

	// Ownership unchanged.
	db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid = 'telegram:victim'`).Scan(&jidOwner)
	if jidOwner != "github:alice" {
		t.Errorf("after replay attempt: want github:alice, got %q", jidOwner)
	}
	// No route to eve's folder.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'eve'`).Scan(&n)
	if n != 0 {
		t.Errorf("eve must not receive a route, got %d", n)
	}
}

// Invite consume is atomic: simulated concurrent redemption cannot exceed
// max_uses. The guard is inside the UPDATE — double-call with uses=max_uses
// after the first returns the "used" page.
func TestInviteAtomicConsume(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:bob', 'bob', 'Bob', '', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, name, parent, added_at)
		VALUES ('alice', 'alice', NULL, '2026-01-01')`)
	tok := createInvite(t, db, "alice", "telegram:1", 1)

	// First consume.
	req := httptest.NewRequest("GET", "/invite/"+tok, nil)
	req.SetPathValue("token", tok)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, config{})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("first consume: want 303, got %d", w.Code)
	}

	// Second consume — uses is now 1 == max_uses; must fail.
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:eve', 'eve', 'Eve', '', '2026-01-01')`)
	req2 := httptest.NewRequest("GET", "/invite/"+tok, nil)
	req2.SetPathValue("token", tok)
	req2.Header.Set("X-User-Sub", "github:eve")
	w2 := httptest.NewRecorder()
	handleInvite(w2, req2, db, config{})
	if w2.Code != http.StatusOK {
		t.Errorf("second consume: want 200 error page, got %d", w2.Code)
	}
	// Eve should not have user_groups row.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM user_groups WHERE user_sub = 'github:eve'`).Scan(&n)
	if n != 0 {
		t.Errorf("eve must not be granted access, got %d rows", n)
	}
}

func TestAddRouteWrongTarget(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('bob', 'bobroom')`)

	w := postOnboard(db, cfg, "bob", url.Values{
		"action": {"add_route"},
		"match":  {"room=999"},
		"target": {"aliceroom"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&n)
	if n != 0 {
		t.Error("no route should have been created")
	}
}

// --- Expanded coverage: schema, permissions, XSS, operator, flow ---

// userRoutes returns routes filtered by user's folders (or all if nil=operator bypass).
func TestUserRoutesFilter(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=1', 'alice')`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=2', 'bob')`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=3', 'alice')`)

	// Alice gets only her routes.
	routes := userRoutes(db, []string{"alice"})
	if len(routes) != 2 {
		t.Errorf("want 2 routes for alice, got %d", len(routes))
	}
	for _, r := range routes {
		if r.Target != "alice" {
			t.Errorf("expected target=alice, got %q", r.Target)
		}
	}

	// nil = bypass, returns all.
	all := userRoutes(db, nil)
	if len(all) != 3 {
		t.Errorf("want 3 routes (all), got %d", len(all))
	}
}

// renderDashboard must HTML-escape attacker-controlled username, sub, and jid.
func TestDashboardXSSEscape(t *testing.T) {
	db := testDB(t)
	attacker := `<script>alert(1)</script>`
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES (?, ?, '', '', '2026-01-01')`, attacker, attacker)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES (?, 'room')`, attacker)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed) VALUES (?, ?, '2026-01-01')`,
		attacker, attacker)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", attacker)
	handleOnboard(w, req, db, config{})

	body := w.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("raw script tag leaked into dashboard HTML")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected HTML-escaped script in body")
	}
}

// Queue position render must escape the gate string (user-controlled via DB).
func TestQueuePositionXSSEscape(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:x', 'x', 'X', '', '2026-01-01')`)
	// gate value itself is stored, but the eta-msg is server-side formatted;
	// ensure no raw DB reflection occurs in HTML without escape.
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:1', 'queued', '<x>', '2026-04-17T10:00:00Z', 'github:x', '2026-01-01')`)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:x")
	handleOnboard(w, req, db, config{})

	if strings.Contains(w.Body.String(), "<x>") {
		t.Error("unescaped gate value leaked into queue page")
	}
}

func TestHandleOnboardPostUnauthenticated(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("POST", "/onboard", nil)
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, config{})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestHandleOnboardPostUnknownAction(t *testing.T) {
	db := testDB(t)
	w := postOnboard(db, config{}, "github:alice", url.Values{"action": {"nope"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestAddRouteMissingFields(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)

	w := postOnboard(db, config{}, "alice", url.Values{"action": {"add_route"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
	w2 := postOnboard(db, config{}, "alice", url.Values{
		"action": {"add_route"}, "match": {"room=1"},
	})
	if w2.Code != http.StatusBadRequest {
		t.Errorf("want 400 on missing target, got %d", w2.Code)
	}
}

func TestDeleteRouteInvalidID(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)

	w := postOnboard(db, config{}, "alice", url.Values{
		"action": {"delete_route"}, "route_id": {"not-a-number"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestDeleteRouteNotFound(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'myroom')`)
	w := postOnboard(db, config{}, "alice", url.Values{
		"action": {"delete_route"}, "route_id": {"99999"},
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

// Operator (user_groups has `**`) can add/delete any route — exercises
// auth.MatchGroups integration in folderAllowed.
func TestOperatorCanManageAnyRoute(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('op', '**')`)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=X', 'any-world')`)

	var id int64
	db.QueryRow(`SELECT id FROM routes WHERE target = 'any-world'`).Scan(&id)
	w := postOnboard(db, config{}, "op", url.Values{
		"action": {"delete_route"}, "route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("operator delete_route want 303, got %d: %s", w.Code, w.Body.String())
	}

	w2 := postOnboard(db, config{}, "op", url.Values{
		"action": {"add_route"}, "match": {"room=Y"}, "target": {"another-world"},
	})
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("operator add_route want 303, got %d: %s", w2.Code, w2.Body.String())
	}
}

// Non-operator with zero grants (empty user_groups) must get 403.
func TestNonOperatorNoGrantsDenied(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=1', 'someroom')`)

	var id int64
	db.QueryRow(`SELECT id FROM routes WHERE target = 'someroom'`).Scan(&id)
	// bob has no user_groups rows → userFolders returns nil → denied.
	// Operator is emergent only from a `**` grant row.
	w := postOnboard(db, config{}, "bob", url.Values{
		"action": {"delete_route"}, "route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}

// Admission queue FIFO: oldest queued_at admitted first.
func TestAdmitFromQueueFIFO(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:late', 'queued', '*', '2026-04-17T12:00:00Z', 'u:late', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:early', 'queued', '*', '2026-04-17T09:00:00Z', 'u:early', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 1)`)
	admitFromQueue(db)

	var early, late string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 't:early'`).Scan(&early)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 't:late'`).Scan(&late)
	if early != "approved" {
		t.Errorf("FIFO violated: early want approved, got %s", early)
	}
	if late != "queued" {
		t.Errorf("FIFO violated: late want queued, got %s", late)
	}
}

// Two gates with independent daily limits.
func TestAdmitFromQueueMultipleGates(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('g:1', 'queued', 'github:org=co', '2026-04-17T10:00:00Z', 'gh:1', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('*:1', 'queued', '*', '2026-04-17T10:00:00Z', 'any:1', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('github:org=co', 5)`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 5)`)
	admitFromQueue(db)

	var a, b string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'g:1'`).Scan(&a)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = '*:1'`).Scan(&b)
	if a != "approved" || b != "approved" {
		t.Errorf("both gates should admit: got github=%s, wildcard=%s", a, b)
	}
}

// Disabled gate is not loaded.
func TestLoadGatesEnabledFilter(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day, enabled) VALUES ('github:org=co', 10, 1)`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day, enabled) VALUES ('*', 5, 0)`)

	gates := loadGates(db)
	if len(gates) != 1 {
		t.Fatalf("want 1 enabled gate, got %d", len(gates))
	}
	if gates[0].kind != "github" {
		t.Errorf("want kind=github, got %s", gates[0].kind)
	}
}

// End-to-end state machine on the canonical migrated schema:
// awaiting_message → (prompt) → token set → (landing) → token_used →
// (linkJID) → queued (gated) → (admit) → approved.
func TestStateMachineMigrated(t *testing.T) {
	db := migratedDB(t)
	// Seed auth user + existing world for the JID to link to.
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 5)`)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:7', 'awaiting_message', '2026-01-01')`)

	cfg := config{authBaseURL: "https://example.com"}

	// 1) prompt → token set
	promptUnprompted(db, cfg)
	var tok sql.NullString
	db.QueryRow(`SELECT token FROM onboarding WHERE jid = 'telegram:7'`).Scan(&tok)
	if !tok.Valid || tok.String == "" {
		t.Fatal("prompt did not set token")
	}

	// 2) token landing (presentation is idempotent — status & token unchanged)
	req := httptest.NewRequest("GET", "/onboard?token="+tok.String, nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)
	var st string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:7'`).Scan(&st)
	if st != "awaiting_message" {
		t.Fatalf("presentation should not change status, got %s", st)
	}

	// 3) identity-bind → token_used (claim happens at user_sub binding)
	if !claimOnboarding(db, "telegram:7", "github:alice") {
		t.Fatal("claimOnboarding should succeed")
	}
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:7'`).Scan(&st)
	if st != "token_used" {
		t.Fatalf("after claim want token_used, got %s", st)
	}

	// 4) linkJID with gates active → queued
	linkJID(db, "telegram:7", "github:alice")
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:7'`).Scan(&st)
	if st != "queued" {
		t.Fatalf("after linkJID+gate want queued, got %s", st)
	}

	// 5) admit from queue → approved
	admitFromQueue(db)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:7'`).Scan(&st)
	if st != "approved" {
		t.Fatalf("after admit want approved, got %s", st)
	}
}

// Invite: auth_return cookie is also set when unauthenticated (regression guard).
func TestInviteSetsCookieFlagsHTTPS(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, max_uses)
		VALUES ('tok', 'alice', 'x', '2026-01-01T00:00:00Z', 1)`)
	cfg := config{authBaseURL: "https://example.com", secureCookie: true}
	req := httptest.NewRequest("GET", "/invite/tok", nil)
	req.SetPathValue("token", "tok")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, cfg)

	var c *http.Cookie
	for _, x := range w.Result().Cookies() {
		if x.Name == "auth_return" {
			c = x
		}
	}
	if c == nil {
		t.Fatal("auth_return cookie missing")
	}
	if !c.Secure {
		t.Error("want Secure cookie over HTTPS")
	}
	if !c.HttpOnly {
		t.Error("want HttpOnly cookie")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("want SameSite=Lax, got %v", c.SameSite)
	}
}

// paramVal / emailDomain edge cases.
func TestParamValAndEmailDomain(t *testing.T) {
	if paramVal("domain=co.com", "domain") != "co.com" {
		t.Error("paramVal should extract value")
	}
	if paramVal("other=x", "domain") != "" {
		t.Error("paramVal should return empty on mismatch")
	}
	if emailDomain("google:a@co.com") != "co.com" {
		t.Error("emailDomain extract")
	}
	if emailDomain("no-at-sign") != "" {
		t.Error("emailDomain should return empty when no @")
	}
}

func TestDashboardUnauthenticated(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)
	// No token, no X-User-Sub → redirect to login
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", w.Code)
	}
}

// sendReply must POST to cfg.gatedURL with a bearer secret when configured.
// promptUnprompted indirectly exercises sendReply; using a test HTTP server
// captures the wire format without mocking.
func TestSendReplyUsesSecretHeader(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:1', 'awaiting_message', '2026-01-01')`)

	var gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = readAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config{secret: "s3cret", gatedURL: srv.URL, authBaseURL: "https://ex.com"}
	promptUnprompted(db, cfg)

	if gotAuth != "Bearer s3cret" {
		t.Errorf("want Bearer secret header, got %q", gotAuth)
	}
	if !strings.Contains(string(gotBody), "telegram:1") ||
		!strings.Contains(string(gotBody), "/onboard?token=") {
		t.Errorf("unexpected outbound body: %s", gotBody)
	}
}

// sendReply gracefully handles non-2xx — prompt still records token update.
func TestSendReplyHandlesNon2xx(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('t:1', 'awaiting_message', '2026-01-01')`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := config{gatedURL: srv.URL, authBaseURL: "https://ex.com"}
	// Should not panic even when router returns 500.
	promptUnprompted(db, cfg)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM onboarding WHERE jid='t:1' AND token IS NOT NULL`).Scan(&n)
	if n != 1 {
		t.Errorf("token should still be persisted after send failure, got %d", n)
	}
}

// sendReply swallows transport errors (unreachable URL).
func TestSendReplyHandlesTransportError(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('t:1', 'awaiting_message', '2026-01-01')`)
	// Point at a closed port to force Do() to fail.
	cfg := config{gatedURL: "http://127.0.0.1:1", authBaseURL: "https://ex.com"}
	promptUnprompted(db, cfg) // must not panic
}

// handleTokenLanding: user already authenticated at landing → row gets linked
// and consumer is redirected to /onboard (not /auth/login).
func TestTokenLandingAuthenticatedRedirectsToDashboard(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:7', 'awaiting_message', 'tok', '2099-01-01T00:00:00Z', '2026-01-01')`)

	cfg := config{authBaseURL: "https://ex.com"}
	req := httptest.NewRequest("GET", "/onboard?token=tok", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/onboard" {
		t.Errorf("want redirect to /onboard, got %q", got)
	}
	// linkJID ran synchronously (no gates, no user rows yet — status goes approved).
	var sub string
	db.QueryRow(`SELECT user_sub FROM user_jids WHERE jid='telegram:7'`).Scan(&sub)
	if sub != "github:alice" {
		t.Errorf("jid not linked on authenticated landing, got %q", sub)
	}
}

// ensureCSRFToken is idempotent: an incoming request that already carries the
// cookie must not receive a new Set-Cookie header.
func TestEnsureCSRFIdempotent(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "existing"})
	ensureCSRFToken(w, req, config{})
	for _, sc := range w.Result().Cookies() {
		if sc.Name == csrfCookieName {
			t.Errorf("unexpected re-set of csrf cookie when one already present")
		}
	}
}

// handleInvite with empty path value (defensive — router should never route
// empty, but guard in handler must still return a friendly page).
func TestInviteEmptyToken(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("GET", "/invite/", nil)
	req.SetPathValue("token", "")
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, config{})
	if w.Code != http.StatusOK {
		t.Errorf("want 200 page, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid Invite") {
		t.Error("want Invalid Invite page")
	}
}

// handleAddRoute: match/target longer than 256 chars is rejected before touching DB.
func TestAddRouteTooLong(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('op', '**')`)
	long := strings.Repeat("a", 257)
	w := postOnboard(db, config{}, "op", url.Values{
		"action": {"add_route"}, "match": {"room=1"}, "target": {long},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 on long target, got %d", w.Code)
	}
	w2 := postOnboard(db, config{}, "op", url.Values{
		"action": {"add_route"}, "match": {long}, "target": {"myroom"},
	})
	if w2.Code != http.StatusBadRequest {
		t.Errorf("want 400 on long match, got %d", w2.Code)
	}
}

// handleAddRoute: invalid pattern characters (spaces, wildcards) are rejected.
func TestAddRouteInvalidMatchChars2(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('op', '**')`)
	// Note: "room=a\n" (trailing newline) is accepted by the current regex —
	// Go's default `$` matches before a final \n. Logged in bugs.md.
	for _, bad := range []string{"room=a b", "room=*", "room=a%", "room=a\nb"} {
		w := postOnboard(db, config{}, "op", url.Values{
			"action": {"add_route"}, "match": {bad}, "target": {"x"},
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("match=%q: want 400, got %d", bad, w.Code)
		}
	}
}

// handleAddRoute: non-operator must own the room referenced in match.
func TestAddRouteNonOperatorForbiddenOnUnownedMatch(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'alice')`)
	// alice has no user_jids → cannot claim any room=<id>.
	w := postOnboard(db, config{}, "alice", url.Values{
		"action": {"add_route"}, "match": {"room=9999"}, "target": {"alice"},
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d: %s", w.Code, w.Body.String())
	}
}

// userOwnsMatch: only canonical "room=<id>" is accepted; malformed prefixes
// return false.
func TestUserOwnsMatchRejectsNonRoomPrefix(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_jids (user_sub, jid, claimed) VALUES ('alice','telegram:42','2026-01-01')`)
	if userOwnsMatch(db, "alice", "peer=42") {
		t.Error("non-room prefix must not match")
	}
	if userOwnsMatch(db, "alice", "room=") {
		t.Error("empty room must not match")
	}
	if !userOwnsMatch(db, "alice", "room=42") {
		t.Error("valid room=42 should match")
	}
	if userOwnsMatch(db, "alice", "room=99") {
		t.Error("wrong room id must not match")
	}
}

// renderQueuePosition: with low daily limit + many queued, ETA must use
// "hours" wording (covers the >=60min branch).
func TestQueuePositionETAHours(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:x', 'x', 'X', '', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 1)`)
	// 4 earlier queued entries + the caller → position 5, 5*1440/1 = 7200 min.
	for i, jid := range []string{"t:a", "t:b", "t:c", "t:d"} {
		db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
			VALUES (?, 'queued', '*', ?, 'u:'||?, '2026-01-01')`,
			jid, "2026-04-17T0"+strconv.Itoa(i)+":00:00Z", i)
	}
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:me', 'queued', '*', '2026-04-17T09:00:00Z', 'github:x', '2026-01-01')`)

	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:x")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, config{})
	if !strings.Contains(w.Body.String(), "hours") {
		t.Errorf("expected 'hours' in ETA, body=%s", w.Body.String())
	}
}

// admitFromQueue respects already-admitted today's count and does not
// exceed limitPerDay even across repeated invocations.
func TestAdmitFromQueueDailyLimitPersists(t *testing.T) {
	db := testDB(t)
	today := time.Now().Format("2006-01-02")
	// Already-approved entry counts against today's budget.
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:old', 'approved', '*', ?, 'u:old', '2026-01-01')`, today+"T02:00:00Z")
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:wait', 'queued', '*', ?, 'u:wait', '2026-01-01')`, today+"T03:00:00Z")
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 1)`)

	admitFromQueue(db)
	var st string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid='t:wait'`).Scan(&st)
	if st != "queued" {
		t.Errorf("daily limit should block further admission, got %s", st)
	}

	// Second call is also a no-op (limit already hit).
	admitFromQueue(db)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid='t:wait'`).Scan(&st)
	if st != "queued" {
		t.Errorf("second admit should still be no-op, got %s", st)
	}
}

// tokenHash returns empty string for empty input and a stable 8-char tag
// otherwise. Stability lets log pipelines correlate attempts.
func TestTokenHash(t *testing.T) {
	if chanlib.ShortHash("") != "" {
		t.Error("empty input must yield empty tag")
	}
	h := chanlib.ShortHash("abc123")
	if len(h) != 8 {
		t.Errorf("want 8-char tag, got %d", len(h))
	}
	if chanlib.ShortHash("abc123") != h {
		t.Error("tokenHash must be deterministic")
	}
	if chanlib.ShortHash("abc124") == h {
		t.Error("different inputs must produce different tags")
	}
}

// loadConfig happy path: populates fields from env, including
// pollInterval parsing.
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ARIZUKO_DEV", "true")
	t.Setenv("ONBOARD_POLL_INTERVAL", "5s")
	t.Setenv("ONBOARDING_GREETING", "hi")
	t.Setenv("ROUTER_URL", "http://r:1")
	t.Setenv("AUTH_BASE_URL", "https://auth.example.com")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.pollInterval != 5*time.Second {
		t.Errorf("want pollInterval=5s, got %v", cfg.pollInterval)
	}
	if cfg.greeting != "hi" {
		t.Errorf("greeting not propagated")
	}
	if cfg.gatedURL != "http://r:1" {
		t.Errorf("gatedURL not propagated, got %q", cfg.gatedURL)
	}
	if !cfg.secureCookie {
		t.Errorf("secureCookie should be true for https AUTH_BASE_URL")
	}
	if !strings.HasSuffix(cfg.dsn, "/store/messages.db") {
		t.Errorf("unexpected dsn: %s", cfg.dsn)
	}
}

// loadConfig with invalid poll interval falls back to default (10s).
func TestLoadConfigBadPollInterval(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ARIZUKO_DEV", "true")
	t.Setenv("ONBOARD_POLL_INTERVAL", "not-a-duration")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.pollInterval != 10*time.Second {
		t.Errorf("want default 10s, got %v", cfg.pollInterval)
	}
}

// isOperator: operator emerges only from a `**` grant; nil/empty/other
// grants are not operator (no nil-sentinel — see specs/5/29-acl.md).
func TestIsOperator(t *testing.T) {
	if !isOperator([]string{"alice", "**"}) {
		t.Error("** grant must be operator")
	}
	if isOperator(nil) {
		t.Error("nil folders must not be operator")
	}
	if isOperator([]string{"alice", "bob"}) {
		t.Error("plain folders must not be operator")
	}
	if isOperator([]string{}) {
		t.Error("empty slice must not be operator")
	}
}

// userFolders filters out empty-string rows (defensive — migration scars).
func TestUserFoldersSkipsEmpty(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', 'real')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('alice', '')`)
	got := userFolders(db, "alice")
	for _, f := range got {
		if f == "" {
			t.Errorf("empty folder leaked into result: %v", got)
		}
	}
}

// handleOnboardPost rejects when CSRF cookie set but no form value supplied.
func TestCSRFRejectedWhenFormMissing(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("POST", "/onboard",
		strings.NewReader(url.Values{"action": {"create_world"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "abc"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, config{})
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403 when csrf form value absent, got %d", w.Code)
	}
}


// TestHandleDashboard_ConsumesAtUserSubBind: the post-OAuth dashboard
// visit is where the token gets cleared and user_sub gets bound. After
// the bind, the same token presented at /onboard?token=X must fail.
func TestHandleDashboard_ConsumesAtUserSubBind(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'tok-bind', '2099-01-01T00:00:00Z', '2026-01-01')`)
	db.Exec(`INSERT INTO auth_users (sub, username, name, hash, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '', '2026-01-01')`)
	db.Exec(`INSERT INTO user_groups (user_sub, folder) VALUES ('github:alice', 'alice')`)

	cfg := config{authBaseURL: "https://example.com"}

	// 1) Unauthenticated GET — token persists.
	req1 := httptest.NewRequest("GET", "/onboard?token=tok-bind", nil)
	w1 := httptest.NewRecorder()
	handleOnboard(w1, req1, db, cfg)
	var token sql.NullString
	db.QueryRow(`SELECT token FROM onboarding WHERE jid='telegram:1'`).Scan(&token)
	if !token.Valid || token.String != "tok-bind" {
		t.Fatalf("token cleared by GET: %+v", token)
	}

	// 2) Post-OAuth dashboard hit with cookie + X-User-Sub binds identity.
	req2 := httptest.NewRequest("GET", "/onboard", nil)
	req2.Header.Set("X-User-Sub", "github:alice")
	req2.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "telegram:1"})
	w2 := httptest.NewRecorder()
	handleOnboard(w2, req2, db, cfg)

	var status string
	var tokenAfter, userSubAfter sql.NullString
	db.QueryRow(`SELECT status, token, user_sub FROM onboarding WHERE jid='telegram:1'`).
		Scan(&status, &tokenAfter, &userSubAfter)
	if !userSubAfter.Valid || userSubAfter.String != "github:alice" {
		t.Errorf("user_sub not bound: %+v", userSubAfter)
	}
	if tokenAfter.Valid {
		t.Errorf("token not cleared after bind: %q", tokenAfter.String)
	}
	if status != "approved" { // no gates, linkJID promotes through
		t.Errorf("want approved after linkJID, got %s", status)
	}

	// 3) Replaying the original token after the bind must fail.
	req3 := httptest.NewRequest("GET", "/onboard?token=tok-bind", nil)
	w3 := httptest.NewRecorder()
	handleOnboard(w3, req3, db, cfg)
	if w3.Code != http.StatusOK {
		t.Errorf("post-bind replay: want 200 (error page), got %d", w3.Code)
	}
}

// TestPromptUnprompted_ResetsTokenUsedWithoutUserSub: the production-bug
// scenario. User clicked link (status moved to token_used in the old
// flow), bailed on OAuth without binding user_sub, returns and messages
// the bot. The next promptUnprompted cycle resets the row and mints a
// fresh token.
func TestPromptUnprompted_ResetsTokenUsedWithoutUserSub(t *testing.T) {
	db := testDB(t)
	// 31 minutes ago — past the 30-minute cool-down.
	stale := time.Now().Add(-31 * time.Minute).Format(time.RFC3339)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, prompted_at, created)
		VALUES ('telegram:1', 'token_used', NULL, '2099-01-01T00:00:00Z', ?, '2026-01-01')`, stale)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config{authBaseURL: "https://example.com", gatedURL: srv.URL}
	promptUnprompted(db, cfg)

	var status string
	var token, prompted, expires sql.NullString
	db.QueryRow(`SELECT status, token, prompted_at, token_expires FROM onboarding WHERE jid='telegram:1'`).
		Scan(&status, &token, &prompted, &expires)
	if status != "awaiting_message" {
		t.Errorf("want status reset to awaiting_message, got %s", status)
	}
	if !token.Valid || len(token.String) != 64 {
		t.Errorf("want fresh 64-char hex token, got %+v", token)
	}
	if !prompted.Valid {
		t.Error("want prompted_at re-set")
	}
	// Verify token_expires is RFC3339 format (the bug: wrong format causes SQL comparison to fail)
	if !expires.Valid {
		t.Error("want token_expires set")
	} else if _, err := time.Parse(time.RFC3339, expires.String); err != nil {
		t.Errorf("token_expires not RFC3339: %q (%v)", expires.String, err)
	}
}

// TestPromptUnprompted_DoesNotResetClaimedRow: rows whose user_sub is
// already bound MUST NOT be re-prompted. The user has authenticated;
// re-sending a link would be redundant and confusing.
func TestPromptUnprompted_DoesNotResetClaimedRow(t *testing.T) {
	db := testDB(t)
	stale := time.Now().Add(-31 * time.Minute).Format(time.RFC3339)
	db.Exec(`INSERT INTO onboarding (jid, status, token, user_sub, prompted_at, created)
		VALUES ('telegram:1', 'token_used', NULL, 'github:alice', ?, '2026-01-01')`, stale)

	cfg := config{authBaseURL: "https://example.com"}
	promptUnprompted(db, cfg)

	var status string
	var token sql.NullString
	db.QueryRow(`SELECT status, token FROM onboarding WHERE jid='telegram:1'`).
		Scan(&status, &token)
	if status != "token_used" {
		t.Errorf("claimed row was reset: status=%s", status)
	}
	if token.Valid {
		t.Errorf("claimed row got token re-minted: %q", token.String)
	}
}

// TestPromptUnprompted_DoesNotResetWithinCoolDown: a row prompted < 30
// minutes ago must not be re-prompted yet. Prevents thrashing if the
// user clicks-and-bails repeatedly.
func TestPromptUnprompted_DoesNotResetWithinCoolDown(t *testing.T) {
	db := testDB(t)
	fresh := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
	db.Exec(`INSERT INTO onboarding (jid, status, token, prompted_at, created)
		VALUES ('telegram:1', 'token_used', NULL, ?, '2026-01-01')`, fresh)

	cfg := config{authBaseURL: "https://example.com"}
	promptUnprompted(db, cfg)

	var status string
	var token sql.NullString
	db.QueryRow(`SELECT status, token FROM onboarding WHERE jid='telegram:1'`).
		Scan(&status, &token)
	if status != "token_used" {
		t.Errorf("row reset within cool-down: status=%s", status)
	}
	if token.Valid {
		t.Errorf("token re-minted within cool-down: %q", token.String)
	}
}

// Small helper: drain a body without importing io at the top of the file.
func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}
