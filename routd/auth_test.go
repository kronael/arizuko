package routd

import (
	"net/http"
	"testing"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// fakeVerifier returns a fixed (sub, scope, folder) for any request — stands
// in for offline JWKs verification in the authz tests (mirrors runed's).
type fakeVerifier struct {
	sub    string
	scope  []string
	folder string
}

func (v fakeVerifier) Verify(*http.Request) (string, []string, string, error) {
	return v.sub, v.scope, v.folder, nil
}

func authSrv(t *testing.T, v Verifier) (*DB, http.Handler) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, v, 0, "")
	return db, srv.Handler()
}

// TestRouteWriteRequiresScope: the /v1/routes REST fold (spec 5/16) — POST add now
// wraps the route arg the shared handler reads (`{"route": ...}`). CRUD demands
// routes:write (the REST gate's scope check → 403), and a write-scoped token bound
// to a folder cannot target a route outside its subtree (the gate's ownsFolder — the
// containment that confines even a top-level tier-0 tenant; fails if it is dropped).
func TestRouteWriteRequiresScope(t *testing.T) {
	// read-only token → 403 on add (the gate's scope check).
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}, folder: "demo"})
	rec := doJSON(t, h, "POST", "/v1/routes", "", map[string]any{"route": apiv1.Route{Match: "platform=slack", Target: "demo"}})
	if rec.Code != 403 {
		t.Fatalf("add with read-only scope = %d want 403", rec.Code)
	}

	// write token bound to "demo" targeting "other" → 403 (the gate's ownsFolder).
	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "demo"})
	rec2 := doJSON(t, h2, "POST", "/v1/routes", "", map[string]any{"route": apiv1.Route{Match: "platform=slack", Target: "other"}})
	if rec2.Code != 403 {
		t.Fatalf("add targeting outside subtree = %d want 403", rec2.Code)
	}

	// write token bound to "demo" targeting "demo/sub" → 200 (descendant ok).
	rec3 := doJSON(t, h2, "POST", "/v1/routes", "", map[string]any{"route": apiv1.Route{Match: "platform=slack", Target: "demo/sub"}})
	if rec3.Code != 200 {
		t.Fatalf("add targeting own subtree = %d want 200 body=%s", rec3.Code, rec3.Body.String())
	}
}

func TestIngressRequiresWriteScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"chats:read"}})
	rec := doJSON(t, h, "POST", "/v1/messages", "",
		apiv1.Message{ChatJID: "slack:T/C/U", Content: "hi"})
	if rec.Code != 403 {
		t.Fatalf("ingress without messages:write = %d want 403", rec.Code)
	}
}
