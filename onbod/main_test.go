package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

func TestMain(m *testing.M) {
	os.Setenv("ARIZUKO_DEV", "true")
	os.Setenv("HOST_APP_DIR", "/tmp/test-app")
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
	// A second pooled connection to ":memory:" is a DIFFERENT, empty database,
	// so anything doing two queries on this handle sees no fixtures. Pin to one.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE routes (id INTEGER PRIMARY KEY AUTOINCREMENT, seq INTEGER, match TEXT, target TEXT, observe_window_messages INTEGER, observe_window_chars INTEGER, added_by TEXT, added_via TEXT);
		CREATE TABLE groups (folder TEXT PRIMARY KEY, parent TEXT, name TEXT, added_at TEXT, slink_token TEXT, product TEXT);
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, prompted_at TEXT, created TEXT, token TEXT, token_expires TEXT, user_sub TEXT, gate TEXT, queued_at TEXT, admitted_at TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, is_from_me INTEGER, is_bot_message INTEGER, source TEXT NOT NULL DEFAULT '');
		CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT, cron TEXT, next_run TEXT, status TEXT, created_at TEXT, context_mode TEXT);
		CREATE TABLE acl (principal TEXT NOT NULL, action TEXT NOT NULL, scope TEXT NOT NULL, effect TEXT NOT NULL DEFAULT 'allow', params TEXT NOT NULL DEFAULT '', predicate TEXT NOT NULL DEFAULT '', granted_by TEXT, granted_at TEXT NOT NULL, grant_option INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (principal, action, scope, params, predicate, effect));
		CREATE TABLE acl_membership (child TEXT NOT NULL, parent TEXT NOT NULL, added_by TEXT, added_at TEXT NOT NULL, PRIMARY KEY (child, parent));
		CREATE TABLE user_profiles (id INTEGER PRIMARY KEY AUTOINCREMENT, sub TEXT UNIQUE, username TEXT, name TEXT, created_at TEXT);
		CREATE TABLE channels (name TEXT PRIMARY KEY, url TEXT, capabilities TEXT);
		CREATE TABLE onboarding_gates (gate TEXT PRIMARY KEY, limit_per_day INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);
		CREATE TABLE invites (ref TEXT PRIMARY KEY, target_glob TEXT NOT NULL, issued_by_sub TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT, max_uses INTEGER NOT NULL DEFAULT 1, used_count INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')), category TEXT NOT NULL, action TEXT NOT NULL, actor TEXT NOT NULL, actor_sub TEXT, resource TEXT, scope TEXT, surface TEXT, params_summary TEXT, outcome TEXT NOT NULL, error_msg TEXT, duration_ms INTEGER, turn_id TEXT, folder TEXT, instance TEXT, request_id TEXT, source_ip TEXT);
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
	handleOnboard(w, req, db, db, cfg)

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
	handleOnboard(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestTokenLandingInvalid(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard?token=nonexistent", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestDashboardLinksJID(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token_expires, created)
		VALUES ('telegram:1', 'token_used', '2099-01-01T00:00:00Z', '2026-01-01')`)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('github:alice', 'admin', 'alice', 'allow', '2026-01-01')`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "telegram:1"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, cfg)

	// JID should be linked
	var userSub string
	db.QueryRow(`SELECT parent FROM acl_membership WHERE child = 'telegram:1'`).Scan(&userSub)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:new', 'github:new', 'New User', '2026-01-01')`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "create_world") {
		t.Error("expected username picker form")
	}
}

// The rendered form and the handler's CSRF check must be tested against each
// other, not assumed to agree: a browser submits exactly the fields the
// renderer emitted, so this replays those (plus the cookies the GET set).
// postOnboard injects the double-submit pair itself, so no test built on it can
// catch a form that omits the field — which is how BUGS F1 shipped.
func TestUsernamePickerFormSatisfiesCSRFCheck(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:new', 'newbie', 'New User', '2026-01-01')`)

	cfg := config{}
	get := httptest.NewRequest("GET", "/onboard", nil)
	get.Header.Set("X-User-Sub", "github:new")
	get.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
	gw := httptest.NewRecorder()
	handleOnboard(gw, get, db, db, cfg)

	form := formInputs(gw.Body.String())
	if form.Get("action") != "create_world" {
		t.Fatalf("no create_world form rendered, got fields %v", form)
	}
	if form.Get(auth.CSRFField) == "" {
		t.Fatalf("rendered form carries no %q field, but handleOnboardPost requires one", auth.CSRFField)
	}

	post := httptest.NewRequest("POST", "/onboard", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("X-User-Sub", "github:new")
	post.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
	for _, c := range gw.Result().Cookies() {
		post.AddCookie(c)
	}
	pw := httptest.NewRecorder()
	handleOnboardPost(pw, post, db, cfg)

	if pw.Code == http.StatusForbidden {
		t.Fatalf("submitting the rendered form is rejected: %s", strings.TrimSpace(pw.Body.String()))
	}
	if pw.Code != http.StatusSeeOther {
		t.Fatalf("want 303 from the rendered form, got %d: %s", pw.Code, pw.Body.String())
	}
}

var (
	inputTagRe = regexp.MustCompile(`<input\b[^>]*>`)
	attrRe     = regexp.MustCompile(`([a-zA-Z-]+)="([^"]*)"`)
)

// formInputs collects the name/value pairs of every <input> on a page — what a
// browser would submit from it. Radios and checkboxes count only when checked,
// as a browser submits exactly one of a radio group.
func formInputs(page string) url.Values {
	vals := url.Values{}
	for _, tag := range inputTagRe.FindAllString(page, -1) {
		attrs := map[string]string{}
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			attrs[m[1]] = m[2]
		}
		switch attrs["type"] {
		case "radio", "checkbox":
			if !strings.Contains(tag, " checked") {
				continue
			}
		}
		if n := attrs["name"]; n != "" {
			vals.Set(n, html.UnescapeString(attrs["value"]))
		}
	}
	return vals
}

