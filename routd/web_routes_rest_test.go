package routd

// REST-face tests for the spec 5/44 web_routes fold: /v1/web_routes list/create/
// delete now ride the SAME shared webRoutesHandler the agent MCP tools use, via
// resreg.RegisterREST + the injected webRoutesRESTCaller/webRoutesRESTGate.
// These assert (a) create/list/delete parity with the agent handler + the new
// resreg wire shapes ({ok:true}/404, []ipc.WebRoute with redirect_to omitempty),
// (b) a scoped non-operator manages its own folder but 403s cross-folder, (c) the
// tier-0 operator delete widening, (d) the relocated ?path_prefix= owner lookup.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// TestRESTWebRouteParity: PUT create → GET list → DELETE all run the shared
// handler and land the same rows a set_web_route/del_web_route MCP call would,
// with the resreg wire shapes ({ok:true}, []ipc.WebRoute redirect_to-omitempty,
// 404 on a missing delete).
func TestRESTWebRouteParity(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"routes:write:own_group", "routes:read:own_group"}, folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "hq"})

	// PUT create: own-slot public route → 200 {ok:true} + row under hq.
	r := doJSON(t, h, "PUT", "/v1/web_routes", "", webRouteReq{Folder: "hq", Path: "/pub/hq/app", Access: "public"})
	if r.Code != 200 {
		t.Fatalf("PUT create = %d want 200 body=%s", r.Code, r.Body.String())
	}
	var ok apiv1.OK
	if err := json.Unmarshal(r.Body.Bytes(), &ok); err != nil || !ok.OK {
		t.Fatalf("PUT create body = %s want {ok:true}", r.Body.String())
	}
	rows, _ := db.WebRoutes("hq")
	if len(rows) != 1 || rows[0].PathPrefix != "/pub/hq/app" || rows[0].Folder != "hq" || rows[0].Access != "public" {
		t.Fatalf("REST create row = %+v want /pub/hq/app public folder=hq", rows)
	}

	// GET list: the ipc.WebRoute shape (path_prefix key; redirect_to omitempty).
	g := doJSON(t, h, "GET", "/v1/web_routes", "", nil)
	if g.Code != 200 {
		t.Fatalf("GET list = %d want 200 body=%s", g.Code, g.Body.String())
	}
	var listed []ipc.WebRoute
	if err := json.Unmarshal(g.Body.Bytes(), &listed); err != nil {
		t.Fatalf("GET list body %s: %v", g.Body.String(), err)
	}
	if len(listed) != 1 || listed[0].PathPrefix != "/pub/hq/app" {
		t.Fatalf("GET list = %+v want one /pub/hq/app row", listed)
	}
	if strings.Contains(g.Body.String(), "redirect_to") {
		t.Fatalf("GET list leaked empty redirect_to (want omitempty): %s", g.Body.String())
	}

	// DELETE: {ok:true} + row gone; a second delete misses → 404.
	d := doJSON(t, h, "DELETE", "/v1/web_routes", "", webRouteReq{Folder: "hq", Path: "/pub/hq/app"})
	if d.Code != 200 {
		t.Fatalf("DELETE = %d want 200 body=%s", d.Code, d.Body.String())
	}
	if rows, _ := db.WebRoutes("hq"); len(rows) != 0 {
		t.Fatalf("DELETE left rows: %+v", rows)
	}
	if d2 := doJSON(t, h, "DELETE", "/v1/web_routes", "", webRouteReq{Folder: "hq", Path: "/pub/hq/app"}); d2.Code != 404 {
		t.Fatalf("DELETE missing = %d want 404 body=%s", d2.Code, d2.Body.String())
	}
}

