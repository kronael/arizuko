package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/tests/testutils"
)

// chatTestDash builds a dashd over a migrated instance DB and a mux. The
// caller seeds groups/messages/tokens against inst.DB.
func chatTestDash(t *testing.T) (*testutils.Inst, *http.ServeMux) {
	t.Helper()
	inst := testutils.NewInstance(t)
	d := &dash{db: inst.DB, dbRW: inst.DB, dbRoutd: inst.DB, dbOnbod: inst.DB, groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	return inst, mux
}

func addGroup(t *testing.T, inst *testutils.Inst, folder string) {
	t.Helper()
	if _, err := inst.DB.Exec(
		`INSERT INTO groups (folder, added_at) VALUES (?, ?)`,
		folder, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("add group %q: %v", folder, err)
	}
}

func TestHandleChatPortal_empty(t *testing.T) {
	_, mux := chatTestDash(t)

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "No groups available") {
		t.Errorf("empty state missing: %q", body)
	}
}

// The portal lists every visible group in the new-conversation form's group
// select (and the search filter), so a member can start a conversation in any
// of them. The form action points at the first group's mint path.
func TestHandleChatPortal_groups(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "alpha")
	addGroup(t, inst, "bravo")

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, f := range []string{"alpha", "bravo"} {
		if !strings.Contains(body, `<option value="`+f+`"`) {
			t.Errorf("group %q missing from portal selects: %q", f, body)
		}
	}
	// New-conversation form posts to the first group's mint path.
	if !strings.Contains(body, `action="/dash/chat/alpha/"`) {
		t.Errorf("new-conversation form action missing: %q", body)
	}
	// No sessions seeded → empty state.
	if !strings.Contains(body, "No conversations yet") {
		t.Errorf("empty conversation state missing: %q", body)
	}
}

// A non-member caller (no grant on the folder, not operator) gets 403 on the
// group page.
func TestHandleChatGroup_access(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "secret")

	req := httptest.NewRequest("GET", "/dash/chat/secret/", nil)
	req.Header.Set("X-User-Sub", "stranger@x")
	req.Header.Set("X-User-Groups", `["other"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandleChatGroup_renders(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "eng"
	addGroup(t, inst, folder)
	// A web-chat conversation (one topic) for this folder.
	seedWebTopic(t, inst, folder, "t1", "design review thoughts")

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/"+folder+"/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "New chat session") {
		t.Errorf("new-session form missing: %q", body)
	}
	// Conversation listed by its topic preview.
	if !strings.Contains(body, "design review thoughts") {
		t.Errorf("conversation not listed: %q", body)
	}
	// Form POSTs to the same folder path.
	if !strings.Contains(body, `action="/dash/chat/`+folder+`/"`) {
		t.Errorf("form action missing: %q", body)
	}
}

// A non-operator with a direct grant on the folder may see the group page.
func TestHandleChatGroup_grantedMember(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "team"
	addGroup(t, inst, folder)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "member@x", Action: "admin", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/dash/chat/"+folder+"/", nil)
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["`+folder+`"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "New chat session") {
		t.Errorf("granted member did not get group page")
	}
}

// seedChatSession creates chat_sessions (idempotent) and inserts one row. The
// process-global sync.Once in ensureChatSessionsTable creates the table in only
// the first test's DB, so tests seed the table directly.
func seedChatSession(t *testing.T, inst *testutils.Inst, folder, token, label, createdAt string) {
	t.Helper()
	if _, err := inst.DB.Exec(`CREATE TABLE IF NOT EXISTS chat_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, folder TEXT NOT NULL,
		token TEXT NOT NULL UNIQUE, label TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create chat_sessions: %v", err)
	}
	if _, err := inst.DB.Exec(
		`INSERT INTO chat_sessions (folder, token, label, created_at) VALUES (?,?,?,?)`,
		folder, token, label, createdAt); err != nil {
		t.Fatalf("seed chat_session %s: %v", token, err)
	}
}

// seedWebTopic seeds one user web-chat message under a topic, timestamped now
// (RFC3339Nano) so title-derivation (timestamp >= created_at) sees it.
func seedWebTopic(t *testing.T, inst *testutils.Inst, folder, topic, content string) {
	t.Helper()
	if _, err := inst.DB.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, source, topic, is_bot_message)
		 VALUES (?, ?, 's', ?, ?, 'web', ?, 0)`,
		"m-"+folder+"-"+topic, "web:"+folder, content,
		time.Now().Format(time.RFC3339Nano), topic); err != nil {
		t.Fatalf("seed web topic %s/%s: %v", folder, topic, err)
	}
}

func TestHandleChatPortal_sessions(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "eng")
	now := time.Now().Format(time.RFC3339Nano)
	seedChatSession(t, inst, "eng", "tok-labelled", "Q3 roadmap", now)
	// A session with no label derives its title from the first user message.
	seedChatSession(t, inst, "eng", "tok-derived", "", now)
	seedWebMsgAt(t, inst, "eng", "what is the deploy plan", now)

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Q3 roadmap") {
		t.Errorf("labelled session title missing: %q", body)
	}
	if !strings.Contains(body, "what is the deploy plan") {
		t.Errorf("derived session title missing: %q", body)
	}
	// Both sessions link to their continue URLs.
	if !strings.Contains(body, `href="/chat/tok-labelled/"`) {
		t.Errorf("continue link for labelled session missing: %q", body)
	}
	if !strings.Contains(body, `href="/chat/tok-derived/"`) {
		t.Errorf("continue link for derived session missing: %q", body)
	}
}

// seedWebMsgAt inserts a user web-chat message at a specific timestamp (no
// topic) — used to feed session title derivation.
func seedWebMsgAt(t *testing.T, inst *testutils.Inst, folder, content, ts string) {
	t.Helper()
	if _, err := inst.DB.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, source, is_bot_message)
		 VALUES (?, ?, 's', ?, ?, 'web', 0)`,
		"m-deploy", "web:"+folder, content, ts); err != nil {
		t.Fatalf("seed web msg: %v", err)
	}
}

