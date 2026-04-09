package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			timestamp TEXT, source TEXT NOT NULL DEFAULT '', verb TEXT)`,
		`CREATE TABLE chats (jid TEXT PRIMARY KEY, errored INTEGER DEFAULT 0)`,
		`CREATE TABLE task_run_logs (id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT, run_at TEXT, duration_ms INTEGER, status TEXT, result TEXT, error TEXT)`,
		`CREATE TABLE routes (id INTEGER PRIMARY KEY AUTOINCREMENT,
			seq INTEGER DEFAULT 0, match TEXT, target TEXT, impulse_config TEXT)`,
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

func TestRenderMemorySectionTraversal(t *testing.T) {
	groups := t.TempDir()
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, "../../etc")
	body := w.Body.String()
	if !strings.Contains(body, "Invalid group path") {
		t.Errorf("traversal not rejected: body = %q", body)
	}
}

func TestRenderMemorySectionValid(t *testing.T) {
	groups := t.TempDir()
	folder := "g1"
	if err := os.MkdirAll(filepath.Join(groups, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	memContent := "hello memory"
	if err := os.WriteFile(filepath.Join(groups, folder, "MEMORY.md"), []byte(memContent), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, folder)
	body := w.Body.String()
	if strings.Contains(body, "Invalid group path") {
		t.Errorf("valid folder rejected: body = %q", body)
	}
	if !strings.Contains(body, memContent) {
		t.Errorf("MEMORY.md content missing: body = %q", body)
	}
}

// dashd has no auth; proxyd fronts it. This regression guard asserts that.
// See diary 2026-04-09.
func TestDashNoAuthGate(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	for _, path := range []string{"/dash/", "/dash/status/", "/dash/tasks/", "/dash/memory/", "/health"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("%s without auth: status = %d (dashd must not gate; proxyd fronts it)", path, w.Code)
		}
	}
}

func TestMdSummaryYAML(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "inline",
			content: "---\ntitle: foo\nsummary: hello world\n---\nbody",
			want:    "hello world",
		},
		{
			name:    "quoted inline",
			content: "---\nsummary: \"quoted text\"\n---\nbody",
			want:    "quoted text",
		},
		{
			name:    "multiline block",
			content: "---\nsummary: |\n  first line\n  second line\n---\nbody",
			want:    "first line second line",
		},
		{
			name:    "no yaml, first non-empty line",
			content: "\n\nplain first line\nsecond",
			want:    "plain first line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".md")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got := mdSummary(path)
			if got != tc.want {
				t.Errorf("mdSummary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandleStatusDBError(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`DROP TABLE channels`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db, dbPath: ":memory:"}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d (handler must not panic on DB errors)", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "Status") {
		t.Errorf("body missing page header: %q", body)
	}
}

func TestRenderMemorySectionClaudeMissing(t *testing.T) {
	groups := t.TempDir()
	folder := "g1"
	if err := os.MkdirAll(filepath.Join(groups, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groups, folder, "MEMORY.md"), []byte("mem"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, folder)
	body := w.Body.String()
	if strings.Contains(body, "Invalid group path") {
		t.Errorf("valid folder rejected: body = %q", body)
	}
	if !strings.Contains(body, "MEMORY.md") {
		t.Errorf("MEMORY.md header missing: body = %q", body)
	}
	if strings.Contains(body, "CLAUDE.md") {
		t.Errorf("CLAUDE.md section present without file: body = %q", body)
	}
}
