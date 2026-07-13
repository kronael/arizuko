package routd

// REST-face tests for the spec 5/16 routes fold: /v1/routes add/set/list/delete now
// ride the SAME shared routesHandler the agent's add_route/set_routes/list_routes/
// delete_route MCP tools use, via resreg.RegisterREST + the injected
// routesRESTCaller/routesRESTGate; get-one stays a thin read. These assert (a)
// POST/GET/GET-one/DELETE parity with the new resreg wire shapes ({id}, bare
// []RouteView, {deleted}/404), (b) the tier-0-tenant containment the gate's
// ownsFolder adds on top of the handler's tier model (add + delete cross-folder →
// 403; fails if that ownsFolder is dropped), (c) the list-all leak guard keyed on an
// EMPTY folder claim not the tier (a top-level tenant is tier 0 yet sees only its
// own; fails if keyed on tier 0), and (d) the self-default guard through REST.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/router"
)

// TestRESTRouteParity: POST add → GET list → GET-one → DELETE all run the shared
// handler and land the rows an add_route/delete_route MCP call would, with the
// resreg wire shapes ({id}, a bare []RouteView annotated list, {deleted}, 404 on a
// missing delete). id addressing (the autoincrement path {id}) is preserved.
func TestRESTRouteParity(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"routes:write:own_group", "routes:read:own_group"}, folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "hq"})

	// POST add (own subtree, seq 1 so it isn't a self-default) → 200 {id}.
	r := doJSON(t, h, "POST", "/v1/routes", "", map[string]any{"route": apiv1.Route{Seq: 1, Match: "platform=slack", Target: "hq"}})
	if r.Code != 200 {
		t.Fatalf("POST add = %d want 200 body=%s", r.Code, r.Body.String())
	}
	var added struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &added); err != nil || added.ID == 0 {
		t.Fatalf("POST add body = %s want {id:N}", r.Body.String())
	}
	if rows, _ := db.Routes(); len(rows) != 1 || rows[0].Target != "hq" {
		t.Fatalf("REST add row = %+v want one hq route", rows)
	}

	// GET list → a bare []RouteView, annotated, scoped to hq.
	g := doJSON(t, h, "GET", "/v1/routes", "", nil)
	if g.Code != 200 {
		t.Fatalf("GET list = %d want 200 body=%s", g.Code, g.Body.String())
	}
	var listed []router.RouteView
	if err := json.Unmarshal(g.Body.Bytes(), &listed); err != nil {
		t.Fatalf("GET list body %s: %v", g.Body.String(), err)
	}
	if len(listed) != 1 || listed[0].Target != "hq" || listed[0].Mode != "trigger" {
		t.Fatalf("GET list = %+v want one annotated hq route", listed)
	}

	// GET one by id → the wire route.
	one := doJSON(t, h, "GET", "/v1/routes/"+strconv.FormatInt(added.ID, 10), "", nil)
	if one.Code != 200 {
		t.Fatalf("GET one = %d want 200 body=%s", one.Code, one.Body.String())
	}
	var got apiv1.Route
	if err := json.Unmarshal(one.Body.Bytes(), &got); err != nil || got.Target != "hq" {
		t.Fatalf("GET one = %s want an hq route", one.Body.String())
	}

	// DELETE by id (path) → {deleted:true}; row gone; a second delete misses → 404.
	d := doJSON(t, h, "DELETE", "/v1/routes/"+strconv.FormatInt(added.ID, 10), "", nil)
	if d.Code != 200 {
		t.Fatalf("DELETE = %d want 200 body=%s", d.Code, d.Body.String())
	}
	if rows, _ := db.Routes(); len(rows) != 0 {
		t.Fatalf("DELETE left rows: %+v", rows)
	}
	if d2 := doJSON(t, h, "DELETE", "/v1/routes/"+strconv.FormatInt(added.ID, 10), "", nil); d2.Code != 404 {
		t.Fatalf("DELETE missing = %d want 404 body=%s", d2.Code, d2.Body.String())
	}
}

