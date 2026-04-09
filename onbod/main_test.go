package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, prompted_at TEXT, created TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, is_from_me INTEGER, is_bot_message INTEGER, source TEXT NOT NULL DEFAULT '');
		CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT, cron TEXT, next_run TEXT, status TEXT, created_at TEXT, context_mode TEXT);
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

func TestPromptUnprompted(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, created) VALUES ('telegram:1', 'awaiting_message', '2026-01-01')`)

	// sendReply will fail silently (no gatedURL), but we can check prompted_at was set
	cfg := config{greeting: "Welcome to our server!"}
	promptUnprompted(db, cfg)

	var prompted sql.NullString
	db.QueryRow(`SELECT prompted_at FROM onboarding WHERE jid = 'telegram:1'`).Scan(&prompted)
	if !prompted.Valid {
		t.Error("expected prompted_at to be set")
	}
}

func TestCheckResponsesTransitionsToPending(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, prompted_at, created)
		VALUES ('telegram:1', 'awaiting_message', '2026-01-01T00:00:00Z', '2026-01-01')`)
	db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message, source)
		VALUES ('msg1', 'telegram:1', 'user', 'Hello admin!', '2026-01-01T00:01:00Z', 0, 0, 'telegram')`)

	cfg := config{}
	checkResponses(db, cfg)

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "pending" {
		t.Errorf("want pending, got %s", status)
	}
}

func TestCheckResponsesIgnoresOldMessages(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, prompted_at, created)
		VALUES ('telegram:1', 'awaiting_message', '2026-01-01T00:05:00Z', '2026-01-01')`)
	// Message is before prompted_at
	db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message, source)
		VALUES ('msg1', 'telegram:1', 'user', 'old msg', '2026-01-01T00:00:00Z', 0, 0, 'telegram')`)

	cfg := config{}
	checkResponses(db, cfg)

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "awaiting_message" {
		t.Errorf("want awaiting_message, got %s", status)
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

	var source string
	var fromMe, botMsg int
	db.QueryRow(`SELECT source, is_from_me, is_bot_message FROM messages
		WHERE chat_jid = 'telegram:1' AND sender = 'system'`).Scan(&source, &fromMe, &botMsg)
	if source != "" {
		t.Errorf("welcome source must be empty per audit-log spec, got %q", source)
	}
	if fromMe != 1 || botMsg != 1 {
		t.Errorf("welcome flags: from_me=%d bot_msg=%d", fromMe, botMsg)
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

	// All three parent groups must exist
	for _, want := range []string{"world", "world/house", "world/house/room"} {
		var got string
		db.QueryRow(`SELECT folder FROM groups WHERE folder = ?`, want).Scan(&got)
		if got != want {
			t.Errorf("missing group %q", want)
		}
	}

	// Parent chain correct
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
		{"room=1*", ""}, // wildcard → skipped, no room → ""
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
