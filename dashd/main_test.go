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

// --- path-traversal guard tests ---

// Batch of traversal payloads that must all be rejected by the prefix check.
func TestRenderMemorySectionTraversalVariants(t *testing.T) {
	groups := t.TempDir()
	d := &dash{groupsDir: groups}
	cases := []string{
		"../../etc",
		"foo/../../etc",
		"g1/../../..",
		"..",
		"../",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			w := httptest.NewRecorder()
			d.renderMemorySection(w, input)
			body := w.Body.String()
			if !strings.Contains(body, "Invalid group path") {
				t.Errorf("traversal %q not rejected: body = %q", input, body)
			}
		})
	}
}

// Absolute-path input: filepath.Join treats the leading slash as a literal
// segment, so /etc/passwd becomes <groupsDir>/etc/passwd — still inside the
// sandbox. This test locks in that current behavior.
func TestRenderMemorySectionAbsolutePath(t *testing.T) {
	groups := t.TempDir()
	// create a real subdirectory and file at the sandboxed path so the read succeeds
	sub := filepath.Join(groups, "etc", "passwd")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "MEMORY.md"), []byte("sandboxed"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, "/etc/passwd")
	body := w.Body.String()
	if strings.Contains(body, "Invalid group path") {
		t.Errorf("absolute path unexpectedly rejected: body = %q", body)
	}
	if !strings.Contains(body, "sandboxed") {
		t.Errorf("expected to read sandboxed MEMORY.md, got: %q", body)
	}
	// Host /etc/passwd must NOT have been read — check a string unique to real passwd files
	if strings.Contains(body, "root:x:") {
		t.Errorf("host /etc/passwd leaked into response: %q", body)
	}
}

// URL-encoded traversal via the full handler: the query decoder turns
// ..%2F..%2Fetc back into ../../etc, which the guard then catches.
func TestHandleMemoryURLEncodedTraversal(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/memory/?group=..%2F..%2Fetc", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid group path") {
		t.Errorf("url-encoded traversal not rejected: body = %q", w.Body.String())
	}
}

// Symlink escape: a directory INSIDE groupsDir that is actually a symlink to
// somewhere outside. The string-prefix guard operates on the unresolved path
// and does NOT catch this — os.ReadFile follows the symlink.
// EXPECTED BEHAVIOR: guard should catch; if this test fails, it reveals a bug.
func TestRenderMemorySectionSymlinkEscape(t *testing.T) {
	if os.Getenv("SKIP_SYMLINK_TEST") != "" {
		t.Skip("symlinks unsupported")
	}
	groups := t.TempDir()
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "MEMORY.md")
	const secret = "TOP_SECRET_OUTSIDE_SANDBOX"
	if err := os.WriteFile(secretPath, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(groups, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, "escape")
	body := w.Body.String()
	if strings.Contains(body, secret) {
		t.Errorf("BUG: symlink escape leaked %q — guard does not resolve symlinks: body = %q",
			secret, body)
	}
}

// Empty / dot folder names collapse to groupsDir itself via the equal branch
// of the guard. Not a traversal, but documents what is currently allowed.
func TestRenderMemorySectionEmptyFolder(t *testing.T) {
	groups := t.TempDir()
	// place a MEMORY.md at the groupsDir root
	if err := os.WriteFile(filepath.Join(groups, "MEMORY.md"), []byte("root-memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	for _, input := range []string{"", "."} {
		t.Run("input="+input, func(t *testing.T) {
			w := httptest.NewRecorder()
			d.renderMemorySection(w, input)
			body := w.Body.String()
			if strings.Contains(body, "Invalid group path") {
				t.Errorf("%q unexpectedly rejected (equal-branch should allow)", input)
			}
			if !strings.Contains(body, "root-memory") {
				t.Errorf("%q did not read groupsDir/MEMORY.md: body = %q", input, body)
			}
		})
	}
}

// --- DB error swallowing tests ---

// Portal ignores errors from three QueryRow().Scan() calls. Verify the
// handler still returns 200 when the tables are missing, documenting the
// silent-swallow behavior: the dots render as "err" because counts stay 0.
func TestHandlePortalDBError(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	for _, tbl := range []string{"channels", "chats", "task_run_logs"} {
		if _, err := db.Exec(`DROP TABLE ` + tbl); err != nil {
			t.Fatal(err)
		}
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "arizuko") {
		t.Errorf("portal did not render despite DB errors: %q", body)
	}
	if !strings.Contains(body, "banner-err") {
		t.Errorf("portal did not surface DB error banner: %q", body)
	}
}

// writeTaskRows DOES surface DB errors in the response — lock that in.
func TestTasksPartialDBError(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE scheduled_tasks`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/tasks/x/list", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "error:") {
		t.Errorf("tasks partial did not surface DB error: %q", body)
	}
}

// writeActivityRows DOES surface DB errors — lock in.
func TestActivityPartialDBError(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE messages`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/activity/x/recent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "error:") {
		t.Errorf("activity partial did not surface DB error: %q", body)
	}
}

