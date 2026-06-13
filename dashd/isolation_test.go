package main

// Cross-tenant isolation tests for the folder-scoped READ mode ("each agent
// owns its dashboard"). A bug here leaks one tenant's data to another, so every
// assertion below is a hard isolation guarantee:
//
//   - operator (`**`)  → full instance view (no feature drop).
//   - alice (admin on corp/eng) → sees corp/eng AND its subtree corp/eng/sre,
//     never corp/sales.
//   - bob (admin on corp/sales) → sees corp/sales only, never corp/eng.
//
// Identity arrives via the proxyd-signed X-User-Groups header (= the caller's
// store.UserScopes at sign time); these tests stamp it directly.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/tests/testutils"
)

const (
	folderEng   = "corp/eng"
	folderSre   = "corp/eng/sre"
	folderSales = "corp/sales"
)

// isoEnv builds a dash server seeded with two tenants + a subfolder, and the
// ACL rows that back the three personas. Returns the mux for direct requests.
func isoEnv(t *testing.T) (*http.ServeMux, *testutils.Inst) {
	t.Helper()
	_, inst, _ := newRWDashServer(t)
	mux := http.NewServeMux()
	(&dash{db: inst.DB, dbRW: inst.DB, dbRoutd: inst.DB, dbPath: "memory", groupsDir: inst.Tmp}).registerRoutes(mux)

	now := time.Now()
	for _, f := range []string{"corp", folderEng, folderSre, folderSales} {
		if err := inst.Store.PutGroup(core.Group{Folder: f, AddedAt: now}); err != nil {
			t.Fatalf("seed group %s: %v", f, err)
		}
	}
	// Persona grants. proxyd would sign these into X-User-Groups; the tests set
	// the header explicitly, but the grants also back the requireAdmin writes and
	// the UserScopes fallback.
	for _, g := range []core.ACLRow{
		{Principal: "op@x", Action: "admin", Scope: "**", Effect: "allow"},
		{Principal: "alice@x", Action: "admin", Scope: folderEng, Effect: "allow"},
		{Principal: "bob@x", Action: "admin", Scope: folderSales, Effect: "allow"},
	} {
		if err := inst.Store.AddACLRow(g); err != nil {
			t.Fatalf("seed acl %+v: %v", g, err)
		}
	}
	return mux, inst
}

// as stamps a persona's signed identity onto a request: X-User-Sub + the
// X-User-Groups the persona holds (what proxyd would sign).
func as(r *http.Request, sub string, groups ...string) *http.Request {
	r.Header.Set("X-User-Sub", sub)
	b := strings.Builder{}
	b.WriteByte('[')
	for i, g := range groups {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(g)
		b.WriteByte('"')
	}
	b.WriteByte(']')
	r.Header.Set("X-User-Groups", b.String())
	return r
}

func get(t *testing.T, mux *http.ServeMux, r *http.Request) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

//  1. alice's /dash/groups shows corp/eng AND corp/eng/sre (subtree inclusion),
//     NOT corp/sales (cross-tenant exclusion).
func TestIso_Groups_AliceSeesSubtreeNotSales(t *testing.T) {
	mux, _ := isoEnv(t)
	code, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/", nil), "alice@x", folderEng))
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	mustContain(t, body, folderEng, "alice must see her own folder")
	mustContain(t, body, folderSre, "alice must see her subtree (corp/eng/sre)")
	mustNotContain(t, body, folderSales, "alice MUST NOT see corp/sales")
}

// 2a. Routes: alice sees a route targeting corp/eng[/sre], not corp/sales.
func TestIso_Routes_AliceScoped(t *testing.T) {
	mux, inst := isoEnv(t)
	for _, rt := range []core.Route{
		{Seq: 1, Match: "room=eng", Target: folderEng},
		{Seq: 2, Match: "room=sre", Target: folderSre},
		{Seq: 3, Match: "room=sales", Target: folderSales},
	} {
		if _, err := inst.Store.AddRoute(rt); err != nil {
			t.Fatal(err)
		}
	}
	code, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/routes/", nil), "alice@x", folderEng))
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	mustContain(t, body, "room=eng", "alice sees the corp/eng route")
	mustContain(t, body, "room=sre", "alice sees the corp/eng/sre route (subtree)")
	mustNotContain(t, body, "room=sales", "alice MUST NOT see the corp/sales route")
}

