package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// routdDB builds an in-memory messages.db with the columns the routd page reads
// (errored / status / is_bot_message) plus a minimal routes+groups schema for
// the stat counts.
func routdDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT,
			timestamp TEXT, is_bot_message INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'sent',
			errored INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE routes (id INTEGER PRIMARY KEY AUTOINCREMENT, match TEXT, target TEXT)`,
		`CREATE TABLE groups (folder TEXT PRIMARY KEY, name TEXT, added_at TEXT, parent TEXT)`,
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TEXT NOT NULL DEFAULT '', category TEXT NOT NULL,
			action TEXT NOT NULL, actor TEXT NOT NULL, actor_sub TEXT, resource TEXT,
			scope TEXT, surface TEXT, params_summary TEXT, outcome TEXT NOT NULL,
			error_msg TEXT, duration_ms INTEGER, turn_id TEXT, folder TEXT,
			instance TEXT, request_id TEXT, source_ip TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

// TestRoutdEmpty: no errored messages → the errored table renders the empty
// marker and stats show zeros.
func TestRoutdEmpty(t *testing.T) {
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := asOperator(httptest.NewRequest("GET", "/dash/routd/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Errored chats") {
		t.Errorf("missing Errored chats heading")
	}
	if !strings.Contains(body, `class="empty"`) {
		t.Errorf("empty errored table should render the empty marker")
	}
	if !strings.Contains(body, "0 routes") || !strings.Contains(body, "0 groups") {
		t.Errorf("stats missing zero counts: %s", body)
	}
}

// TestRoutdErroredRows: errored messages group by chat and render a retry form;
// the pending-outbound count reflects pending bot messages.
func TestRoutdErroredRows(t *testing.T) {
	db := routdDB(t)
	defer db.Close()
	seed := [][2]string{
		{"m1", "web:alice"},
		{"m2", "web:alice"},
		{"m3", "telegram:group/42"},
	}
	for _, s := range seed {
		if _, err := db.Exec(
			`INSERT INTO messages (id, chat_jid, timestamp, errored) VALUES (?, ?, ?, 1)`,
			s[0], s[1], "2026-06-16T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// One pending bot message — counted; one sent — not counted.
	if _, err := db.Exec(
		`INSERT INTO messages (id, chat_jid, timestamp, is_bot_message, status)
		 VALUES ('p1','web:alice','2026-06-16T10:01:00Z',1,'pending')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db, dbRoutd: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := asOperator(httptest.NewRequest("GET", "/dash/routd/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "web:alice") || !strings.Contains(body, "telegram:group/42") {
		t.Errorf("missing errored chat_jids: %s", body)
	}
	if !strings.Contains(body, `action="/dash/routd/retry"`) {
		t.Errorf("missing retry form")
	}
	// web:alice carries a resolvable folder → rendered as a chat link.
	if !strings.Contains(body, `href="/dash/chat/alice/"`) {
		t.Errorf("web:alice should link to its chat page: %s", body)
	}
	if !strings.Contains(body, "1 pending outbound") {
		t.Errorf("pending outbound count wrong: %s", body)
	}
}

// TestRoutdRetryClears: POST /dash/routd/retry clears errored for the chat and
// redirects; the cleared messages no longer surface.
func TestRoutdRetryClears(t *testing.T) {
	db := routdDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO messages (id, chat_jid, timestamp, errored) VALUES
		 ('m1','web:alice','2026-06-16T10:00:00Z',1),
		 ('m2','web:bob','2026-06-16T10:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db, dbRW: db, dbRoutd: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := strings.NewReader("chat_jid=web:alice")
	req := asOperator(httptest.NewRequest("POST", "/dash/routd/retry", form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}

	var aliceErr, bobErr int
	db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_jid='web:alice' AND errored=1`).Scan(&aliceErr)
	db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_jid='web:bob' AND errored=1`).Scan(&bobErr)
	if aliceErr != 0 {
		t.Errorf("alice errored not cleared: %d still errored", aliceErr)
	}
	if bobErr != 1 {
		t.Errorf("bob errored should be untouched: %d", bobErr)
	}
}

// TestRoutdNonOperatorForbidden: the routd cockpit is operator-only.
func TestRoutdNonOperatorForbidden(t *testing.T) {
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/routd/", nil)
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
