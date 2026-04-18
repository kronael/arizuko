package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// testDBFile returns a file-backed SQLite DB. Needed when the handler runs
// nested queries (outer rows iterator + inner query): with a shared
// :memory: each new pool connection opens a *different* in-memory DB.
func testDBFile(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE groups (folder TEXT PRIMARY KEY, name TEXT, added_at TEXT, parent TEXT, state TEXT NOT NULL DEFAULT 'active')`,
		`CREATE TABLE sessions (group_folder TEXT PRIMARY KEY, session_id TEXT)`,
		`CREATE TABLE channels (name TEXT, url TEXT)`,
		`CREATE TABLE scheduled_tasks (id TEXT PRIMARY KEY, owner TEXT, chat_jid TEXT, prompt TEXT, cron TEXT, next_run TEXT, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE messages (id TEXT PRIMARY KEY, chat_jid TEXT, sender TEXT, content TEXT, timestamp TEXT, source TEXT NOT NULL DEFAULT '', verb TEXT)`,
		`CREATE TABLE chats (jid TEXT PRIMARY KEY, errored INTEGER DEFAULT 0)`,
		`CREATE TABLE task_run_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, run_at TEXT, duration_ms INTEGER, status TEXT, result TEXT, error TEXT)`,
		`CREATE TABLE routes (id INTEGER PRIMARY KEY AUTOINCREMENT, seq INTEGER DEFAULT 0, match TEXT, target TEXT, impulse_config TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

// --- data-path tests for row-rendering functions ---

func TestStatusWithChannels(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO channels(name, url) VALUES('tel', 'http://t/'), ('disc', 'http://d/')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chats(jid, errored) VALUES('a', 0), ('b', 1)`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db, dbPath: "/tmp/x.db"}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"tel", "http://t/", "disc", "banner-warn", "/tmp/x.db"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body: %s", want, body)
		}
	}
}

func TestStatusNoChannelsErrBanner(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, dbPath: ":memory:"}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("expected banner-err for zero channels: %s", w.Body.String())
	}
}

func TestTasksPartialRows(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, cron, next_run, status, created_at)
		 VALUES('t1', 'g1', 'c', 'p', '* * * * *', '2030-01-01', 'active', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	// row with NULL cron/next_run to exercise nullStr branch
	if _, err := db.Exec(
		`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, status, created_at)
		 VALUES('t2', 'g2', 'c', 'p', 'paused', '2026-01-02')`); err != nil {
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
	for _, want := range []string{"t1", "g1", "* * * * *", "active", "t2", "g2", "paused"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q: %s", want, body)
		}
	}
}

func TestTasksPartialXSS(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO scheduled_tasks(id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES('<script>', '</td>', '', '', 'x', 'active', '<b>')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/tasks/x/list", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if strings.Contains(body, "<script>") {
		t.Errorf("unescaped <script> in output: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped script tag: %s", body)
	}
}

func TestActivityFullPage(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO messages(id, chat_jid, sender, content, timestamp, source, verb)
		 VALUES('m1', 'c1', 's1', 'hi there', '2026-01-01T00:00:00Z', 'tel', 'say')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/activity/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Activity", "hx-get=\"/dash/activity/x/recent\"", "c1", "s1", "hi there", "tel"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body: %s", want, body)
		}
	}
}

func TestActivityPartialRows(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	// long content to exercise substr LIMIT
	long := strings.Repeat("a", 200)
	if _, err := db.Exec(
		`INSERT INTO messages(id, chat_jid, sender, content, timestamp, source) VALUES('m1','c','s',?,'2026-01-01','tel')`,
		long); err != nil {
		t.Fatal(err)
	}
	// null verb
	if _, err := db.Exec(
		`INSERT INTO messages(id, chat_jid, sender, content, timestamp, source) VALUES('m2','c','s','short','2026-01-02','disc')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/activity/x/recent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if strings.Contains(body, strings.Repeat("a", 100)) {
		t.Errorf("content not truncated by substr: %s", body)
	}
	if !strings.Contains(body, "short") {
		t.Errorf("missing m2 row: %s", body)
	}
	if !strings.Contains(body, "disc") {
		t.Errorf("missing source: %s", body)
	}
}

func TestActivityPartialXSS(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO messages(id, chat_jid, sender, content, timestamp, source, verb) VALUES('m1','<c>','<s>','<b>evil</b>','t','<src>','<v>')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/activity/x/recent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	for _, raw := range []string{"<c>", "<s>", "<b>evil</b>", "<src>", "<v>"} {
		if strings.Contains(body, raw) {
			t.Errorf("unescaped %q in body: %s", raw, body)
		}
	}
}

// --- groups + routes ---

func TestGroupsWithRoutes(t *testing.T) {
	db := testDBFile(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO groups(folder, name, added_at, parent, state) VALUES('root','Root','',NULL,'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO groups(folder, name, added_at, parent, state) VALUES('root/child','Child','','root','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO routes(seq, match, target) VALUES(1, 'tel:*', 'root')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO routes(seq, match, target) VALUES(2, 'disc:*', 'root/child')`); err != nil {
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
	for _, want := range []string{"root", "Root", "(root)", "Child", "tel:*", "disc:*"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body: %s", want, body)
		}
	}
}

func TestGroupsRoutesEmpty(t *testing.T) {
	db := testDBFile(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO groups(folder, name, added_at, parent, state) VALUES('g1','G1','',NULL,'active')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/groups/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "no routes") {
		t.Errorf("expected 'no routes': %s", w.Body.String())
	}
}

func TestWriteGroupRoutesQueryError(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`DROP TABLE routes`); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &dash{db: db}
	w := httptest.NewRecorder()
	d.writeGroupRoutes(w, "g1") // must not panic; renders inline error banner
	body := w.Body.String()
	if strings.Contains(body, "<table>") {
		t.Errorf("unexpected table on query error: %s", body)
	}
	if !strings.Contains(body, "banner-err") {
		t.Errorf("writeGroupRoutes did not surface DB error: %s", body)
	}
}

// --- memory handler with groups present ---

func TestHandleMemoryDropdown(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO groups(folder, name, added_at, parent, state) VALUES('alpha','A','',NULL,'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO groups(folder, name, added_at, parent, state) VALUES('beta','B','',NULL,'active')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db, groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/memory/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "<select") {
		t.Errorf("dropdown missing: %s", body)
	}
	for _, g := range []string{"alpha", "beta"} {
		if !strings.Contains(body, `value="`+g+`"`) {
			t.Errorf("option %q missing: %s", g, body)
		}
	}
	if strings.Contains(body, " selected") {
		t.Errorf("unexpected pre-selection: %s", body)
	}
}

func TestHandleMemorySelectedGroup(t *testing.T) {
	groups := t.TempDir()
	folder := "grp"
	if err := os.MkdirAll(filepath.Join(groups, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groups, folder, "MEMORY.md"), []byte("memdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO groups(folder, name, added_at, parent, state) VALUES(?, 'G','',NULL,'active')`, folder); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db, groupsDir: groups}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/memory/?group="+folder, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `value="grp" selected`) {
		t.Errorf("expected selected option: %s", body)
	}
	if !strings.Contains(body, "memdata") {
		t.Errorf("MEMORY.md content missing: %s", body)
	}
}