// twoWorldUser: alice administers two worlds and has just paired a JID that no
// route names — spec 5/18's step 6, where the choice is hers to make.
func twoWorldUser(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('acme', '2026-01-01'), ('beta', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES
		('github:alice', 'admin', 'acme', 'allow', '2026-01-01'),
		('github:alice', 'admin', 'beta', 'allow', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:alice', 'telegram:42', '2026-01-01')`)
	return db
}

func getOnboard(db *sql.DB, sub string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", sub)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, config{})
	return w
}

// With two worlds there is no defensible auto-pick: the dashboard used to route
// the JID into whichever folder SQLite returned first, silently and without the
// owner ever seeing the other option. Every world must be offered, and nothing
// may be written until the owner submits.
func TestUnroutedJIDOffersEveryAdminWorld(t *testing.T) {
	db := twoWorldUser(t)

	w := getOnboard(db, "github:alice")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 picker, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, folder := range []string{"acme", "beta"} {
		if !strings.Contains(body, `value="`+folder+`"`) {
			t.Errorf("world %q is not offered as a choice", folder)
		}
	}
	if !strings.Contains(body, "telegram:42") {
		t.Error("picker must name the chat being routed")
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&n)
	if n != 0 {
		t.Errorf("auto-picked a world before the owner chose: %d routes written", n)
	}
}

// Same contract as TestUsernamePickerFormSatisfiesCSRFCheck, for the second
// form onbod renders: replay exactly the fields the page emits, with the
// cookies the GET set. postOnboard injects the double-submit pair itself, so
// only a test built from the rendered page can catch a missing csrf field —
// which is how BUGS F1 shipped.
func TestWorldPickerFormSatisfiesCSRFCheck(t *testing.T) {
	db := twoWorldUser(t)

	gw := getOnboard(db, "github:alice")
	form := formInputs(gw.Body.String())
	if form.Get("action") != "add_route" {
		t.Fatalf("no add_route form rendered, got fields %v", form)
	}
	if form.Get(auth.CSRFField) == "" {
		t.Fatalf("rendered form carries no %q field, but handleOnboardPost requires one", auth.CSRFField)
	}
	if form.Get("match") != "room=42" || form.Get("target") == "" {
		t.Fatalf("form does not bind the chat to a world: %v", form)
	}

	post := httptest.NewRequest("POST", "/onboard", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("X-User-Sub", "github:alice")
	for _, c := range gw.Result().Cookies() {
		post.AddCookie(c)
	}
	pw := httptest.NewRecorder()
	handleOnboardPost(pw, post, db, config{})

	if pw.Code == http.StatusForbidden {
		t.Fatalf("submitting the rendered form is rejected: %s", strings.TrimSpace(pw.Body.String()))
	}
	if pw.Code != http.StatusSeeOther {
		t.Fatalf("want 303 from the rendered form, got %d: %s", pw.Code, pw.Body.String())
	}

	var target string
	db.QueryRow(`SELECT target FROM routes WHERE match = 'room=42'`).Scan(&target)
	if target != form.Get("target") {
		t.Errorf("route target = %q, want the chosen %q", target, form.Get("target"))
	}
}

// One world is not a choice — asking would be a page with a single radio.
func TestSingleWorldRoutesWithoutAsking(t *testing.T) {
	db := twoWorldUser(t)
	db.Exec(`DELETE FROM groups WHERE folder = 'beta'`)

	w := getOnboard(db, "github:alice")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 dashboard, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "add_route") {
		t.Error("asked the owner to choose between one world")
	}
	var target string
	db.QueryRow(`SELECT target FROM routes WHERE match = 'room=42'`).Scan(&target)
	if target != "acme" {
		t.Errorf("single world not routed automatically, target=%q", target)
	}
}

// An empty choice set is a terminal page, not a picker with no options and not
// a dashboard that quietly omits the chat: the owner must be told the chat has
// nowhere to go.
func TestNoAdminWorldIsAnExplicitDeadEnd(t *testing.T) {
	db := twoWorldUser(t)
	// alice keeps a scope (so she is past the invite gate) but administers
	// nothing: her grants name folders that do not exist as groups.
	db.Exec(`DELETE FROM groups`)

	w := getOnboard(db, "github:alice")
	body := w.Body.String()
	if strings.Contains(body, "add_route") {
		t.Fatal("rendered an empty picker")
	}
	if !strings.Contains(body, "telegram:42") || !strings.Contains(body, "Nowhere to route") {
		t.Errorf("dead end not explained to the user, got: %s", body)
	}
}

func TestCreateWorldValidUsername(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:new', 'github:new', 'New User', '2026-01-01')`)

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
	db.QueryRow(`SELECT scope FROM acl WHERE principal = 'github:new'`).Scan(&ug)
	if ug != "alice/**" {
		t.Errorf("creator grant should be subtree-scoped, got %q", ug)
	}

	// username updated
	var un string
	db.QueryRow(`SELECT username FROM user_profiles WHERE sub = 'github:new'`).Scan(&un)
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
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
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

	linkJID(db, db, "telegram:1", "github:alice")

	var userSub string
	db.QueryRow(`SELECT parent FROM acl_membership WHERE child = 'telegram:1'`).Scan(&userSub)
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

	for i := range 3 {
		req := httptest.NewRequest("GET", "/onboard?token=tok-replay", nil)
		w := httptest.NewRecorder()
		handleOnboard(w, req, db, db, cfg)
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
	handleOnboard(w, req, db, db, cfg)

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

// Two parallel claims of the same token must produce exactly one winner: the
// atomic UPDATE ... WHERE user_sub IS NULL RETURNING jid lets only one caller
// bind the JID, so linkJID runs once. claimByToken is the race-safe primitive
// handleTokenLanding gates linkJID on.
func TestClaimByTokenSingleWinner(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(1)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', 'race-tok', '2099-01-01T00:00:00Z', '2026-01-01')`)

	jidA, okA := claimByToken(db, "race-tok", "github:alice")
	jidB, okB := claimByToken(db, "race-tok", "github:bob")

	wins := 0
	if okA {
		wins++
		if jidA != "telegram:1" {
			t.Errorf("winner A returned jid %q, want telegram:1", jidA)
		}
	}
	if okB {
		wins++
		if jidB != "telegram:1" {
			t.Errorf("winner B returned jid %q, want telegram:1", jidB)
		}
	}
	if wins != 1 {
		t.Fatalf("want exactly 1 claim winner, got %d", wins)
	}

	var status string
	var sub, token sql.NullString
	db.QueryRow(`SELECT status, user_sub, token FROM onboarding WHERE jid='telegram:1'`).
		Scan(&status, &sub, &token)
	if status != "token_used" {
		t.Errorf("want status=token_used, got %s", status)
	}
	if !sub.Valid || (sub.String != "github:alice" && sub.String != "github:bob") {
		t.Errorf("user_sub not bound to a winner: %q", sub.String)
	}
	if token.Valid && token.String != "" {
		t.Errorf("token should be cleared, got %q", token.String)
	}
}

// A consumed token is one whose token column is NULL — the consume step nulls
// it. user_sub is no longer the marker: since the edge is written BEFORE the
// consume (P1 crash ordering), a row with user_sub set and a live token is the
// legitimate mid-flight state that a replay must be able to finish.
//
// Takeover is still refused, one layer up: linkJID rejects a JID already bound
// to a different account (errLinkRefused), so a second lander cannot rebind it.
func TestClaimByTokenRefusesClaimed(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token, token_expires, user_sub, created)
		VALUES ('telegram:1', 'token_used', NULL, '2099-01-01T00:00:00Z', 'github:alice', '2026-01-01')`)

	if _, ok := claimByToken(db, "spent-tok", "github:bob"); ok {
		t.Fatal("claimByToken should fail once the token is consumed")
	}
	// And the takeover itself is refused where it matters.
	if err := linkJID(db, db, "telegram:1", "github:alice"); err != nil {
		t.Fatalf("owner link: %v", err)
	}
	if err := linkJID(db, db, "telegram:1", "github:bob"); !errors.Is(err, errLinkRefused) {
		t.Fatalf("takeover want errLinkRefused, got %v", err)
	}
	var sub string
	db.QueryRow(`SELECT user_sub FROM onboarding WHERE jid='telegram:1'`).Scan(&sub)
	if sub != "github:alice" {
		t.Errorf("user_sub overwritten by loser: %q", sub)
	}
}

func TestLinkJIDIdempotent(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:5', 'token_used', '2026-01-01')`)

	linkJID(db, db, "telegram:5", "github:alice")
	linkJID(db, db, "telegram:5", "github:alice")

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM acl_membership WHERE child = 'telegram:5'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 user_jids row, got %d", n)
	}
}

func TestLinkJIDDifferentUserRejected(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:6', 'token_used', '2026-01-01')`)

	linkJID(db, db, "telegram:6", "github:alice")
	linkJID(db, db, "telegram:6", "github:eve")

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM acl_membership WHERE child = 'telegram:6'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 user_jids row, got %d", n)
	}

	var userSub string
	db.QueryRow(`SELECT parent FROM acl_membership WHERE child = 'telegram:6'`).Scan(&userSub)
	if userSub != "github:alice" {
		t.Errorf("want github:alice, got %q", userSub)
	}
}