// TestRESTRouteTier0TenantContainment: a top-level tenant "alice" is tier 0, so the
// shared handler's tier model (AuthorizeStructural) is a no-op for it — the gate's
// ownsFolder is the ONLY thing confining it to its own subtree. add + delete of a
// sibling folder's route are 403'd, and nothing is written / removed. This FAILS if
// routesRESTGate's ownsFolder check is dropped (the tier-0 leak class).
func TestRESTRouteTier0TenantContainment(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "alice"})
	for _, f := range []string{"alice", "alice/sub", "bob"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	// own subtree: allowed.
	if r := doJSON(t, h, "POST", "/v1/routes", "", map[string]any{"route": apiv1.Route{Seq: 1, Match: "m", Target: "alice/sub"}}); r.Code != 200 {
		t.Fatalf("own-subtree add = %d want 200 body=%s", r.Code, r.Body.String())
	}
	// sibling folder: 403, no row.
	if r := doJSON(t, h, "POST", "/v1/routes", "", map[string]any{"route": apiv1.Route{Seq: 2, Match: "m", Target: "bob"}}); r.Code != 403 {
		t.Fatalf("sibling add = %d want 403 body=%s", r.Code, r.Body.String())
	}
	for _, rt := range mustRoutes(t, db) {
		if rt.Target == "bob" {
			t.Fatalf("denied sibling add still wrote a row: %+v", rt)
		}
	}
	// delete of a sibling's route (id resolves to target bob) → 403, still present.
	sibID, err := db.AddRoute(core.Route{Seq: 1, Match: "m", Target: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if r := doJSON(t, h, "DELETE", "/v1/routes/"+strconv.FormatInt(sibID, 10), "", nil); r.Code != 403 {
		t.Fatalf("cross-folder delete = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if _, err := db.GetRoute(sibID); err != nil {
		t.Fatal("cross-folder delete removed the sibling's route")
	}
}

// TestRESTRouteListScoping: the list-all leak guard. An EMPTY folder claim (root /
// service token) lists every folder's routes; a folder-scoped token — even a
// top-level tenant, which is tier 0 — lists ONLY its own subtree. This FAILS if the
// list is keyed on tier 0 instead of the empty folder claim (the tenant would then
// read every sibling's routes).
func TestRESTRouteListScoping(t *testing.T) {
	seed := func(db *DB) {
		for _, f := range []string{"alice", "alice/sub", "bob"} {
			_ = db.PutGroup(core.Group{Folder: f})
		}
		_, _ = db.AddRoute(core.Route{Seq: 1, Match: "m", Target: "alice"})
		_, _ = db.AddRoute(core.Route{Seq: 2, Match: "m", Target: "alice/sub"})
		_, _ = db.AddRoute(core.Route{Seq: 3, Match: "m", Target: "bob"})
	}

	// tier-0 tenant "alice": only its own subtree, never bob's.
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read:own_group"}, folder: "alice"})
	seed(db)
	scoped := listTargets(t, h)
	if len(scoped) != 2 || scoped["bob"] {
		t.Fatalf("tier-0 tenant list = %v want only alice + alice/sub", scoped)
	}

	// empty folder (root / service token): the whole table.
	db2, h2 := authSrv(t, fakeVerifier{sub: "svc", scope: []string{"routes:read"}, folder: ""})
	seed(db2)
	all := listTargets(t, h2)
	if len(all) != 3 || !all["bob"] {
		t.Fatalf("root list = %v want all three folders", all)
	}
}

// TestRESTRouteSelfDefaultGuard: a folder cannot drop its own seq-0 default route
// through the REST face — DELETE of it is refused, and a set that omits it is
// refused. Same guard the MCP twin enforces.
func TestRESTRouteSelfDefaultGuard(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"routes:write:own_group", "routes:read:own_group"}, folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "hq"})
	defID, err := db.AddRoute(core.Route{Seq: 0, Match: "room=hq", Target: "hq"})
	if err != nil {
		t.Fatal(err)
	}

	// DELETE the seq-0 default → 403, still present.
	if r := doJSON(t, h, "DELETE", "/v1/routes/"+strconv.FormatInt(defID, 10), "", nil); r.Code != 403 {
		t.Fatalf("delete own default = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if _, err := db.GetRoute(defID); err != nil {
		t.Fatal("refused delete still removed the default route")
	}
	// set that drops the seq-0 default → 403, table unchanged.
	if r := doJSON(t, h, "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{{Seq: 5, Match: "platform=slack", Target: "hq"}}}); r.Code != 403 {
		t.Fatalf("set dropping default = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if rows := mustRoutes(t, db); len(rows) != 1 || rows[0].Seq != 0 {
		t.Fatalf("refused set mutated the table: %+v", rows)
	}
}

// TestRESTRouteTier1OwnSubtree: a tier-1 folder "alice/team" manages routes in its
// OWN subtree over REST. The baked route tier cap allowed only STRICT descendants
// (HasPrefix target, folder+"/"), so an add pointing at the folder's OWN name was
// wrongly 403. Post-decouple the REST face uses ownsFolder (own-or-descendant): own
// + descendant are 200 while a sibling or the parent is 403.
func TestRESTRouteTier1OwnSubtree(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"routes:write:own_group", "routes:read:own_group"}, folder: "alice/team"})
	for _, f := range []string{"alice", "alice/team", "alice/team/sub", "alice/other"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	add := func(seq int, target string) int {
		return doJSON(t, h, "POST", "/v1/routes", "",
			map[string]any{"route": apiv1.Route{Seq: seq, Match: "m", Target: target}}).Code
	}
	if c := add(1, "alice/team"); c != 200 { // own folder (was 403 under the strict-descendant cap)
		t.Fatalf("own-folder add = %d want 200", c)
	}
	if c := add(2, "alice/team/sub"); c != 200 {
		t.Fatalf("descendant add = %d want 200", c)
	}
	if c := add(3, "alice/other"); c != 403 { // same-world sibling
		t.Fatalf("sibling add = %d want 403", c)
	}
	if c := add(4, "alice"); c != 403 { // parent
		t.Fatalf("parent add = %d want 403", c)
	}
	for _, rt := range mustRoutes(t, db) {
		if rt.Target == "alice/other" || rt.Target == "alice" {
			t.Fatalf("denied add wrote a cross-subtree route: %+v", rt)
		}
	}
}