// TestRESTWebRouteScopedManage: a scoped non-operator (tier-1) manages its OWN
// folder's route but is 403'd cross-folder by the Gate's ownsFolder — a scoped
// delete is bound to its own folder (no tier-0 widening).
func TestRESTWebRouteScopedManage(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "world/eng"})
	_ = db.PutGroup(core.Group{Folder: "world/eng"})
	_ = db.PutGroup(core.Group{Folder: "world/ops"})

	// own folder: allowed, row lands under world/eng.
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", webRouteReq{Folder: "world/eng", Path: "/pub/world/eng/app", Access: "public"}); r.Code != 200 {
		t.Fatalf("own-folder create = %d want 200 body=%s", r.Code, r.Body.String())
	}
	if rows, _ := db.WebRoutes("world/eng"); len(rows) != 1 {
		t.Fatalf("own create didn't land: %+v", rows)
	}
	// sibling folder: rejected by the Gate's ownsFolder (403), no row.
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", webRouteReq{Folder: "world/ops", Path: "/pub/world/ops/app", Access: "public"}); r.Code != 403 {
		t.Fatalf("sibling create = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if rows, _ := db.WebRoutes("world/ops"); len(rows) != 0 {
		t.Fatalf("sibling create leaked a row: %+v", rows)
	}
	// scoped delete stays bound to the own folder: a sibling's route is untouched.
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/pub/world/ops/x", Access: "public", Folder: "world/ops"})
	if r := doJSON(t, h, "DELETE", "/v1/web_routes", "", webRouteReq{Folder: "world/ops", Path: "/pub/world/ops/x"}); r.Code != 403 {
		t.Fatalf("cross-folder delete = %d want 403 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.WebRouteOwner("/pub/world/ops/x"); !ok {
		t.Fatal("cross-folder delete removed the sibling's route")
	}
	if r := doJSON(t, h, "DELETE", "/v1/web_routes", "", webRouteReq{Folder: "world/eng", Path: "/pub/world/eng/app"}); r.Code != 200 {
		t.Fatalf("own delete = %d want 200 body=%s", r.Code, r.Body.String())
	}
}

// TestRESTWebRouteTier0DeleteWidens: a tier-0 (top-level) operator deletes ANY
// folder's route by path — the accepted 5/44 delete widening. The operator sends
// no folder arg, so the target defaults to its own tier-0 folder and the shared
// handler drops the folder bound (scopedFolder="").
func TestRESTWebRouteTier0DeleteWidens(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "other"})
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/pub/other/x", Access: "public", Folder: "other"})

	if r := doJSON(t, h, "DELETE", "/v1/web_routes", "", webRouteReq{Path: "/pub/other/x"}); r.Code != 200 {
		t.Fatalf("tier-0 widened delete = %d want 200 body=%s", r.Code, r.Body.String())
	}
	if _, ok := db.WebRouteOwner("/pub/other/x"); ok {
		t.Fatal("tier-0 operator did not delete the other folder's route")
	}
}

// TestRESTWebRouteScopedListNoLeak is the leak guard for webRoutesTarget: a
// top-level folder is tier-0, but a folder-SCOPED token there must list only its
// own routes, never a sibling's. Keying list-all on tier-0 (instead of an empty
// folder claim = root/service token) would leak every folder's routes to any
// top-level tenant. Content-level, because a status-only check can't tell
// "own-only, 200" from "all, 200".
func TestRESTWebRouteScopedListNoLeak(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read:own_group"}, folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "bob"})
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/pub/alice/a", Access: "public", Folder: "alice"})
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/pub/bob/b", Access: "public", Folder: "bob"})

	g := doJSON(t, h, "GET", "/v1/web_routes", "", nil)
	if g.Code != 200 {
		t.Fatalf("alice list = %d want 200 body=%s", g.Code, g.Body.String())
	}
	var listed []ipc.WebRoute
	if err := json.Unmarshal(g.Body.Bytes(), &listed); err != nil {
		t.Fatalf("alice list body %s: %v", g.Body.String(), err)
	}
	if len(listed) != 1 || listed[0].PathPrefix != "/pub/alice/a" {
		t.Fatalf("alice (tier-0 scoped) list = %+v want ONLY /pub/alice/a — bob's route leaked", listed)
	}
}

// TestRESTWebRouteOwnerLookup: the first-claim owner lookup, relocated off GET
// /v1/web_routes to GET /v1/web_routes/owner, still resolves an exact path_prefix
// to its owning folder (or "" when unclaimed).
func TestRESTWebRouteOwnerLookup(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read:own_group"}, folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/shared", Access: "public", Folder: "hq"})

	r := doJSON(t, h, "GET", "/v1/web_routes/owner?path_prefix=/shared", "", nil)
	if r.Code != 200 {
		t.Fatalf("owner lookup = %d want 200 body=%s", r.Code, r.Body.String())
	}
	var out struct {
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil || out.Owner != "hq" {
		t.Fatalf("owner = %q want hq (body=%s)", out.Owner, r.Body.String())
	}
	r2 := doJSON(t, h, "GET", "/v1/web_routes/owner?path_prefix=/nope", "", nil)
	if r2.Code != 200 {
		t.Fatalf("unclaimed owner lookup = %d want 200", r2.Code)
	}
	var out2 struct {
		Owner string `json:"owner"`
	}
	_ = json.Unmarshal(r2.Body.Bytes(), &out2)
	if out2.Owner != "" {
		t.Fatalf("unclaimed owner = %q want empty", out2.Owner)
	}
}