// Spec 5/18 step 7 inverts what this used to assert. Creating a world routed
// EVERY JID the sub had paired at it — two chats moved when the user asked for
// a world. create_world now writes no route; the /onboard landing it redirects
// to routes the ONE unrouted JID, and the next landing the next.
func TestCreateWorldRoutesNoJIDs(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:new', 'github:new', 'New User', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at) VALUES ('github:new', 'telegram:10', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at) VALUES ('github:new', 'discord:20', '2026-01-01')`)

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
	db.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&n)
	if n != 0 {
		t.Fatalf("create_world wrote %d route(s) as a side effect; want 0", n)
	}

	// One landing, one route — not one per paired chat.
	getOnboard(db, "github:new")
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'newworld'`).Scan(&n)
	if n != 1 {
		t.Fatalf("landing routed %d chats; the act concerns one", n)
	}
	// The other chat is not stranded: the next landing reaches it, one act each.
	getOnboard(db, "github:new")
	db.QueryRow(`SELECT COUNT(*) FROM routes WHERE target = 'newworld'`).Scan(&n)
	if n != 2 {
		t.Errorf("second landing left %d routes; the remaining chat is stranded", n)
	}
}

func TestCreateWorldNoLinkedJIDs(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:lonely', 'github:lonely', 'Lonely', '2026-01-01')`)

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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at)
		VALUES ('github:alice', 'admin', 'alice', 'allow', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
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
	handleOnboard(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (dashboard), got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "create_world") {
		t.Error("should not show username picker on second-JID link")
	}

	// discord:42 should be linked.
	var userSub string
	db.QueryRow(`SELECT parent FROM acl_membership WHERE child = 'discord:42'`).Scan(&userSub)
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

// Operator (an acl "**" grant) can create a world. The world is the assertion;
// routes are not written here at all (5/18 step 7).
func TestCreateWorldOperatorAllowed(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:op', 'op', 'Op', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('github:op', 'admin', '**', 'allow', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
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
	var folder string
	db.QueryRow(`SELECT folder FROM groups WHERE folder = 'opworld'`).Scan(&folder)
	if folder != "opworld" {
		t.Errorf("operator's world not created")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&n)
	if n != 0 {
		t.Errorf("create_world wrote %d route(s) as a side effect; want 0", n)
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

	linkJID(db, db, "telegram:1", "github:alice")

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

	linkJID(db, db, "telegram:1", "google:alice@other.com")

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
	// 1 already admitted today (counted by admitted_at)
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, admitted_at, user_sub, created)
		VALUES ('t:0', 'approved', 'github:org=co', ?, ?, 'github:z', '2026-01-01')`,
		today+"T08:00:00Z", today+"T08:00:00Z")
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

// TestAdmitFromQueueCapHoldsAcrossDays reproduces the cross-day backlog bug:
// rows queued yesterday, admitted today, must count toward today's quota. Under
// the old queued_at-counting code the second tick saw 0 admissions "today"
// (the backlog's queued_at is yesterday) and drained the whole backlog past the
// cap. Counting by admitted_at keeps the cap intact across poll ticks.
func TestAdmitFromQueueCapHoldsAcrossDays(t *testing.T) {
	db := testDB(t)
	yesterday := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02")
	// 3 users queued yesterday, all under the same gate, limit=2.
	for i, jid := range []string{"t:1", "t:2", "t:3"} {
		db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
			VALUES (?, 'queued', 'github:org=co', ?, ?, '2026-01-01')`,
			jid, yesterday+"T1"+strconv.Itoa(i)+":00:00Z", "github:"+jid)
	}
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('github:org=co', 2)`)

	// Two ticks in the same run; the second must not exceed today's cap.
	admitFromQueue(db)
	admitFromQueue(db)

	var approved int
	db.QueryRow(`SELECT COUNT(*) FROM onboarding WHERE gate = 'github:org=co' AND status = 'approved'`).
		Scan(&approved)
	if approved != 2 {
		t.Errorf("want 2 admitted (daily cap), got %d — cap leaks across days", approved)
	}
	// admitted rows must carry today's admitted_at so the count is on the right day.
	today := time.Now().UTC().Format("2006-01-02")
	var stamped int
	db.QueryRow(`SELECT COUNT(*) FROM onboarding WHERE status = 'approved' AND admitted_at LIKE ?`,
		today+"%").Scan(&stamped)
	if stamped != 2 {
		t.Errorf("want 2 rows stamped admitted_at=today, got %d", stamped)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:second', 'second', 'Second', '2026-01-01')`)

	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 100)`)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:second")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, cfg)

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
	linkJID(db, db, "telegram:1", "github:alice")

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "approved" {
		t.Errorf("want approved (legacy), got %s", status)
	}
}

// --- Invite tests ---

// createInvite is a test helper that mints an invite via the new store API,
// returning the raw bearer (I1: the DB stores only its hash).
func createInvite(t *testing.T, db *sql.DB, targetGlob, issuedBy string, maxUses int) string {
	t.Helper()
	_, token, err := store.New(db).CreateInvite(targetGlob, issuedBy, maxUses, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	return token
}

func TestInviteCreation(t *testing.T) {
	db := testDB(t)
	token := createInvite(t, db, "alice", "telegram:1", 3)
	if len(token) != 64 {
		t.Errorf("expected 64-char hex token, got %d", len(token))
	}

	var glob, issuedBy string
	var maxUses, used int
	db.QueryRow(`SELECT target_glob, issued_by_sub, max_uses, used_count FROM invites WHERE ref = ?`,
		store.InviteRef(token)).Scan(&glob, &issuedBy, &maxUses, &used)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:bob', 'bob', 'Bob', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at)
		VALUES ('github:bob', 'telegram:99', '2026-01-01')`)

	token := createInvite(t, db, "alice", "telegram:1", 1)

	cfg := config{authBaseURL: "https://example.com"}
	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/onboard" {
		t.Errorf("want redirect to /onboard, got %s", loc)
	}

	// user_groups row should exist
	var folder string
	db.QueryRow(`SELECT scope FROM acl WHERE principal = 'github:bob'`).Scan(&folder)
	if folder != "alice" {
		t.Errorf("want user_groups folder=alice, got %q", folder)
	}

	// Redemption is a grant and nothing else (5/18 step 7). It used to route
	// every paired JID at the target; the /onboard it redirects to routes the
	// one unrouted chat, attributed.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&n)
	if n != 0 {
		t.Errorf("redemption wrote %d route(s) as a side effect; want 0", n)
	}

	// used_count should be incremented
	var used int
	db.QueryRow(`SELECT used_count FROM invites WHERE ref = ?`, store.InviteRef(token)).Scan(&used)
	if used != 1 {
		t.Errorf("want used_count=1, got %d", used)
	}
}