// TestRESTRouteTier2OperatorOwnSubtree: an operator at a tier-2 folder manages
// routes in its own subtree over REST. The baked route cap denied tier 2+ ALL route
// management (403), so even an own-folder add failed. Post-decouple the REST face
// uses ownsFolder: own + descendant are 200 while a sibling is 403.
func TestRESTRouteTier2OperatorOwnSubtree(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"routes:write:own_group"}, folder: "alice/team/sub"})
	for _, f := range []string{"alice/team/sub", "alice/team/sub/deep", "alice/team/other"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	add := func(seq int, target string) int {
		return doJSON(t, h, "POST", "/v1/routes", "",
			map[string]any{"route": apiv1.Route{Seq: seq, Match: "m", Target: target}}).Code
	}
	if c := add(1, "alice/team/sub"); c != 200 { // own (was 403 — tier 2 blanket-denied)
		t.Fatalf("own add = %d want 200", c)
	}
	if c := add(2, "alice/team/sub/deep"); c != 200 {
		t.Fatalf("descendant add = %d want 200", c)
	}
	if c := add(3, "alice/team/other"); c != 403 { // sibling
		t.Fatalf("sibling add = %d want 403", c)
	}
}

func mustRoutes(t *testing.T, db *DB) []core.Route {
	t.Helper()
	rows, err := db.Routes()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// listTargets GETs /v1/routes and returns the set of route targets in the response.
func listTargets(t *testing.T, h http.Handler) map[string]bool {
	t.Helper()
	rec := doJSON(t, h, "GET", "/v1/routes", "", nil)
	if rec.Code != 200 {
		t.Fatalf("GET list = %d body=%s", rec.Code, rec.Body.String())
	}
	var views []router.RouteView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("list body %s: %v", rec.Body.String(), err)
	}
	out := map[string]bool{}
	for _, v := range views {
		out[v.Target] = true
	}
	return out
}
