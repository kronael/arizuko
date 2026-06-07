package routd

import (
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
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
	if c := getCode(t, "alice", "/v1/sessions?folder=bob", read...); c != 403 {
		t.Fatalf("cross-folder session = %d want 403", c)
	}
	if c := getCode(t, "alice", "/v1/sessions?folder=alice", read...); c != 200 {
		t.Fatalf("own session = %d want 200", c)
	}
	// routing/errored binds an explicit ?folder= to the token's subtree.
	if c := getCode(t, "alice", "/v1/routing/errored?folder=bob", "routes:read:own_group"); c != 403 {
		t.Fatalf("cross-folder errored = %d want 403", c)
	}
	if c := getCode(t, "alice", "/v1/routing/errored?folder=alice", "routes:read:own_group"); c != 200 {
		t.Fatalf("own errored = %d want 200", c)
	}
}

func TestRESTCostFolderBound(t *testing.T) {
	// cost uses its OWN scope (not messages:send) and binds req.Folder.
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"cost:write:own_group"}, folder: "alice"})
	deny := doJSON(t, h, "POST", "/v1/cost", "", apiv1.CostRequest{Folder: "bob", Provider: "openai", Model: "m"})
	if deny.Code != 403 {
		t.Fatalf("cross-folder cost = %d want 403 body=%s", deny.Code, deny.Body.String())
	}
	ok := doJSON(t, h, "POST", "/v1/cost", "", apiv1.CostRequest{Folder: "alice", Provider: "openai", Model: "m"})
	if ok.Code != 200 {
		t.Fatalf("own-folder cost = %d want 200 body=%s", ok.Code, ok.Body.String())
	}
	// messages:send must NOT satisfy the dedicated cost scope.
	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"messages:send:own_group"}, folder: "alice"})
	wrong := doJSON(t, h2, "POST", "/v1/cost", "", apiv1.CostRequest{Folder: "alice", Provider: "openai", Model: "m"})
	if wrong.Code != 403 {
		t.Fatalf("cost with messages:send = %d want 403 (dedicated scope)", wrong.Code)
	}
}

// The REST web_route PUT must enforce the SAME path-claim + redirect-self-slot
// containment as its set_web_route MCP twin (2026-06-07 bug sweep): a scoped
// caller cannot hijack another folder's web path or set a cross-folder redirect.
func TestRESTWebRoutePutContainment(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "alice"})
	_ = db.PutGroup(core.Group{Folder: "alice"}) // web_routes.folder FKs groups
	_ = db.PutGroup(core.Group{Folder: "bob"})
	// own-slot public route: allowed.
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", apiv1.WebRoute{Folder: "alice", PathPrefix: "/pub/alice/app", Access: "public"}); r.Code != 200 {
		t.Fatalf("own-slot public = %d want 200 body=%s", r.Code, r.Body.String())
	}
	// redirect into another folder's slot: forbidden (open-redirect / impersonation).
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", apiv1.WebRoute{Folder: "alice", PathPrefix: "/pub/alice/r", Access: "redirect", RedirectTo: "/pub/bob/x"}); r.Code != 403 {
		t.Fatalf("cross-folder redirect = %d want 403", r.Code)
	}
	// first-claim: a top-level path bob already owns cannot be hijacked by alice.
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/shared", Access: "public", Folder: "bob"})
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", apiv1.WebRoute{Folder: "alice", PathPrefix: "/shared", Access: "public"}); r.Code != 403 {
		t.Fatalf("claim hijack = %d want 403", r.Code)
	}
	// cross-folder target folder is still rejected by ownsFolder.
	if r := doJSON(t, h, "PUT", "/v1/web_routes", "", apiv1.WebRoute{Folder: "bob", PathPrefix: "/pub/bob/x", Access: "public"}); r.Code != 403 {
		t.Fatalf("cross-folder folder = %d want 403", r.Code)
	}
}