func TestInviteExpired(t *testing.T) {
	db := testDB(t)
	past := "2020-01-01T00:00:00Z"
	db.Exec(`INSERT INTO invites (ref, target_glob, issued_by_sub, issued_at, max_uses, expires_at)
		VALUES (?, 'alice', 'telegram:1', '2026-01-01T00:00:00Z', 1, ?)`, store.InviteRef("expired-tok"), past)

	cfg := config{}
	req := httptest.NewRequest("GET", "/invite/expired-tok", nil)
	req.SetPathValue("token", "expired-tok")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, cfg)

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
	db.Exec(`INSERT INTO invites (ref, target_glob, issued_by_sub, issued_at, used_count, max_uses)
		VALUES (?, 'alice', 'telegram:1', '2026-01-01T00:00:00Z', 1, 1)`, store.InviteRef("used-tok"))

	cfg := config{}
	req := httptest.NewRequest("GET", "/invite/used-tok", nil)
	req.SetPathValue("token", "used-tok")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, cfg)

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
	db.Exec(`INSERT INTO invites (ref, target_glob, issued_by_sub, issued_at, max_uses)
		VALUES (?, 'alice', 'telegram:1', '2026-01-01T00:00:00Z', 1)`, store.InviteRef("auth-tok"))

	cfg := config{authBaseURL: "https://example.com", secureCookie: true}
	req := httptest.NewRequest("GET", "/invite/auth-tok", nil)
	req.SetPathValue("token", "auth-tok")
	// No X-User-Sub header → should redirect to login
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, cfg)

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
	handleInvite(w, req, db, db, cfg)

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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('bob', 'admin', 'bobroom', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)
	// Alice must own a JID whose room matches the add_route match pattern.
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at) VALUES ('alice', 'telegram:999', '2026-01-01')`)

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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('github:alice', 'admin', 'alice', 'allow', '2026-01-01')`)
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:eve', 'eve', 'Eve', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('eve', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('github:eve', 'admin', 'eve', 'allow', '2026-01-01')`)
	// token_used row with user_sub still NULL.
	db.Exec(`INSERT INTO onboarding (jid, status, token_expires, created)
		VALUES ('telegram:victim', 'token_used', '2099-01-01T00:00:00Z', '2026-01-01')`)

	// Alice legitimately claims the JID.
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "telegram:victim"})
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, config{})

	var jidOwner string
	db.QueryRow(`SELECT parent FROM acl_membership WHERE child = 'telegram:victim'`).Scan(&jidOwner)
	if jidOwner != "github:alice" {
		t.Fatalf("first claim: want github:alice, got %q", jidOwner)
	}

	// Eve tries to rebind using the same cookie. Must fail: onboarding row is
	// now claimed (user_sub != NULL), so the atomic UPDATE matches nothing.
	req2 := httptest.NewRequest("GET", "/onboard", nil)
	req2.Header.Set("X-User-Sub", "github:eve")
	req2.AddCookie(&http.Cookie{Name: "onboard_jid", Value: "telegram:victim"})
	w2 := httptest.NewRecorder()
	handleOnboard(w2, req2, db, db, config{})

	// Ownership unchanged.
	db.QueryRow(`SELECT parent FROM acl_membership WHERE child = 'telegram:victim'`).Scan(&jidOwner)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:bob', 'bob', 'Bob', '2026-01-01')`)
	db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('alice', '2026-01-01')`)
	tok := createInvite(t, db, "alice", "telegram:1", 1)

	// First consume.
	req := httptest.NewRequest("GET", "/invite/"+tok, nil)
	req.SetPathValue("token", tok)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, config{})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("first consume: want 303, got %d", w.Code)
	}

	// Second consume — uses is now 1 == max_uses; must fail.
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:eve', 'eve', 'Eve', '2026-01-01')`)
	req2 := httptest.NewRequest("GET", "/invite/"+tok, nil)
	req2.SetPathValue("token", tok)
	req2.Header.Set("X-User-Sub", "github:eve")
	w2 := httptest.NewRecorder()
	handleInvite(w2, req2, db, db, config{})
	if w2.Code != http.StatusOK {
		t.Errorf("second consume: want 200 error page, got %d", w2.Code)
	}
	// Eve should not have user_groups row.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM acl WHERE principal = 'github:eve'`).Scan(&n)
	if n != 0 {
		t.Errorf("eve must not be granted access, got %d rows", n)
	}
}

