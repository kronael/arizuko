package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// --- data-path tests for row-rendering functions ---

func TestTasksPartialRows(t *testing.T) {
	db := routdDB(t)
	nextRun := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	seedScheduledTask(t, db, core.Task{
		ID: "t1", Owner: "g1", ChatJID: "c", Prompt: "p", Cron: "* * * * *",
		NextRun: &nextRun, Status: "active",
		Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	// A task carrying NO cron and no next_run — the one-shot that has fired, and
	// the branch that renders an absent attribute.
	seedScheduledTask(t, db, core.Task{
		ID: "t2", Owner: "g2", ChatJID: "c", Prompt: "p", Status: "paused",
		Created: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/tasks/x/list", nil))
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
	db := routdDB(t)
	seedScheduledTask(t, db, core.Task{
		ID: "<script>", Owner: "</td>", Cron: "x", Status: "active",
	})
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/tasks/x/list", nil))
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
	db := routdDB(t)
	seedRoutdMessage(t, db, core.Message{
		ID: "m1", ChatJID: "c1", Sender: "s1", Content: "hi there",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Source: "tel", Verb: "say",
	})
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/activity/", nil))
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

// TestActivityPagination: a page past activityPageSize rows renders an
// "older" link, whose target shows the next page and stays static (no
// auto-refresh hx-trigger, so it doesn't get polled back to the newest page).
func TestActivityPagination(t *testing.T) {
	db := routdDB(t)
	const total = activityPageSize + 5
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range total {
		ts := base.Add(time.Duration(i) * time.Second)
		seedRoutdMessage(t, db, core.Message{
			ID: fmt.Sprintf("m%d", i), ChatJID: "c1", Sender: "s1", Content: "msg",
			Timestamp: ts, Source: "tel", Verb: "say",
		})
	}
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := asOperator(httptest.NewRequest("GET", "/dash/activity/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(body, `href="/dash/activity/?before=`) {
		t.Fatalf("expected an older-page link with %d rows: %s", total, body)
	}
	if !strings.Contains(body, `hx-trigger="every 10s"`) {
		t.Errorf("first page should still auto-refresh: %s", body)
	}

	// Extract the cursor and fetch the older page.
	start := strings.Index(body, `href="/dash/activity/?before=`) + len(`href="/dash/activity/?before=`)
	end := strings.Index(body[start:], `"`)
	cursor := body[start : start+end]

	req2 := asOperator(httptest.NewRequest("GET", "/dash/activity/?before="+cursor, nil))
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	body2 := w2.Body.String()
	if w2.Code != 200 {
		t.Fatalf("older page status = %d", w2.Code)
	}
	if strings.Contains(body2, `hx-trigger="every 10s"`) {
		t.Errorf("an older page must not auto-refresh (would poll back to newest): %s", body2)
	}
	if !strings.Contains(body2, core.TS(base)) {
		t.Errorf("oldest message should appear on the second page: %s", body2)
	}
	if got := strings.Count(body2, "<td><abbr"); got != 5 {
		t.Errorf("second page rows = %d, want 5 (55 total - 50 on page one)", got)
	}
}

func TestActivityPartialRows(t *testing.T) {
	db := routdDB(t)
	// long content to exercise substr LIMIT
	long := strings.Repeat("a", 200)
	seedRoutdMessage(t, db, core.Message{
		ID: "m1", ChatJID: "c", Sender: "s", Content: long,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Source: "tel",
	})
	seedRoutdMessage(t, db, core.Message{
		ID: "m2", ChatJID: "c", Sender: "s", Content: "short",
		Timestamp: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Source: "disc",
	})
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/activity/x/recent", nil))
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
	db := routdDB(t)
	seedRoutdMessage(t, db, core.Message{
		ID: "m1", ChatJID: "<c>", Sender: "<s>", Content: "<b>evil</b>",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Source: "<src>", Verb: "<v>",
	})
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
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
	db := routdDB(t)
	seedFolder(t, db, "root")
	seedFolder(t, db, "root/child")
	seedRoute(t, db, core.Route{Seq: 1, Match: "tel:*", Target: "root"})
	seedRoute(t, db, core.Route{Seq: 2, Match: "disc:*", Target: "root/child"})
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/groups/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"root", "(root)", "root/child", "tel:*", "disc:*"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body: %s", want, body)
		}
	}
}

func TestGroupsRoutesEmpty(t *testing.T) {
	db := routdDB(t)
	seedFolder(t, db, "g1")
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/groups/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "no routes") {
		t.Errorf("expected 'no routes': %s", w.Body.String())
	}
}

// --- memory handler with groups present ---

func TestHandleMemoryDropdown(t *testing.T) {
	db := routdDB(t)
	seedFolder(t, db, "alpha")
	seedFolder(t, db, "beta")
	d := &dash{routd: db, groupsDir: t.TempDir(), routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/memory/", nil))
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
	db := routdDB(t)
	seedFolder(t, db, folder)
	d := &dash{routd: db, groupsDir: groups, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/memory/?group="+folder, nil))
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
	for i := range n {
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

// --- PERSONA.md in memory section ---

func TestRenderMemorySectionPersona(t *testing.T) {
	groups := t.TempDir()
	folder := "g1"
	if err := os.MkdirAll(filepath.Join(groups, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groups, folder, "PERSONA.md"), []byte("personadata"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	w := httptest.NewRecorder()
	d.renderMemorySection(w, folder)
	body := w.Body.String()
	if !strings.Contains(body, "personadata") {
		t.Errorf("PERSONA.md content missing: %s", body)
	}
	if !strings.Contains(body, "PERSONA.md") {
		t.Errorf("PERSONA.md label missing: %s", body)
	}
}

func TestMemoryWritePersona(t *testing.T) {
	d, folder, mux := setupMemoryTest(t)

	body := "---\nname: Atlas\n---\nYou are Atlas.\n"
	req := authMemReq("PUT", "/dash/memory/"+folder+"/PERSONA.md", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(d.groupsDir, folder, "PERSONA.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Errorf("got %q, want %q", string(data), body)
	}
}

// --- transit-proof gate: when a Verifier (ks) is set, dashd VERIFIES proxyd's
// service:proxyd transit bearer. A raw/forged X-User-Sub with no proof (a request
// that bypassed proxyd) must be REJECTED — it can no longer impersonate an
// operator. This is the proof the defense-in-depth gap is closed. ---

// With a verifier configured (ks non-nil), a forged X-User-Sub carrying NO
// transit bearer (a request that bypassed proxyd) is REJECTED — it can no longer
// impersonate an operator. This is the proof the defense-in-depth gap is closed.
func TestDashRejectsUnsignedXUserSub(t *testing.T) {
	ks, _ := proxydBearerKS(t)
	db := routdDB(t)
	d := &dash{routd: db, groupsDir: t.TempDir(), ks: ks, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	for _, p := range []string{"/dash/", "/dash/status/", "/dash/tasks/", "/dash/activity/", "/dash/groups/", "/dash/memory/", "/dash/tasks/x/list", "/dash/activity/x/recent"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("X-User-Sub", "attacker@example.com")
		req.Header.Set("X-User-Email", "attacker@example.com")
		req.Header.Set("X-Auth-Provider", "forged")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		// guard redirects unproven callers to /auth/login (303).
		if w.Code == 200 {
			t.Errorf("%s: status = 200 — forged X-User-Sub was trusted (impersonation gap open)", p)
		}
	}
}

// A request proving it transited proxyd (valid service:proxyd bearer) carrying
// the stamped end-user identity passes the guard and reaches the handler (200).
func TestDashAcceptsTransitProvenXUserSub(t *testing.T) {
	ks, tok := proxydBearerKS(t)
	db := routdDB(t)
	d := &dash{routd: db, groupsDir: t.TempDir(), ks: ks, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/status/", nil)
	req.Header.Set("X-User-Sub", "op@example.com")
	req.Header.Set("X-User-Name", "Op")
	req.Header.Set("X-User-Groups", "[]")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("transit-proven request: status = %d, want 200 (body=%q)", w.Code, w.Body.String())
	}
}

// /health MUST stay reachable with NO signature — the container healthcheck
// hits it. Guarding it would mark the container unhealthy in prod.
func TestDashHealthExemptFromGuard(t *testing.T) {
	db := routdDB(t)
	d := &dash{routd: db, groupsDir: t.TempDir(), routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("/health with no sig: status = %d, want 200 (healthcheck must not be guarded)", w.Code)
	}
}

// --- portal content ---

func TestPortalAllDotsOk(t *testing.T) {
	db := routdDB(t)
	// no errored chats, no failed tasks → statusDot=ok, tasksDot=ok
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `dot dot-ok`) {
		t.Errorf("expected 'dot dot-ok' classes: %s", body)
	}
}

func TestPortalFailedTasksDot(t *testing.T) {
	db := routdDB(t)
	seedScheduledTask(t, db, core.Task{
		ID: "t", Owner: "op", ChatJID: "web:a", Prompt: "p",
	})
	for range 3 {
		if err := store.NewOver(db).RecordTaskRun(store.TaskRunLog{
			TaskID: "t", Status: "error",
		}); err != nil {
			t.Fatal(err)
		}
	}
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `dot dot-err`) {
		t.Errorf("expected 'dot dot-err' class for >=3 failed tasks: %s", body)
	}
}

func TestPortalRejectsNonRoot(t *testing.T) {
	db := routdDB(t)
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
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
