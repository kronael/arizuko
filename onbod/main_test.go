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
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT, sender TEXT, channel TEXT, world_name TEXT, prompted_at TEXT, created TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, is_from_me INTEGER, is_bot_message INTEGER, source TEXT, group_folder TEXT);
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
	db.Exec(`INSERT INTO onboarding (jid, status, channel, created) VALUES ('telegram:1', 'awaiting_message', 'teled', '2026-01-01')`)

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
	db.Exec(`INSERT INTO onboarding (jid, status, channel, prompted_at, created)
		VALUES ('telegram:1', 'awaiting_message', 'teled', '2026-01-01T00:00:00Z', '2026-01-01')`)
	db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message, source, group_folder)
		VALUES ('msg1', 'telegram:1', 'user', 'Hello admin!', '2026-01-01T00:01:00Z', 0, 0, 'telegram', '')`)

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
	db.Exec(`INSERT INTO onboarding (jid, status, channel, prompted_at, created)
		VALUES ('telegram:1', 'awaiting_message', 'teled', '2026-01-01T00:05:00Z', '2026-01-01')`)
	// Message is before prompted_at
	db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message, source, group_folder)
		VALUES ('msg1', 'telegram:1', 'user', 'old msg', '2026-01-01T00:00:00Z', 0, 0, 'telegram', '')`)

	cfg := config{}
	checkResponses(db, cfg)

	var status string
	db.QueryRow(`SELECT status FROM onboarding WHERE jid = 'telegram:1'`).Scan(&status)
	if status != "awaiting_message" {
		t.Errorf("want awaiting_message, got %s", status)
	}
}