// 2b. Tasks: alice sees tasks owned by corp/eng[/sre], not corp/sales.
func TestIso_Tasks_AliceScoped(t *testing.T) {
	mux, inst := isoEnv(t)
	seedTask(t, inst, "task-eng", folderEng)
	seedTask(t, inst, "task-sre", folderSre)
	seedTask(t, inst, "task-sales", folderSales)

	code, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/tasks/", nil), "alice@x", folderEng))
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	mustContain(t, body, "task-eng", "alice sees her own task")
	mustContain(t, body, "task-sre", "alice sees her subtree task")
	mustNotContain(t, body, "task-sales", "alice MUST NOT see corp/sales task")

	// The partial (htmx refresh) must enforce the same scope.
	code, body = get(t, mux, as(httptest.NewRequest("GET", "/dash/tasks/x/list", nil), "alice@x", folderEng))
	if code != 200 {
		t.Fatalf("partial status = %d", code)
	}
	mustNotContain(t, body, "task-sales", "tasks partial MUST NOT leak corp/sales task")
}

// 2c. Activity: alice sees messages in corp/eng[/sre] chats, not corp/sales.
func TestIso_Activity_AliceScoped(t *testing.T) {
	mux, inst := isoEnv(t)
	seedMsg(t, inst, "m-eng", "web:"+folderEng, "ENG-MARKER")
	seedMsg(t, inst, "m-sre", "web:"+folderSre, "SRE-MARKER")
	seedMsg(t, inst, "m-sales", "web:"+folderSales, "SALES-MARKER")

	code, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/activity/", nil), "alice@x", folderEng))
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	mustContain(t, body, "ENG-MARKER", "alice sees corp/eng activity")
	mustContain(t, body, "SRE-MARKER", "alice sees corp/eng/sre activity (subtree)")
	mustNotContain(t, body, "SALES-MARKER", "alice MUST NOT see corp/sales activity")

	code, body = get(t, mux, as(httptest.NewRequest("GET", "/dash/activity/x/recent", nil), "alice@x", folderEng))
	if code != 200 {
		t.Fatalf("partial status = %d", code)
	}
	mustNotContain(t, body, "SALES-MARKER", "activity partial MUST NOT leak corp/sales")
}

// 2d. Memory picker: alice's dropdown lists corp/eng[/sre], not corp/sales.
func TestIso_Memory_AlicePickerScoped(t *testing.T) {
	mux, _ := isoEnv(t)
	code, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/memory/", nil), "alice@x", folderEng))
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	mustContain(t, body, `value="`+folderEng+`"`, "alice's picker lists corp/eng")
	mustContain(t, body, `value="`+folderSre+`"`, "alice's picker lists corp/eng/sre")
	mustNotContain(t, body, `value="`+folderSales+`"`, "alice's picker MUST NOT list corp/sales")
}

//  3. Per-folder GETs into corp/sales are 403 for alice (settings/grants/tokens/
//     tools/task-detail).
func TestIso_PerFolderGET_AliceForbiddenOnSales(t *testing.T) {
	mux, inst := isoEnv(t)
	seedTask(t, inst, "task-sales", folderSales)

	cases := []struct{ name, path string }{
		{"settings", "/dash/groups/corp/sales/settings"},
		{"grants", "/dash/groups/corp/sales/grants"},
		{"tools", "/dash/groups/corp/sales/tools"},
		{"tokens", "/dash/tokens/corp%2Fsales/"},
		{"task-detail", "/dash/tasks/task-sales"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _ := get(t, mux, as(httptest.NewRequest("GET", c.path, nil), "alice@x", folderEng))
			if code != http.StatusForbidden {
				t.Errorf("GET %s as alice = %d, want 403 (cross-tenant leak)", c.path, code)
			}
		})
	}

	// Sanity: alice's OWN subtree is reachable (not 403).
	code, _ := get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/corp/eng/sre/settings", nil), "alice@x", folderEng))
	if code != 200 {
		t.Errorf("GET own subtree settings = %d, want 200", code)
	}
}

//  4. Instance-wide sections (invites, channels) are 403 for non-operators, and
//     the invites nav link is hidden.
func TestIso_InstanceWide_AliceForbidden(t *testing.T) {
	mux, _ := isoEnv(t)
	for _, path := range []string{"/dash/invites/", "/dash/channels/whatsapp/pair"} {
		code, _ := get(t, mux, as(httptest.NewRequest("GET", path, nil), "alice@x", folderEng))
		if code != http.StatusForbidden {
			t.Errorf("GET %s as alice = %d, want 403", path, code)
		}
	}
	// Nav on a scoped page omits the operator-only invites link for alice.
	_, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/", nil), "alice@x", folderEng))
	mustNotContain(t, body, `href="/dash/invites/"`, "non-operator nav MUST NOT show invites link")
	mustContain(t, body, `href="/dash/groups/"`, "scoped links stay in nav")
}

