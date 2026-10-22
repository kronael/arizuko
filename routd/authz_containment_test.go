package routd

import (
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/core"
)

// The REST read/manage surface must enforce the SAME folder containment the MCP
// path does (codex split review #1/#2): a folder-scoped token may touch only its
// own subtree; an empty-folder (root / service) token is unrestricted. web:<f>
// resolves to folder f via the 1:1 fallback, so it needs no route row.

func getCode(t *testing.T, tokenFolder, path string, scope ...string) int {
	t.Helper()
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: scope, folder: tokenFolder})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code
}

func TestRESTReadFolderBound(t *testing.T) {
	read := []string{"chats:read:own_group"}
	// inspect: own chat ok, sibling's chat denied, root sees all.
	if c := getCode(t, "alice", "/v1/messages/inspect?jid=web:alice", read...); c != 200 {
		t.Fatalf("own-folder inspect = %d want 200", c)
	}
	if c := getCode(t, "alice", "/v1/messages/inspect?jid=web:bob", read...); c != 403 {
		t.Fatalf("cross-folder inspect = %d want 403", c)
	}
	if c := getCode(t, "", "/v1/messages/inspect?jid=web:bob", read...); c != 200 {
		t.Fatalf("root inspect = %d want 200", c)
	}
	// thread + session bind the same way.
	if c := getCode(t, "alice", "/v1/messages/thread?jid=web:bob&topic=x", read...); c != 403 {
		t.Fatalf("cross-folder thread = %d want 403", c)
	}
	if c := getCode(t, "alice", "/v1/topic_sessions?folder=bob", read...); c != 403 {
		t.Fatalf("cross-folder session = %d want 403", c)
	}
	if c := getCode(t, "alice", "/v1/topic_sessions?folder=alice", read...); c != 200 {
		t.Fatalf("own session = %d want 200", c)
	}
	// routing/errored binds an explicit ?folder= to the token's subtree.
	if c := getCode(t, "alice", "/v1/routing/errored?folder=bob", "routes:read:own_group"); c != 403 {
		t.Fatalf("cross-folder errored = %d want 403", c)
	}
	if c := getCode(t, "alice", "/v1/routing/errored?folder=alice", "routes:read:own_group"); c != 200 {
		t.Fatalf("own errored = %d want 200", c)
	}
	// web_routes list rides the shared handler through the REST Gate. Only an
	// empty-folder (root/service) token is unrestricted; a folder-scoped token —
	// even one at a top-level folder, which is tier-0 — lists only its own subtree
	// (the leak guard: keying list-all on tier-0 would let a top-level TENANT read
	// every folder's routes; TestRESTWebRouteScopedListNoLeak asserts the content).
	if c := getCode(t, "", "/v1/web_routes", "routes:read"); c != 200 {
		t.Fatalf("root web_routes list = %d want 200", c)
	}
	if c := getCode(t, "alice", "/v1/web_routes", "routes:read:own_group"); c != 200 {
		t.Fatalf("own-folder web_routes list = %d want 200", c)
	}
	if c := getCode(t, "alice/eng", "/v1/web_routes?folder=alice/ops", "routes:read:own_group"); c != 403 {
		t.Fatalf("cross-folder web_routes list = %d want 403", c)
	}
	if c := getCode(t, "alice/eng", "/v1/web_routes?folder=alice/eng", "routes:read:own_group"); c != 200 {
		t.Fatalf("own web_routes list = %d want 200", c)
	}
}

// webRouteReq is the REST create/delete request body after the 5/16 REST-face
// fold: the shared handler reads the flat MCP arg names (path/access/redirect_to)
// plus the REST-only target `folder` (bound to the caller's subtree by the Gate).
type webRouteReq struct {
	Folder     string `json:"folder"`
	Path       string `json:"path"`
	Access     string `json:"access,omitempty"`
	RedirectTo string `json:"redirect_to,omitempty"`
}

