package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// runedDB builds an in-memory runed.db with the spawns schema the runs view
// reads.
func runedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE spawns (
		run_id TEXT PRIMARY KEY,
		folder TEXT NOT NULL,
		topic TEXT NOT NULL DEFAULT '',
		container_name TEXT NOT NULL DEFAULT '',
		session_log_id INTEGER,
		mcp_token_jti TEXT,
		session_id TEXT,
		state TEXT NOT NULL DEFAULT 'queued',
		outcome TEXT,
		exit_code INTEGER,
		steered INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		started_at TEXT,
		ended_at TEXT
	)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// TestRunedNilStore: with no runed.db wired in, the page renders a graceful
// "store unavailable" banner rather than erroring.
func TestRunedNilStore(t *testing.T) {
	db := routdDB(t) // routd.db only — dbRuned left nil
	defer db.Close()
	d := &dash{db: db, dbRoutd: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := asOperator(httptest.NewRequest("GET", "/dash/runed/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "runed store unavailable") {
		t.Errorf("nil dbRuned should render the unavailable banner: %s", w.Body.String())
	}
}

// TestRunedActiveRuns: queued/running spawns surface in the active table with a
// per-folder kill form and a truncated run id.
func TestRunedActiveRuns(t *testing.T) {
	rdb := runedDB(t)
	defer rdb.Close()
	if _, err := rdb.Exec(
		`INSERT INTO spawns (run_id, folder, state, created_at, started_at) VALUES
		 ('run-abcdef0123456789','corp/eng','running','2026-06-16T10:00:00Z','2026-06-16T10:00:05Z'),
		 ('run-queued00000000','solo/inbox','queued','2026-06-16T10:01:00Z',NULL)`); err != nil {
		t.Fatal(err)
	}
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db, dbRuned: rdb}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := asOperator(httptest.NewRequest("GET", "/dash/runed/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Active runs") {
		t.Errorf("missing Active runs heading")
	}
	if !strings.Contains(body, "corp/eng") || !strings.Contains(body, "solo/inbox") {
		t.Errorf("missing active run folders: %s", body)
	}
	if !strings.Contains(body, `action="/dash/runed/kill"`) {
		t.Errorf("missing kill form")
	}
	// run_id truncated to first 8 chars.
	if !strings.Contains(body, "run-abcd") || strings.Contains(body, "run-abcdef0123456789") {
		t.Errorf("run id should be truncated to 8 chars: %s", body)
	}
}

// TestRunedRecentRuns: exited/error/killed spawns surface in the recent table
// with outcome, exit code, and a computed duration.
func TestRunedRecentRuns(t *testing.T) {
	rdb := runedDB(t)
	defer rdb.Close()
	if _, err := rdb.Exec(
		`INSERT INTO spawns (run_id, folder, state, outcome, exit_code, created_at, started_at, ended_at) VALUES
		 ('r-ok','corp/eng','exited','completed',0,'2026-06-16T09:00:00Z','2026-06-16T09:00:00Z','2026-06-16T09:00:42Z'),
		 ('r-err','solo/inbox','error','crash',1,'2026-06-16T09:05:00Z','2026-06-16T09:05:00Z','2026-06-16T09:06:00Z')`); err != nil {
		t.Fatal(err)
	}
	// An active run must NOT appear in the recent table.
	if _, err := rdb.Exec(
		`INSERT INTO spawns (run_id, folder, state, created_at) VALUES ('r-live','x','running','2026-06-16T09:10:00Z')`); err != nil {
		t.Fatal(err)
	}
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db, dbRuned: rdb}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := asOperator(httptest.NewRequest("GET", "/dash/runed/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	recent := body[strings.Index(body, "Recent runs"):]
	if !strings.Contains(recent, "completed") || !strings.Contains(recent, "crash") {
		t.Errorf("missing recent outcomes: %s", recent)
	}
	if !strings.Contains(recent, "42s") || !strings.Contains(recent, "1m") {
		t.Errorf("missing computed durations: %s", recent)
	}
	if strings.Contains(recent, "r-live") {
		t.Errorf("active run leaked into recent table: %s", recent)
	}
}

// TestRunedKillNoURL: with RUNED_URL unset, the kill POST is 503.
func TestRunedKillNoURL(t *testing.T) {
	rdb := runedDB(t)
	defer rdb.Close()
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db, dbRuned: rdb} // runedURL == ""
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := strings.NewReader("folder=corp/eng")
	req := asOperator(httptest.NewRequest("POST", "/dash/runed/kill", form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// TestRunedKillProxies: with RUNED_URL set, the kill POST proxies to runed's
// /v1/runs/stop and redirects with the killed banner.
func TestRunedKillProxies(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/stop" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"killed":true,"run_id":"run-1"}`))
	}))
	defer upstream.Close()

	rdb := runedDB(t)
	defer rdb.Close()
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db, dbRuned: rdb, runedURL: upstream.URL}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := strings.NewReader("folder=corp/eng")
	req := asOperator(httptest.NewRequest("POST", "/dash/runed/kill", form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "msg=killed") {
		t.Errorf("redirect = %q, want killed banner", loc)
	}
	if !strings.Contains(gotBody, `"folder":"corp/eng"`) {
		t.Errorf("upstream body = %q, want folder corp/eng", gotBody)
	}
}

// TestRunedNonOperatorForbidden: the runed cockpit is operator-only.
func TestRunedNonOperatorForbidden(t *testing.T) {
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/runed/", nil)
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

// TestRunedKillNonOperatorForbidden: POST /dash/runed/kill is operator-only.
func TestRunedKillNonOperatorForbidden(t *testing.T) {
	rdb := runedDB(t)
	defer rdb.Close()
	db := routdDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db, dbRuned: rdb}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := strings.NewReader("folder=corp/eng")
	req := httptest.NewRequest("POST", "/dash/runed/kill", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