//  5. Operator keeps the full view — all folders + invites + channels (no
//     feature drop).
func TestIso_Operator_FullView(t *testing.T) {
	mux, inst := isoEnv(t)
	for _, rt := range []core.Route{
		{Seq: 1, Match: "room=eng", Target: folderEng},
		{Seq: 3, Match: "room=sales", Target: folderSales},
	} {
		if _, err := inst.Store.AddRoute(rt); err != nil {
			t.Fatal(err)
		}
	}
	seedTask(t, inst, "task-eng", folderEng)
	seedTask(t, inst, "task-sales", folderSales)

	// Groups: operator sees BOTH tenants.
	_, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/", nil), "op@x", "**"))
	mustContain(t, body, folderEng, "operator sees corp/eng")
	mustContain(t, body, folderSales, "operator sees corp/sales")

	// Routes: both targets.
	_, body = get(t, mux, as(httptest.NewRequest("GET", "/dash/routes/", nil), "op@x", "**"))
	mustContain(t, body, "room=eng", "operator sees eng route")
	mustContain(t, body, "room=sales", "operator sees sales route")

	// Tasks: both owners.
	_, body = get(t, mux, as(httptest.NewRequest("GET", "/dash/tasks/", nil), "op@x", "**"))
	mustContain(t, body, "task-eng", "operator sees eng task")
	mustContain(t, body, "task-sales", "operator sees sales task")

	// Invites + channels reachable (not 403); nav shows invites.
	code, _ := get(t, mux, as(httptest.NewRequest("GET", "/dash/invites/", nil), "op@x", "**"))
	if code != 200 {
		t.Errorf("operator invites = %d, want 200", code)
	}
	code, _ = get(t, mux, as(httptest.NewRequest("GET", "/dash/channels/whatsapp/pair", nil), "op@x", "**"))
	if code != 200 {
		t.Errorf("operator channels = %d, want 200", code)
	}
	_, body = get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/", nil), "op@x", "**"))
	mustContain(t, body, `href="/dash/invites/"`, "operator nav shows invites link")

	// Cross-tenant per-folder GETs are reachable for the operator.
	code, _ = get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/corp/sales/settings", nil), "op@x", "**"))
	if code != 200 {
		t.Errorf("operator corp/sales settings = %d, want 200", code)
	}
}

// 6. bob sees corp/sales only, never corp/eng (symmetric to alice).
func TestIso_Bob_SeesSalesNotEng(t *testing.T) {
	mux, inst := isoEnv(t)
	for _, rt := range []core.Route{
		{Seq: 1, Match: "room=eng", Target: folderEng},
		{Seq: 3, Match: "room=sales", Target: folderSales},
	} {
		if _, err := inst.Store.AddRoute(rt); err != nil {
			t.Fatal(err)
		}
	}
	seedTask(t, inst, "task-eng", folderEng)
	seedTask(t, inst, "task-sales", folderSales)

	_, body := get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/", nil), "bob@x", folderSales))
	mustContain(t, body, folderSales, "bob sees corp/sales")
	mustNotContain(t, body, folderEng, "bob MUST NOT see corp/eng")

	_, body = get(t, mux, as(httptest.NewRequest("GET", "/dash/routes/", nil), "bob@x", folderSales))
	mustContain(t, body, "room=sales", "bob sees sales route")
	mustNotContain(t, body, "room=eng", "bob MUST NOT see eng route")

	_, body = get(t, mux, as(httptest.NewRequest("GET", "/dash/tasks/", nil), "bob@x", folderSales))
	mustContain(t, body, "task-sales", "bob sees sales task")
	mustNotContain(t, body, "task-eng", "bob MUST NOT see eng task")

	// bob is 403 on corp/eng per-folder GET.
	code, _ := get(t, mux, as(httptest.NewRequest("GET", "/dash/groups/corp/eng/settings", nil), "bob@x", folderSales))
	if code != http.StatusForbidden {
		t.Errorf("bob GET corp/eng/settings = %d, want 403", code)
	}
}