func TestHandleChatPortal_search(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "eng")
	now := time.Now().Format(time.RFC3339Nano)
	seedChatSession(t, inst, "eng", "tok-deploy", "deploy pipeline", now)
	seedChatSession(t, inst, "eng", "tok-design", "design review", now)

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/?q=deploy", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "deploy pipeline") {
		t.Errorf("matching session missing under ?q=deploy: %q", body)
	}
	if strings.Contains(body, "design review") {
		t.Errorf("non-matching session leaked under ?q=deploy")
	}
}

func TestHandleChatPortal_dateGrouping(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "eng")
	now := time.Now()
	seedChatSession(t, inst, "eng", "tok-today", "today thread",
		now.Format(time.RFC3339Nano))
	seedChatSession(t, inst, "eng", "tok-old", "old thread",
		now.AddDate(0, 0, -30).Format(time.RFC3339Nano))

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Today") {
		t.Errorf("Today date group missing: %q", body)
	}
	if !strings.Contains(body, "Older") {
		t.Errorf("Older date group missing: %q", body)
	}
	// Today header precedes Older (newest-first ordering).
	if strings.Index(body, ">Today<") > strings.Index(body, ">Older<") {
		t.Errorf("date groups out of order (Today should precede Older)")
	}
}

// The portal hides another caller's sessions: a group-scoped member sees only
// their folder's conversations.
func TestHandleChatPortal_scoped(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "eng")
	addGroup(t, inst, "secret")
	now := time.Now().Format(time.RFC3339Nano)
	seedChatSession(t, inst, "eng", "tok-eng", "eng thread", now)
	seedChatSession(t, inst, "secret", "tok-secret", "secret thread", now)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "member@x", Action: "admin", Scope: "eng", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/dash/chat/", nil)
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["eng"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "eng thread") {
		t.Errorf("own-folder session missing: %q", body)
	}
	if strings.Contains(body, "secret thread") {
		t.Errorf("other-folder session leaked to scoped member")
	}
}

func TestHandleChatNew_recordsSession(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "alice"
	addGroup(t, inst, folder)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "admin@x", Action: "admin", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/dash/chat/"+folder+"/",
		strings.NewReader("label=kickoff"))
	req.Host = "example.com"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "admin@x")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %q", w.Code, w.Body.String())
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(w.Header().Get("Location"), "/chat/"), "/")

	var gotFolder, gotLabel, gotToken string
	if err := inst.DB.QueryRow(
		`SELECT folder, token, label FROM chat_sessions WHERE token = ?`, raw).
		Scan(&gotFolder, &gotToken, &gotLabel); err != nil {
		t.Fatalf("minted session not in chat_sessions: %v", err)
	}
	if gotFolder != folder || gotLabel != "kickoff" {
		t.Errorf("session row = folder %q label %q, want %q / kickoff", gotFolder, gotLabel, folder)
	}
}

func TestHandleChatNew_creates(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "alice"
	addGroup(t, inst, folder)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "admin@x", Action: "admin", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/dash/chat/"+folder+"/",
		strings.NewReader("label=design"))
	req.Host = "example.com" // same-origin (no Origin header) for CSRF gate
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "admin@x")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %q", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/chat/") || !strings.HasSuffix(loc, "/") {
		t.Errorf("redirect = %q, want /chat/<token>/", loc)
	}
	// The minted token resolves back to this folder's web: JID.
	raw := strings.TrimSuffix(strings.TrimPrefix(loc, "/chat/"), "/")
	row, ok := inst.Store.LookupRouteToken(raw)
	if !ok {
		t.Fatalf("minted token %q not found in store", raw)
	}
	if row.JID != "web:"+folder || row.OwnerFolder != folder {
		t.Errorf("token row = %+v, want web:%s / %s", row, folder, folder)
	}
}

// A non-admin caller cannot mint a chat token.
func TestHandleChatNew_forbidden(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "alice"
	addGroup(t, inst, folder)

	req := httptest.NewRequest("POST", "/dash/chat/"+folder+"/",
		strings.NewReader(""))
	req.Host = "example.com"
	req.Header.Set("X-User-Sub", "nobody@x")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(inst.Store.ListRouteTokens(folder)) != 0 {
		t.Errorf("forbidden POST must not mint a token")
	}
}