// --- renderEntries exercised via renderMemorySection ---

func TestRenderEntriesDiary(t *testing.T) {
	groups := t.TempDir()
	folder := "g1"
	diary := filepath.Join(groups, folder, "diary")
	if err := os.MkdirAll(diary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(diary, "20260101.md"), []byte("first line"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(diary, "20260102.md"), []byte("---\nsummary: yaml summary\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, folder)
	body := w.Body.String()
	for _, want := range []string{"Diary", "2 files", "20260101.md", "20260102.md", "first line", "yaml summary"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q: %s", want, body)
		}
	}
}

func TestRenderEntriesTruncates(t *testing.T) {
	groups := t.TempDir()
	folder := "g1"
	dir := filepath.Join(groups, folder, "episodes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// create > maxDirEntries files
	n := maxDirEntries + 5
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, "e"+pad(i)+".md")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, folder)
	body := w.Body.String()
	if !strings.Contains(body, "showing newest") {
		t.Errorf("expected truncation note: %s", body)
	}
}

func pad(i int) string {
	s := ""
	if i < 10 {
		s = "00"
	} else if i < 100 {
		s = "0"
	}
	return s + itoa(i)
}
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestRenderEntriesEmptyDir(t *testing.T) {
	groups := t.TempDir()
	folder := "g1"
	if err := os.MkdirAll(filepath.Join(groups, folder, "diary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// no .md files
	if err := os.WriteFile(filepath.Join(groups, folder, "diary", "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, folder)
	// nothing for "Diary" section should be emitted
	if strings.Contains(w.Body.String(), "<h2>Diary</h2>") {
		t.Errorf("diary header rendered for dir without md: %s", w.Body.String())
	}
}

// --- renderCappedFile truncation ---

func TestRenderCappedFileTruncation(t *testing.T) {
	groups := t.TempDir()
	folder := "g1"
	if err := os.MkdirAll(filepath.Join(groups, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxFileBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(groups, folder, "MEMORY.md"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, folder)
	body := w.Body.String()
	if !strings.Contains(body, "truncated at") {
		t.Errorf("expected truncation note: (body omitted)")
		_ = body
	}
}

// --- nullStr ---

func TestNullStr(t *testing.T) {
	if got := nullStr(sql.NullString{}); got != "" {
		t.Errorf("invalid NullString = %q, want empty", got)
	}
	if got := nullStr(sql.NullString{Valid: true, String: "v"}); got != "v" {
		t.Errorf("valid NullString = %q, want v", got)
	}
}

// --- loggingMiddleware ---

func TestLoggingMiddleware(t *testing.T) {
	called := false
	h := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(418)
		w.Write([]byte("ok"))
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-User-Sub", "user@example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !called {
		t.Fatal("handler not called")
	}
	if w.Code != 418 {
		t.Errorf("code = %d, want 418", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q", w.Body.String())
	}
}

// --- auth header pass-through: dashd trusts X-User-Sub from proxyd,
// but does not gate on it. Ensure handlers process requests with it set. ---

func TestDashHandlersIgnoreXAuthHeaders(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db, dbPath: ":memory:", groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	for _, p := range []string{"/dash/", "/dash/status/", "/dash/tasks/", "/dash/activity/", "/dash/groups/", "/dash/memory/", "/dash/tasks/x/list", "/dash/activity/x/recent"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("X-User-Sub", "attacker@example.com")
		req.Header.Set("X-User-Email", "attacker@example.com")
		req.Header.Set("X-Auth-Provider", "forged")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("%s: status = %d (dashd trusts proxyd, must not validate)", p, w.Code)
		}
	}
}

// --- portal content ---

func TestPortalAllDotsOk(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	// channels > 0, no errored chats, no failed tasks → statusDot=ok, tasksDot=ok
	if _, err := db.Exec(`INSERT INTO channels(name, url) VALUES('tel','http://t/')`); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	// each dot span should have class "dot ok"
	if !strings.Contains(body, `dot ok`) {
		t.Errorf("expected 'dot ok' classes: %s", body)
	}
}

func TestPortalFailedTasksDot(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO channels(name, url) VALUES('tel','http://t/')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(
			`INSERT INTO task_run_logs(task_id, run_at, status) VALUES('t', datetime('now'), 'error')`); err != nil {
			t.Fatal(err)
		}
	}
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `dot err`) {
		t.Errorf("expected 'dot err' class for >=3 failed tasks: %s", body)
	}
}

func TestPortalRejectsNonRoot(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	// path "/dash/bogus" matches "/dash/" prefix route, handler should 404
	req := httptest.NewRequest("GET", "/dash/bogus", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404 for non-root under /dash/, got %d", w.Code)
	}
}

// --- LIMIT 500 bound: confirm the handler caps rows. ---

func TestStatusChannelsLimit500(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	// Insert 600 channels; handler should cap at 500.
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO channels(name, url) VALUES(?, ?)`)
	for i := 0; i < 600; i++ {
		if _, err := stmt.Exec("ch"+itoa(i), "u"); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	tx.Commit()

	d := &dash{db: db, dbPath: ":memory:"}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	// count channel rows: every channel emits <tr><td>...chN...</td>
	n := strings.Count(body, "<tr><td>ch")
	if n != 500 {
		t.Errorf("channel rows = %d, want 500 (LIMIT bound)", n)
	}
}