//  7. Portal dot-counts (countVisible* on authz.go) MUST scope to the caller.
//     A non-operator's group/errored-chat/failed-task counts include only their
//     own subtree; a bug in the per-row visible() filter leaks other tenants'
//     totals onto the landing page (alice would see "3 errored" when 2 are
//     sales'). Operator gets the raw COUNT(*).
func TestIso_PortalCounts_Scoped(t *testing.T) {
	_, inst := isoEnv(t)
	d := &dash{db: inst.DB, dbRW: inst.DB, dbRoutd: inst.DB}

	// Errored chats: one per tenant + a JID with no resolvable folder.
	for _, m := range []struct{ id, jid string }{
		{"e-eng", "web:" + folderEng},
		{"e-sre", "web:" + folderSre},
		{"e-sales", "web:" + folderSales},
		{"e-orphan", "tel:99999"}, // no route → jidFolder "" → invisible to non-op
	} {
		if _, err := inst.DB.Exec(
			`INSERT INTO messages (id, chat_jid, sender, content, timestamp, source, errored)
			 VALUES (?, ?, 's', 'x', datetime('now'), 'web', 1)`, m.id, m.jid); err != nil {
			t.Fatal(err)
		}
	}
	// Failed task runs in the last day: one per tenant.
	for _, ft := range []struct{ taskID, owner string }{
		{"ft-eng", folderEng}, {"ft-sre", folderSre}, {"ft-sales", folderSales},
	} {
		seedTask(t, inst, ft.taskID, ft.owner)
		if _, err := inst.DB.Exec(
			`INSERT INTO task_run_logs (task_id, run_at, status) VALUES (?, datetime('now'), 'error')`,
			ft.taskID); err != nil {
			t.Fatal(err)
		}
	}

	alice := []string{folderEng}
	// alice: corp/eng + corp/eng/sre groups (NOT corp, corp/sales).
	if n := d.countVisibleGroups(alice, false); n != 2 {
		t.Errorf("countVisibleGroups(alice) = %d, want 2 (eng+sre)", n)
	}
	// alice: eng + sre errored chats; sales + orphan excluded.
	if n := d.countVisibleErroredChats(alice, false); n != 2 {
		t.Errorf("countVisibleErroredChats(alice) = %d, want 2 (eng+sre)", n)
	}
	// alice: eng + sre failed tasks; sales excluded.
	if n := d.countVisibleFailedTasks(alice, false); n != 2 {
		t.Errorf("countVisibleFailedTasks(alice) = %d, want 2 (eng+sre)", n)
	}

	// Operator: raw totals across all tenants (incl. the orphan errored chat).
	if n := d.countVisibleGroups(nil, true); n != 4 {
		t.Errorf("countVisibleGroups(op) = %d, want 4 (corp+eng+sre+sales)", n)
	}
	if n := d.countVisibleErroredChats(nil, true); n != 4 {
		t.Errorf("countVisibleErroredChats(op) = %d, want 4", n)
	}
	if n := d.countVisibleFailedTasks(nil, true); n != 3 {
		t.Errorf("countVisibleFailedTasks(op) = %d, want 3", n)
	}
}

// visible() unit coverage: the subtree predicate and cross-tenant exclusion,
// independent of HTTP wiring.
func TestIso_VisiblePredicate(t *testing.T) {
	allowed := []string{folderEng}
	cases := []struct {
		folder string
		want   bool
	}{
		{folderEng, true},       // exact
		{folderSre, true},       // subtree
		{folderSales, false},    // sibling tenant
		{"corp", false},         // parent is NOT visible from a child grant
		{"corp/england", false}, // prefix-but-not-subtree (no false positive)
		{"", false},
	}
	for _, c := range cases {
		if got := visible(allowed, false, c.folder); got != c.want {
			t.Errorf("visible(%v, false, %q) = %v, want %v", allowed, c.folder, got, c.want)
		}
	}
	// Operator sees everything regardless of allowed set.
	if !visible(nil, true, folderSales) {
		t.Error("operator must see any folder")
	}
}

// --- seed + assert helpers ---

func seedTask(t *testing.T, inst *testutils.Inst, id, owner string) {
	t.Helper()
	if _, err := inst.DB.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES (?, ?, ?, 'p', '0 9 * * *', 'active', datetime('now'))`,
		id, owner, "web:"+owner); err != nil {
		t.Fatalf("seed task %s: %v", id, err)
	}
}

func seedMsg(t *testing.T, inst *testutils.Inst, id, jid, content string) {
	t.Helper()
	if _, err := inst.DB.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, source)
		 VALUES (?, ?, 's', ?, datetime('now'), 'web')`,
		id, jid, content); err != nil {
		t.Fatalf("seed msg %s: %v", id, err)
	}
}

func mustContain(t *testing.T, body, want, why string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("%s: body missing %q", why, want)
	}
}

func mustNotContain(t *testing.T, body, bad, why string) {
	t.Helper()
	if strings.Contains(body, bad) {
		t.Errorf("%s: body leaked %q", why, bad)
	}
}