// handleGroups DOES surface DB errors in the body.
func TestHandleGroupsDBError(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE groups`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/groups/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "error:") {
		t.Errorf("groups handler did not surface DB error: %q", body)
	}
}

// handleMemory surfaces DB errors from the group-list query as an inline
// banner, and omits the dropdown.
func TestHandleMemoryDBError(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE groups`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db, groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/memory/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("status = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "<select") {
		t.Errorf("dropdown rendered despite DB error: %q", body)
	}
	if !strings.Contains(body, "banner-err") {
		t.Errorf("memory did not surface DB error banner: %q", body)
	}
}

// --- auth gate tests ---
// dashd intentionally has NO JWT middleware; proxyd fronts it. These tests
// guard that contract: bogus Authorization headers are neither honored nor
// rejected — every request is admitted regardless.

func TestDashIgnoresAuthHeader(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, dbPath: ":memory:", groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	cases := []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"bearer junk", "Bearer not.a.real.jwt"},
		{"bearer expired", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjF9.xxx"},
		{"raw secret", "super-secret"},
		{"malformed", "Basic foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/dash/status/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Errorf("status = %d (dashd must not gate on Authorization header)", w.Code)
			}
		})
	}
}

func TestMemoryPathAllowed(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"MEMORY.md", true},
		{".claude/CLAUDE.md", true},
		{"diary/2026-04-19.md", true},
		{"facts/foo.md", true},
		{"users/bob.md", true},
		{"episodes/week-1.md", true},
		{"", false},
		{"..", false},
		{"../etc/passwd", false},
		{"/absolute", false},
		{"diary/../MEMORY.md", false},
		{"diary/sub/nested.md", false},
		{"random.md", false},
		{"MEMORY", false},
		{".env", false},
		{"facts/foo.txt", false},
	}
	for _, c := range cases {
		got := memoryPathAllowed(c.rel)
		if got != c.want {
			t.Errorf("memoryPathAllowed(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func setupMemoryTest(t *testing.T) (*dash, string, *http.ServeMux) {
	t.Helper()
	groups := t.TempDir()
	folder := "g1"
	if err := os.MkdirAll(filepath.Join(groups, folder, "diary"), 0o755); err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	t.Cleanup(func() { db.Close() })
	d := &dash{db: db, groupsDir: groups}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	return d, folder, mux
}

func TestMemoryWrite(t *testing.T) {
	d, folder, mux := setupMemoryTest(t)

	body := "hello memory\n"
	req := httptest.NewRequest("PUT", "/dash/memory/"+folder+"/MEMORY.md", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(d.groupsDir, folder, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Errorf("file contents = %q, want %q", data, body)
	}
}

func TestMemoryWriteDiary(t *testing.T) {
	d, folder, mux := setupMemoryTest(t)

	req := httptest.NewRequest("PUT", "/dash/memory/"+folder+"/diary/2026-04-19.md", strings.NewReader("entry"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(d.groupsDir, folder, "diary", "2026-04-19.md")); err != nil {
		t.Fatalf("diary file missing: %v", err)
	}
}

func TestMemoryWriteRejectsTraversal(t *testing.T) {
	_, folder, mux := setupMemoryTest(t)

	cases := []string{
		"/dash/memory/" + folder + "/../../../etc/passwd",
		"/dash/memory/" + folder + "/random.md",
		"/dash/memory/" + folder + "/.env",
		"/dash/memory/../other/MEMORY.md",
	}
	for _, p := range cases {
		req := httptest.NewRequest("PUT", p, strings.NewReader("x"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusNoContent {
			t.Errorf("PUT %s should be rejected, got %d", p, w.Code)
		}
	}
}

func TestMemoryDelete(t *testing.T) {
	d, folder, mux := setupMemoryTest(t)
	path := filepath.Join(d.groupsDir, folder, "MEMORY.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("DELETE", "/dash/memory/"+folder+"/MEMORY.md", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be deleted, got err = %v", err)
	}
}

func TestMemoryDeleteRejectsTraversal(t *testing.T) {
	_, folder, mux := setupMemoryTest(t)
	req := httptest.NewRequest("DELETE", "/dash/memory/"+folder+"/../other/MEMORY.md", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNoContent {
		t.Errorf("DELETE traversal should be rejected, got %d", w.Code)
	}
}

func TestMemoryRejectsSymlink(t *testing.T) {
	d, folder, mux := setupMemoryTest(t)
	// Create a symlink MEMORY.md -> outside file
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d.groupsDir, folder, "MEMORY.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlink not supported")
	}

	req := httptest.NewRequest("PUT", "/dash/memory/"+folder+"/MEMORY.md", strings.NewReader("pwned"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNoContent {
		t.Errorf("PUT to symlink should be rejected, got %d", w.Code)
	}
	// Verify outside file untouched
	data, _ := os.ReadFile(outside)
	if string(data) != "secret" {
		t.Errorf("outside file modified: %q", data)
	}
}
