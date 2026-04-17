package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, prompted_at TEXT, created TEXT, token TEXT, token_expires TEXT, user_sub TEXT, gate TEXT, queued_at TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, is_from_me INTEGER, is_bot_message INTEGER, source TEXT NOT NULL DEFAULT '');
		CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT, cron TEXT, next_run TEXT, status TEXT, created_at TEXT, context_mode TEXT);
		CREATE TABLE user_jids (user_sub TEXT NOT NULL, jid TEXT NOT NULL, claimed TEXT NOT NULL, PRIMARY KEY (user_sub, jid));
		CREATE UNIQUE INDEX idx_user_jids_jid ON user_jids(jid);
		CREATE TABLE user_groups (user_sub TEXT NOT NULL, folder TEXT NOT NULL, PRIMARY KEY (user_sub, folder));
		CREATE TABLE auth_users (id INTEGER PRIMARY KEY AUTOINCREMENT, sub TEXT UNIQUE, username TEXT, hash TEXT, name TEXT, created_at TEXT);
		CREATE TABLE auth_sessions (token_hash TEXT PRIMARY KEY, user_sub TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL);
		CREATE TABLE channels (name TEXT PRIMARY KEY, url TEXT, capabilities TEXT);
		CREATE TABLE onboarding_gates (gate TEXT PRIMARY KEY, limit_per_day INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);
		CREATE TABLE invitations (token TEXT PRIMARY KEY, folder TEXT NOT NULL, created_by TEXT NOT NULL, created_at TEXT NOT NULL, uses INTEGER NOT NULL DEFAULT 0, max_uses INTEGER NOT NULL DEFAULT 1, expires TEXT);
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

// --- Gate parsing tests ---

func TestParseGates(t *testing.T) {
	tests := []struct {
		input string
		want  []gate
	}{
		{"github:org=mycompany:10/day", []gate{
			{kind: "github", param: "org=mycompany", limitPerDay: 10},
		}},
		{"*:50/day", []gate{
			{kind: "*", param: "", limitPerDay: 50},
		}},
		{"github:org=co:10/day,google:domain=co.com:20/day,*:5/day", []gate{
			{kind: "github", param: "org=co", limitPerDay: 10},
			{kind: "google", param: "domain=co.com", limitPerDay: 20},
			{kind: "*", param: "", limitPerDay: 5},
		}},
		{"email:domain=example.com:5/day", []gate{
			{kind: "email", param: "domain=example.com", limitPerDay: 5},
		}},
	}
	for _, tc := range tests {
		got, err := parseGates(tc.input)
		if err != nil {
			t.Errorf("parseGates(%q) error: %v", tc.input, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("parseGates(%q) = %d gates, want %d", tc.input, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseGates(%q)[%d] = %+v, want %+v", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestParseGatesInvalid(t *testing.T) {
	bad := []string{"", "github", "github:org=co:0/day", "github:org=co:-1/day"}
	for _, s := range bad {
		got, err := parseGates(s)
		if err == nil && len(got) > 0 {
			t.Errorf("parseGates(%q) should fail, got %+v", s, got)
		}
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
	today := "2026-04-17"
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

// --- Invitation tests ---

func TestInviteCreation(t *testing.T) {
	db := testDB(t)
	token := createInvitation(db, "alice", "telegram:1", 3)
	if len(token) != 64 {
		t.Errorf("expected 64-char hex token, got %d", len(token))
	}

	var folder, createdBy string
	var maxUses, uses int
	db.QueryRow(`SELECT folder, created_by, max_uses, uses FROM invitations WHERE token = ?`,
		token).Scan(&folder, &createdBy, &maxUses, &uses)
	if folder != "alice" {
		t.Errorf("want folder=alice, got %q", folder)
	}
	if createdBy != "telegram:1" {
		t.Errorf("want created_by=telegram:1, got %q", createdBy)
	}
	if maxUses != 3 {
		t.Errorf("want max_uses=3, got %d", maxUses)
	}
	if uses != 0 {
		t.Errorf("want uses=0, got %d", uses)
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

	token := createInvitation(db, "alice", "telegram:1", 1)

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

	// Uses should be incremented
	var uses int
	db.QueryRow(`SELECT uses FROM invitations WHERE token = ?`, token).Scan(&uses)
	if uses != 1 {
		t.Errorf("want uses=1, got %d", uses)
	}
}

func TestInviteExpired(t *testing.T) {
	db := testDB(t)
	past := "2020-01-01T00:00:00Z"
	db.Exec(`INSERT INTO invitations (token, folder, created_by, created_at, max_uses, expires)
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
	db.Exec(`INSERT INTO invitations (token, folder, created_by, created_at, uses, max_uses)
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
	db.Exec(`INSERT INTO invitations (token, folder, created_by, created_at, max_uses)
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
	body := vals.Encode()
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", sub)
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, cfg)
	return w
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