func TestAddRouteWrongTarget(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('bob', 'admin', 'bobroom', 'allow', '2026-01-01')`)

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

// renderDashboard must HTML-escape attacker-controlled username, sub, and jid.
func TestDashboardXSSEscape(t *testing.T) {
	db := testDB(t)
	attacker := `<script>alert(1)</script>`
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES (?, ?, '', '2026-01-01')`, attacker, attacker)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES (?, 'admin', 'room', 'allow', '2026-01-01')`, attacker)
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at) VALUES (?, ?, '2026-01-01')`,
		attacker, attacker)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", attacker)
	handleOnboard(w, req, db, db, config{})

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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:x', 'x', 'X', '2026-01-01')`)
	// gate value itself is stored, but the eta-msg is server-side formatted;
	// ensure no raw DB reflection occurs in HTML without escape.
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:1', 'queued', '<x>', '2026-04-17T10:00:00Z', 'github:x', '2026-01-01')`)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:x")
	handleOnboard(w, req, db, db, config{})

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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)

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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)

	w := postOnboard(db, config{}, "alice", url.Values{
		"action": {"delete_route"}, "route_id": {"not-a-number"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestDeleteRouteNotFound(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'myroom', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('op', 'admin', '**', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '2026-01-01')`)
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
	handleOnboard(w, req, db, db, cfg)
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
	linkJID(db, db, "telegram:7", "github:alice")
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
	db.Exec(`INSERT INTO invites (ref, target_glob, issued_by_sub, issued_at, max_uses)
		VALUES (?, 'alice', 'x', '2026-01-01T00:00:00Z', 1)`, store.InviteRef("tok"))
	cfg := config{authBaseURL: "https://example.com", secureCookie: true}
	req := httptest.NewRequest("GET", "/invite/tok", nil)
	req.SetPathValue("token", "tok")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db, cfg)

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
	handleOnboard(w, req, db, db, cfg)
	// No token, no X-User-Sub → redirect to login
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect, got %d", w.Code)
	}
}

// promptUnprompted POSTs the greeting + onboard link to routd's /v1/outbound,
// presenting the service:onbod bearer. A test HTTP server captures the wire
// format without mocking.
func TestPromptUnpromptedSendsGreeting(t *testing.T) {
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

	cfg := config{gatedURL: srv.URL, authBaseURL: "https://ex.com",
		svcToken: func(context.Context) (string, error) { return "svc-jwt", nil }}
	promptUnprompted(db, cfg)

	if gotAuth != "Bearer svc-jwt" {
		t.Errorf("want Bearer service token, got %q", gotAuth)
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
	handleOnboard(w, req, db, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/onboard" {
		t.Errorf("want redirect to /onboard, got %q", got)
	}
	// linkJID ran synchronously (no gates, no user rows yet — status goes approved).
	var sub string
	db.QueryRow(`SELECT parent FROM acl_membership WHERE child='telegram:7'`).Scan(&sub)
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
	handleInvite(w, req, db, db, config{})
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('op', 'admin', '**', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('op', 'admin', '**', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'alice', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO acl_membership (parent, child, added_at) VALUES ('alice','telegram:42','2026-01-01')`)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:x', 'x', 'X', '2026-01-01')`)
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
	handleOnboard(w, req, db, db, config{})
	if !strings.Contains(w.Body.String(), "hours") {
		t.Errorf("expected 'hours' in ETA, body=%s", w.Body.String())
	}
}

// admitFromQueue respects already-admitted today's count and does not
// exceed limitPerDay even across repeated invocations.
func TestAdmitFromQueueDailyLimitPersists(t *testing.T) {
	db := testDB(t)
	today := time.Now().Format("2006-01-02")
	// Already-approved entry counts against today's budget (by admitted_at).
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, admitted_at, user_sub, created)
		VALUES ('t:old', 'approved', '*', ?, ?, 'u:old', '2026-01-01')`,
		today+"T02:00:00Z", today+"T02:00:00Z")
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

// Backlog queued on a prior day, admitted today, must count against today's
// limit. The buggy queued_at-scoped count saw 0 admitted today and drained the
// whole backlog every poll, making the daily limit unbounded across days.
func TestAdmitFromQueuePriorDayBacklogCounts(t *testing.T) {
	db := migratedDB(t)
	// Two entries queued yesterday, limit 1/day. First admit takes one.
	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:1', 'queued', '*', ?, 'u:1', '2026-01-01')`, yesterday+"T01:00:00Z")
	db.Exec(`INSERT INTO onboarding (jid, status, gate, queued_at, user_sub, created)
		VALUES ('t:2', 'queued', '*', ?, 'u:2', '2026-01-01')`, yesterday+"T02:00:00Z")
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('*', 1)`)

	admitFromQueue(db)
	var s1, s2 string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid='t:1'`).Scan(&s1)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid='t:2'`).Scan(&s2)
	if s1 != "approved" {
		t.Errorf("oldest backlog entry should be admitted, got %s", s1)
	}
	if s2 != "queued" {
		t.Errorf("second entry must wait: today's quota of 1 spent on t:1, got %s", s2)
	}

	// Re-poll: the admission stamped admitted_at=today, so today's count is 1,
	// quota exhausted; t:2 must stay queued instead of draining the backlog.
	admitFromQueue(db)
	db.QueryRow(`SELECT status FROM onboarding WHERE jid='t:2'`).Scan(&s2)
	if s2 != "queued" {
		t.Errorf("prior-day backlog must not evade today's limit, got %s", s2)
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
	authd, _ := fakeAuthdToken(t)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ARIZUKO_DEV", "true")
	t.Setenv("ONBOARD_POLL_INTERVAL", "5s")
	t.Setenv("ONBOARDING_GREETING", "hi")
	t.Setenv("ROUTER_URL", "http://r:1")
	t.Setenv("AUTH_BASE_URL", "https://auth.example.com")
	t.Setenv("AUTHD_URL", authd.URL)
	t.Setenv("AUTHD_SERVICE_KEY", "boot-onbod")

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
	if cfg.svcToken == nil {
		t.Errorf("svcToken must be set (AUTHD required)")
	}
}

// loadConfig with invalid poll interval falls back to default (10s).
func TestLoadConfigBadPollInterval(t *testing.T) {
	dir := t.TempDir()
	authd, _ := fakeAuthdToken(t)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ARIZUKO_DEV", "true")
	t.Setenv("ONBOARD_POLL_INTERVAL", "not-a-duration")
	t.Setenv("AUTHD_URL", authd.URL)
	t.Setenv("AUTHD_SERVICE_KEY", "boot-onbod")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.pollInterval != 10*time.Second {
		t.Errorf("want default 10s, got %v", cfg.pollInterval)
	}
}

// isOperator: operator emerges only from a `**` grant; nil/empty/other
// grants are not operator (no nil-sentinel — see specs/5/32-acl-unified.md).
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
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', 'real', 'allow', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('alice', 'admin', '', 'allow', '2026-01-01')`)
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
	db.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES ('github:alice', 'alice', 'Alice', '2026-01-01')`)
	db.Exec(`INSERT INTO acl (principal, action, scope, effect, granted_at) VALUES ('github:alice', 'admin', 'alice', 'allow', '2026-01-01')`)

	cfg := config{authBaseURL: "https://example.com"}

	// 1) Unauthenticated GET — token persists.
	req1 := httptest.NewRequest("GET", "/onboard?token=tok-bind", nil)
	w1 := httptest.NewRecorder()
	handleOnboard(w1, req1, db, db, cfg)
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
	handleOnboard(w2, req2, db, db, cfg)

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
	handleOnboard(w3, req3, db, db, cfg)
	if w3.Code != http.StatusOK {
		t.Errorf("post-bind replay: want 200 (error page), got %d", w3.Code)
	}
}
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

// Gates configured and none matching is a dead end, not a success: the row
// stays at token_used and no later pass revisits it. It used to warn and
// return, so the caller redirected the user to an empty dashboard with no
// indication anything had gone wrong.
func TestLinkJID_NoGateMatchIsRefused(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:9', 'token_used', '2026-01-01')`)
	db.Exec(`INSERT INTO onboarding_gates (gate, limit_per_day) VALUES ('github:org=acme', 10)`)

	err := linkJID(db, db, "telegram:9", "google:nobody")
	if err == nil {
		t.Fatal("no matching gate must return an error the caller can surface")
	}
	if !errors.Is(err, errLinkRefused) {
		t.Errorf("want errLinkRefused (403), got %v", err)
	}

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:9'`).Scan(&status)
	if status == "queued" || status == "approved" {
		t.Errorf("row advanced to %q despite no gate match", status)
	}
}

