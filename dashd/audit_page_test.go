package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/kronael/arizuko/audit"
)

// The audit page no longer reads routd.db: it fans GET /v1/audit out to routd,
// runed and authd and merges. The fixtures follow — each fake source is a real
// in-memory audit_log served through audit.Query, the same function the real
// daemons serve it through, so these exercise the actual query semantics
// (subtree folder match, id cursor, limit clamp) rather than a stub that agrees
// with the page by construction.

// auditDB is a migrated routd.db standing in for any audit_log owner: the four
// tables are one shape, replicated per owner DB (spec 5/I), so routd's chain is
// the honest source for all of them.
func auditDB(t *testing.T) *sql.DB { return routdDB(t) }

// seedAudit inserts one row at an explicit timestamp. ts is explicit because
// the merge orders on created_at across sources; a default-now stamp would make
// cross-source ordering a race on insert speed.
func seedAudit(t *testing.T, db *sql.DB, ts, category, action, actor, folder, outcome string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO audit_log (created_at, category, action, actor, folder, outcome)
		 VALUES (?, ?, ?, ?, ?, ?)`, ts, category, action, actor, folder, outcome); err != nil {
		t.Fatal(err)
	}
	// A seed that inserted nothing must fail the test, not pass it silently:
	// four vacuous audit tests shipped this week because the table they
	// asserted about was empty.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("seedAudit wrote no row (count=%d, err=%v)", n, err)
	}
}

// auditSourceOf serves GET /v1/audit off db through audit.Query, translating
// the same query params the page sends.
func auditSourceOf(t *testing.T, db *sql.DB) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/audit", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var before, limit int64
		fmt.Sscanf(q.Get("before_id"), "%d", &before)
		fmt.Sscanf(q.Get("limit"), "%d", &limit)
		rows, err := audit.Query(context.Background(), db, audit.Filter{
			Folder:   q.Get("folder"),
			Category: q.Get("category"),
			Actor:    q.Get("actor"),
			BeforeID: before,
			Limit:    int(limit),
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// auditDash builds a dash whose four sources point at the given base URLs.
// An empty URL models an unconfigured daemon. A non-empty onbod URL also sets
// dbOnbod, since auditSources treats an absent onbod store as "the onboarding
// profile is off" and drops the source rather than reporting it dead.
func auditDash(t *testing.T, routd, runed, authd, onbod string) *dash {
	t.Helper()
	d := &dash{routdURL: routd, runedURL: runed, authdURL: authd, onbodURL: onbod}
	if onbod != "" {
		d.dbOnbod = auditDB(t)
	}
	return d
}

func auditGetOn(t *testing.T, d *dash, url string) string {
	t.Helper()
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", url, nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d for %s", w.Code, url)
	}
	return w.Body.String()
}

// bodyRows counts rendered <tbody> rows. Every content assertion below counts
// first and inspects second: an assertion that a string is ABSENT passes just
// as happily on an empty table, which is how a leak test becomes vacuous.
func bodyRows(t *testing.T, body string) int {
	t.Helper()
	_, after, ok := strings.Cut(body, "<tbody>")
	if !ok {
		return 0
	}
	inner, _, ok := strings.Cut(after, "</tbody>")
	if !ok {
		t.Fatalf("unterminated tbody: %s", body)
	}
	return strings.Count(inner, "<tr>")
}

// TestAuditFederatesAllFourSources is the point of BUGS F29 and its F35 tail:
// runed's, authd's AND onbod's rows reach the operator. It counts before it
// inspects, so a page that rendered nothing cannot pass.
func TestAuditFederatesAllFourSources(t *testing.T) {
	rdb, ndb, adb, odb := auditDB(t), auditDB(t), auditDB(t), auditDB(t)
	seedAudit(t, rdb, "2026-08-01T10:00:00.000Z", "mutation", "routes:create", "github:op", "alice", "ok")
	seedAudit(t, ndb, "2026-08-01T11:00:00.000Z", "agent", "run.kill", "github:op", "alice", "ok")
	seedAudit(t, adb, "2026-08-01T12:00:00.000Z", "authn", "login", "user:google:114", "alice", "ok")
	seedAudit(t, odb, "2026-08-01T13:00:00.000Z", "mutation", "invite.consume", "user:github:bob", "alice", "ok")

	d := auditDash(t, auditSourceOf(t, rdb).URL, auditSourceOf(t, ndb).URL,
		auditSourceOf(t, adb).URL, auditSourceOf(t, odb).URL)
	body := auditGetOn(t, d, "/dash/audit/")

	if n := bodyRows(t, body); n != 4 {
		t.Fatalf("rendered %d rows, want 4 (one per source): %s", n, body)
	}
	for _, want := range []string{
		"routes:create", "run.kill", "login", "invite.consume",
		"routd", "runed", "authd", "onbod",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q — federation dropped a source: %s", want, body)
		}
	}
}

// TestAuditMergedNewestFirst: the merge orders across sources, not within them.
func TestAuditMergedNewestFirst(t *testing.T) {
	rdb, ndb := auditDB(t), auditDB(t)
	seedAudit(t, rdb, "2026-08-01T10:00:00.000Z", "mutation", "oldest-routd", "op", "f", "ok")
	seedAudit(t, ndb, "2026-08-01T11:00:00.000Z", "agent", "middle-runed", "op", "f", "ok")
	seedAudit(t, rdb, "2026-08-01T12:00:00.000Z", "mutation", "newest-routd", "op", "f", "ok")

	d := auditDash(t, auditSourceOf(t, rdb).URL, auditSourceOf(t, ndb).URL, "", "")
	body := auditGetOn(t, d, "/dash/audit/")
	if n := bodyRows(t, body); n != 3 {
		t.Fatalf("rendered %d rows, want 3: %s", n, body)
	}
	iNew := strings.Index(body, "newest-routd")
	iMid := strings.Index(body, "middle-runed")
	iOld := strings.Index(body, "oldest-routd")
	if !(iNew < iMid && iMid < iOld) {
		t.Errorf("interleave wrong: newest=%d middle=%d oldest=%d — a per-source merge would "+
			"have kept routd's two adjacent: %s", iNew, iMid, iOld, body)
	}
}

// TestAuditSourceFailureIsLoud: a source that cannot be read must produce a
// banner naming it, NOT an empty section. Rendering the survivors silently would
// tell an operator "nothing happened in runed" when runed simply did not answer.
func TestAuditSourceFailureIsLoud(t *testing.T) {
	rdb := auditDB(t)
	seedAudit(t, rdb, "2026-08-01T10:00:00.000Z", "mutation", "survivor-row", "op", "f", "ok")
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer dead.Close()

	d := auditDash(t, auditSourceOf(t, rdb).URL, dead.URL, "", "")
	body := auditGetOn(t, d, "/dash/audit/")

	if !strings.Contains(body, "audit source unavailable") || !strings.Contains(body, "runed") {
		t.Errorf("dead source must be named in a banner: %s", body)
	}
	// The survivor still renders — a failing source degrades the page, not
	// blanks it.
	if n := bodyRows(t, body); n != 1 {
		t.Fatalf("rendered %d rows, want the 1 surviving row: %s", n, body)
	}
	if !strings.Contains(body, "survivor-row") {
		t.Errorf("surviving source dropped: %s", body)
	}
}

// TestAuditUnconfiguredSourceIsLoud: an empty base URL is reported, not skipped
// in silence. compose always runs these three, so a blank one is a
// misconfiguration the operator must see rather than a quietly narrower page.
func TestAuditUnconfiguredSourceIsLoud(t *testing.T) {
	d := auditDash(t, "", "", "", "")
	body := auditGetOn(t, d, "/dash/audit/")
	for _, name := range []string{"routd", "runed", "authd"} {
		if !strings.Contains(body, name+": no URL configured") {
			t.Errorf("unconfigured %s not reported: %s", name, body)
		}
	}
	if n := bodyRows(t, body); n != 0 {
		t.Errorf("rendered %d rows with no sources: %s", n, body)
	}
}

// TestAuditOnbodProfileOffIsNotASource: onbod is an optional compose profile
// (ONBOARDING_ENABLED, default off). An instance that never ran onboarding must
// not carry a permanent "audit source unavailable — onbod" banner; an instance
// that DID must still hear about a dead one. dbOnbod is the profile signal.
func TestAuditOnbodProfileOffIsNotASource(t *testing.T) {
	rdb := auditDB(t)
	seedAudit(t, rdb, "2026-08-01T10:00:00.000Z", "mutation", "survivor-row", "op", "f", "ok")

	off := auditDash(t, auditSourceOf(t, rdb).URL, "", "", "")
	body := auditGetOn(t, off, "/dash/audit/")
	if strings.Contains(body, "audit source unavailable — onbod") {
		t.Errorf("onboarding-off instance reports onbod as a failed source: %s", body)
	}

	// Profile ON but the daemon is unreachable: that IS a failure, and silence
	// would tell the operator "no admissions happened" when onbod did not answer.
	on := auditDash(t, auditSourceOf(t, rdb).URL, "", "", "")
	on.dbOnbod = auditDB(t)
	body = auditGetOn(t, on, "/dash/audit/")
	if !strings.Contains(body, "onbod: no URL configured") {
		t.Errorf("onboarding-on instance hid a dead onbod: %s", body)
	}
}

// TestAuditCategoryFilter: ?cat= reaches the sources and the option renders
// selected.
func TestAuditCategoryFilter(t *testing.T) {
	rdb := auditDB(t)
	seedAudit(t, rdb, "2026-08-01T10:00:00.000Z", "mutation", "routd.retry", "a", "alice", "ok")
	seedAudit(t, rdb, "2026-08-01T11:00:00.000Z", "authz", "grant.add", "b", "bob", "ok")

	d := auditDash(t, auditSourceOf(t, rdb).URL, "", "", "")
	body := auditGetOn(t, d, "/dash/audit/?cat=authz")
	if n := bodyRows(t, body); n != 1 {
		t.Fatalf("rendered %d rows, want 1 authz row: %s", n, body)
	}
	if !strings.Contains(body, "grant.add") {
		t.Errorf("authz row missing: %s", body)
	}
	if strings.Contains(body, "routd.retry") {
		t.Errorf("mutation row leaked under ?cat=authz: %s", body)
	}
	if !strings.Contains(body, `value="authz" selected`) {
		t.Errorf("dropdown should mark authz selected: %s", body)
	}
}

// TestAuditActorFilter: ?actor= is a substring match forwarded to the sources.
func TestAuditActorFilter(t *testing.T) {
	rdb := auditDB(t)
	seedAudit(t, rdb, "2026-08-01T10:00:00.000Z", "mutation", "act-alice", "alice", "grp1", "ok")
	seedAudit(t, rdb, "2026-08-01T11:00:00.000Z", "authz", "act-bob", "bob", "grp2", "ok")

	d := auditDash(t, auditSourceOf(t, rdb).URL, "", "", "")
	body := auditGetOn(t, d, "/dash/audit/?actor=alice")
	if n := bodyRows(t, body); n != 1 {
		t.Fatalf("rendered %d rows, want 1: %s", n, body)
	}
	if !strings.Contains(body, "act-alice") {
		t.Errorf("alice row missing: %s", body)
	}
	if strings.Contains(body, "act-bob") {
		t.Errorf("bob row leaked under ?actor=alice: %s", body)
	}
}

// TestAuditFolderFilterIsSubtree: ?folder= bounds by SUBTREE, matching how a
// grant is written (acme/**) and how runed's ownsFolder contains a run. A
// sibling folder sharing a name prefix but not a path segment must NOT match —
// that is the difference between `LIKE 'grp%'` and `LIKE 'grp/%'`.
func TestAuditFolderFilterIsSubtree(t *testing.T) {
	rdb := auditDB(t)
	seedAudit(t, rdb, "2026-08-01T10:00:00.000Z", "mutation", "at-parent", "op", "acme", "ok")
	seedAudit(t, rdb, "2026-08-01T11:00:00.000Z", "mutation", "at-child", "op", "acme/support", "ok")
	seedAudit(t, rdb, "2026-08-01T12:00:00.000Z", "mutation", "at-lookalike", "op", "acmecorp", "ok")
	seedAudit(t, rdb, "2026-08-01T13:00:00.000Z", "mutation", "at-other", "op", "beta", "ok")

	d := auditDash(t, auditSourceOf(t, rdb).URL, "", "", "")
	body := auditGetOn(t, d, "/dash/audit/?folder=acme")
	if n := bodyRows(t, body); n != 2 {
		t.Fatalf("rendered %d rows, want 2 (acme + acme/support): %s", n, body)
	}
	for _, want := range []string{"at-parent", "at-child"} {
		if !strings.Contains(body, want) {
			t.Errorf("subtree row %q missing: %s", want, body)
		}
	}
	for _, leak := range []string{"at-lookalike", "at-other"} {
		if strings.Contains(body, leak) {
			t.Errorf("%q leaked under ?folder=acme: %s", leak, body)
		}
	}
}

// TestAuditPaginationCompositeCursor: the cursor is per-source, so paging
// through one busy daemon does not reset another. 60 routd rows plus 1 old
// runed row: page 1 is the newest 50 routd rows, page 2 must contain routd's
// remaining 10 AND the runed row, each exactly once.
func TestAuditPaginationCompositeCursor(t *testing.T) {
	rdb, ndb := auditDB(t), auditDB(t)
	// Timestamps ascend with the id, which is the real shape of an append-only
	// log: both created_at and the AUTOINCREMENT id are assigned at insert and
	// no writer supplies created_at. audit.Query pages on id while the merge
	// orders on created_at, and that co-monotonicity is what makes the two
	// agree within one source.
	for i := range 60 {
		seedAudit(t, rdb, fmt.Sprintf("2026-08-01T00:%02d:00.000Z", i),
			"mutation", fmt.Sprintf("routd-act%02d", i), "actor", "f", "ok")
	}
	seedAudit(t, ndb, "2026-07-01T00:00:00.000Z", "agent", "runed-oldest", "actor", "f", "ok")

	d := auditDash(t, auditSourceOf(t, rdb).URL, auditSourceOf(t, ndb).URL, "", "")
	page1 := auditGetOn(t, d, "/dash/audit/")
	if n := bodyRows(t, page1); n != 50 {
		t.Fatalf("page 1 rendered %d rows, want 50: %s", n, page1)
	}
	if !strings.Contains(page1, "older &rarr;") {
		t.Fatalf("61 rows should render an older link: %s", page1)
	}
	// The cursor names BOTH sources; a single scalar cursor could not.
	if !strings.Contains(page1, "routd%3A") {
		t.Errorf("older link should carry a per-source routd cursor: %s", page1)
	}

	page2 := auditGetOn(t, d, extractOlderHref(t, page1))
	if n := bodyRows(t, page2); n != 11 {
		t.Fatalf("page 2 rendered %d rows, want 11 (10 routd + 1 runed): %s", n, page2)
	}
	if !strings.Contains(page2, "runed-oldest") {
		t.Errorf("the runed row never surfaced — a routd-only cursor starved it: %s", page2)
	}
	if strings.Contains(page2, "older &rarr;") {
		t.Errorf("last page should not offer another: %s", page2)
	}
}

// extractOlderHref pulls the "older" link target out of a rendered page.
func extractOlderHref(t *testing.T, body string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, `<a class="btn" href="`)
	if !ok {
		t.Fatalf("no older link in body: %s", body)
	}
	href, _, ok := strings.Cut(after, `"`)
	if !ok {
		t.Fatalf("unterminated href: %s", body)
	}
	return strings.ReplaceAll(href, "&amp;", "&")
}

