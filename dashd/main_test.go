package main

import (
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
	for _, q := range []string{
		`CREATE TABLE groups (
			folder TEXT PRIMARY KEY, name TEXT,
			added_at TEXT, parent TEXT, state TEXT NOT NULL DEFAULT 'active')`,
		`CREATE TABLE sessions (group_folder TEXT PRIMARY KEY, session_id TEXT)`,
		`CREATE TABLE channels (name TEXT, url TEXT)`,
		`CREATE TABLE scheduled_tasks (
			id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT,
			cron TEXT, next_run TEXT, status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT,
			timestamp TEXT, source TEXT, group_folder TEXT, verb TEXT)`,
		`CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, channel TEXT,
			is_group INTEGER DEFAULT 0, last_message_time TEXT, errored INTEGER DEFAULT 0)`,
		`CREATE TABLE task_run_logs (id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT, run_at TEXT, duration_ms INTEGER, status TEXT, result TEXT, error TEXT)`,
		`CREATE TABLE routes (id INTEGER PRIMARY KEY AUTOINCREMENT, jid TEXT,
			seq INTEGER DEFAULT 0, type TEXT DEFAULT 'default', match TEXT, target TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func TestDashHealth(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("health = %v", resp)
	}
}

func TestDashPortal(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct == "" {
		t.Error("no Content-Type")
	}
}

func TestDashStatus(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, dbPath: ":memory:"}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestDashTasks(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/tasks/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}