// A second account may not take over a JID already linked elsewhere, and the
// refusal reaches the caller rather than only the log.
func TestLinkJID_AlreadyClaimedIsRefused(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:10', 'token_used', '2026-01-01')`)
	if err := linkJID(db, db, "telegram:10", "github:alice"); err != nil {
		t.Fatalf("first link: %v", err)
	}
	err := linkJID(db, db, "telegram:10", "github:mallory")
	if !errors.Is(err, errLinkRefused) {
		t.Errorf("takeover want errLinkRefused, got %v", err)
	}
}

// W1: a creator's grant must reach the world's SUBGROUPS, and the reader that
// resolves "which folder may this user administer" must honour the pattern
// rather than compare strings. The old `JOIN acl a ON a.scope = g.folder`
// matched neither `acme` against `acme/eng` nor `acme/**` against anything.
func TestAdminFolders_HonoursSubtreeGrant(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO groups (folder, added_at)
		VALUES ('acme', datetime('now')), ('acme/eng', datetime('now'))`); err != nil {
		t.Fatalf("groups fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_at, granted_by)
		VALUES ('github:owner', 'admin', 'acme/**', 'allow', '', '', datetime('now'), 'test')`); err != nil {
		t.Fatalf("acl fixture: %v", err)
	}

	// Both the world AND its subgroup are choices the owner may be offered.
	if got := adminFolders(db, "github:owner"); len(got) != 2 {
		t.Fatalf("subtree grant enumerated %v, want both acme and acme/eng", got)
	}
	if got := adminFolders(db, "github:stranger"); len(got) != 0 {
		t.Errorf("ungranted principal enumerated %v, want none", got)
	}
}