// TestAuditEmpty: reachable sources with no rows render the empty marker — and
// no "unavailable" banner, which is the distinction that makes the banner
// meaningful.
func TestAuditEmpty(t *testing.T) {
	d := auditDash(t, auditSourceOf(t, auditDB(t)).URL, "", "", "")
	body := auditGetOn(t, d, "/dash/audit/")
	if !strings.Contains(body, `class="empty"`) {
		t.Errorf("empty log should render the empty marker: %s", body)
	}
	if strings.Contains(body, "routd: ") {
		t.Errorf("a reachable-but-empty source must not report as unavailable: %s", body)
	}
}

// TestAuditNonOperatorForbidden: the page is operator-only, and stays so now
// that it holds three daemons' trails instead of one.
func TestAuditNonOperatorForbidden(t *testing.T) {
	d := auditDash(t, auditSourceOf(t, auditDB(t)).URL, "", "", "")
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/audit/", nil)
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestParseAuditCursor(t *testing.T) {
	got := parseAuditCursor("routd:123,runed:45, authd:7 ")
	want := map[string]int64{"routd": 123, "runed": 45, "authd": 7}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("cursor[%s] = %d, want %d", k, got[k], v)
		}
	}
	// Garbage is skipped, not fatal — a hand-edited URL must not 500 the page.
	if n := len(parseAuditCursor("nonsense,routd:abc,:9,runed:0")); n != 0 {
		t.Errorf("malformed cursor yielded %d entries, want 0", n)
	}
}

// TestNextAuditCursorHoldsUncontributedSource: a source that contributed no row
// to this page keeps its previous cursor. Resetting it would replay its newest
// rows on every "older" click — an infinite page that never reaches the past.
func TestNextAuditCursorHoldsUncontributedSource(t *testing.T) {
	prev := map[string]int64{"routd": 100, "runed": 7}
	got := nextAuditCursor(prev, []auditRow{
		{Row: audit.Row{ID: 90}, Source: "routd"},
		{Row: audit.Row{ID: 80}, Source: "routd"},
	})
	if !strings.Contains(got, "routd:80") {
		t.Errorf("routd cursor should advance to its oldest rendered id (80): %s", got)
	}
	if !strings.Contains(got, "runed:7") {
		t.Errorf("runed contributed nothing and must keep cursor 7: %s", got)
	}
}