// GET /v1/cost binds the TURN's folder to the token and requires the dedicated
// cost:read scope. The folder is read from the turn's own record, never parsed
// out of anything the caller supplied, so a token scoped to alice cannot read
// what bob's turn cost.
func TestRESTCostRead(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"cost:read:own_group"}, folder: "alice"})
	if _, err := db.PutTurnContext("t1", "alice", "", "web:alice", "user:u", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutTurnContext("t2", "bob", "", "web:bob", "user:v", ""); err != nil {
		t.Fatal(err)
	}

	if rec := doJSON(t, h, "GET", "/v1/cost?turn_id=t2", "", nil); rec.Code != 403 {
		t.Fatalf("cross-folder cost read = %d want 403", rec.Code)
	}
	if rec := doJSON(t, h, "GET", "/v1/cost?turn_id=missing", "", nil); rec.Code != 404 {
		t.Fatalf("missing turn cost read = %d want 404", rec.Code)
	}
	// An own-folder turn that no meter recorded is 404, never a fabricated zero:
	// this server names no gateway, so no number exists for it.
	if rec := doJSON(t, h, "GET", "/v1/cost?turn_id=t1", "", nil); rec.Code != 404 {
		t.Fatalf("unmetered turn cost read = %d want 404", rec.Code)
	}
	// messages:read must NOT satisfy the dedicated cost:read scope.
	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"messages:read"}, folder: "alice"})
	if rec := doJSON(t, h2, "GET", "/v1/cost?turn_id=t1", "", nil); rec.Code != 403 {
		t.Fatalf("cost read with messages:read = %d want 403 (dedicated scope)", rec.Code)
	}
}

// The REST web_route PUT must enforce the SAME path-claim + redirect-self-slot
// containment as its set_web_route MCP twin (2026-06-07 bug sweep): a scoped
// caller cannot hijack another folder's web path or set a cross-folder redirect.
// Post 5/16 fold both faces run the ONE shared handler, so the redirect-self-slot
// violation now surfaces the handler's 400 (was the old REST's 403).
func TestRESTWebRoutePutContainment(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "alice"}) // web_routes.folder FKs groups
	_ = db.PutGroup(core.Group{Folder: "bob"})
	// own-slot public route: allowed.
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", webRouteReq{Folder: "alice", Path: "/pub/alice/app", Access: "public"}); r.Code != 200 {
		t.Fatalf("own-slot public = %d want 200 body=%s", r.Code, r.Body.String())
	}
	// redirect into another folder's slot: rejected by the shared handler (400).
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", webRouteReq{Folder: "alice", Path: "/pub/alice/r", Access: "redirect", RedirectTo: "/pub/bob/x"}); r.Code != 400 {
		t.Fatalf("cross-folder redirect = %d want 400 body=%s", r.Code, r.Body.String())
	}
	// first-claim: a top-level path bob already owns cannot be hijacked by alice.
	putWebRoute(t, db, WebRouteRow{PathPrefix: "/shared", Access: "public", Folder: "bob"})
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", webRouteReq{Folder: "alice", Path: "/shared", Access: "public"}); r.Code != 403 {
		t.Fatalf("claim hijack = %d want 403", r.Code)
	}
	// cross-folder target folder is still rejected by the Gate's ownsFolder.
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", webRouteReq{Folder: "bob", Path: "/pub/bob/x", Access: "public"}); r.Code != 403 {
		t.Fatalf("cross-folder folder = %d want 403", r.Code)
	}
}

// GET /v1/cost/summary is the operator's spend axis (spec 6/30), so its
// narrowing must be the same one every other read here uses: a folder-scoped
// token cannot ask for a folder outside its claim, and an empty claim (root or
// a service bearer) sees everything.
func TestRESTCostSummaryFolderBound(t *testing.T) {
	const read = "cost:read:own_group"
	const since = "?since=2020-01-01T00:00:00Z"

	if c := getCode(t, "alice/eng", "/v1/cost/summary"+since+"&folder=alice/ops", read); c != 403 {
		t.Errorf("cross-folder summary = %d, want 403", c)
	}
	if c := getCode(t, "alice/eng", "/v1/cost/summary"+since+"&folder=alice/eng", read); c != 200 {
		t.Errorf("own-folder summary = %d, want 200", c)
	}
	// No folder asked for: narrowed to the token's own, not widened to all.
	if c := getCode(t, "alice/eng", "/v1/cost/summary"+since, read); c != 200 {
		t.Errorf("own summary with no folder param = %d, want 200", c)
	}
	if c := getCode(t, "", "/v1/cost/summary"+since, read); c != 200 {
		t.Errorf("root summary = %d, want 200", c)
	}

	// The window is required: without it the answer is a lifetime total dressed
	// as a current one.
	if c := getCode(t, "", "/v1/cost/summary", read); c != 400 {
		t.Errorf("summary with no since = %d, want 400", c)
	}
	// The dedicated scope is required: a bearer holding some OTHER cost-shaped
	// scope is refused. `cost:write` is the string used because routd issues no
	// such scope since `6/39-gateway-owns-spend` took the write path, so nothing
	// can satisfy this by accident.
	if c := getCode(t, "alice", "/v1/cost/summary"+since, "cost:write:own_group"); c != 403 {
		t.Errorf("summary with a non-read cost scope = %d, want 403", c)
	}
}