// P1 crash ordering: the membership edge must be durable BEFORE the token is
// consumed. The two writes land in different databases with no cross-DB
// transaction, so a crash between them is possible at any point; edge-first is
// the only order that is safe wherever it lands. This test simulates the crash
// by doing the first half only, then replaying.
func TestTokenLanding_EdgeSurvivesCrashBeforeConsume(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created, token)
		VALUES ('telegram:77', 'awaiting_message', '2026-01-01', 'tok-77')`)

	// First half of the landing: resolve, write the edge, then "crash".
	jid, ok := jidForToken(db, "tok-77")
	if !ok || jid != "telegram:77" {
		t.Fatalf("jidForToken = %q,%v", jid, ok)
	}
	if err := linkJID(db, db, jid, "github:alice"); err != nil {
		t.Fatalf("linkJID: %v", err)
	}

	// The token must still be live, so the user can simply click again.
	if _, ok := jidForToken(db, "tok-77"); !ok {
		t.Fatal("token was consumed before the edge was durable — the stranding order")
	}

	// Replay: the edge write is idempotent and the consume now succeeds.
	if err := linkJID(db, db, jid, "github:alice"); err != nil {
		t.Fatalf("replayed linkJID must be a no-op, got: %v", err)
	}
	if _, ok := claimByToken(db, "tok-77", "github:alice"); !ok {
		t.Fatal("replay could not consume the token")
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM acl_membership WHERE child='telegram:77'`).Scan(&n)
	if n != 1 {
		t.Errorf("replay produced %d edges, want exactly 1", n)
	}
}

// O1: a row in a status no query selects is stranded — never prompted, queued
// or admitted, and its jid PRIMARY KEY blocks a fresh insert, so the user can
// never onboard from that chat again. Two such rows exist in production. The
// pipeline cannot repair them (picking a status is the operator's call) but it
// must not stay silent about them.
func TestKnownStatusesCoversEveryWriter(t *testing.T) {
	// Every status the code writes must be one the pipeline can advance,
	// otherwise we ship the O1 bug again with a new value.
	for _, s := range []string{"awaiting_message", "token_used", "queued", "approved"} {
		if !knownStatuses[s] {
			t.Errorf("%q is written by the pipeline but not in knownStatuses", s)
		}
	}
	if knownStatuses["pending"] {
		t.Error("'pending' is not a status any writer produces; it must read as stranded")
	}
}

// acl_membership carries pairing edges AND role memberships, and a JID may hold
// both — a paired user granted operator directly. The claim check must ignore
// role parents; otherwise QueryRow can return the role row in undefined order
// and refuse a legitimate re-pair, naming "role:operator" as the other account.
// sloth has exactly this shape in production.
func TestLinkJID_RoleMembershipIsNotAClaim(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:5', 'token_used', '2026-01-01')`)
	// The paired sub must sort AFTER "role:" so the role row comes first in the
	// (child, parent) index — that ordering is what makes the unfiltered
	// QueryRow return the role and refuse. With a sub like "google:..." the bug
	// hides, because the pairing row happens to come first.
	const sub = "zoom:alice"
	if err := linkJID(db, db, "telegram:5", sub); err != nil {
		t.Fatalf("first pair: %v", err)
	}
	// The JID is also granted operator directly.
	if _, err := db.Exec(`INSERT INTO acl_membership (child, parent, added_at, added_by)
		VALUES ('telegram:5', 'role:operator', datetime('now'), 'cli')`); err != nil {
		t.Fatal(err)
	}
	// Re-pairing to the SAME account must still succeed (idempotent replay).
	if err := linkJID(db, db, "telegram:5", sub); err != nil {
		t.Errorf("re-pair refused because of the role row: %v", err)
	}
}
