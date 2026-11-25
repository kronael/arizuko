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

// TestRouteWriteRequiresScope: the /v1/routes REST fold (resreg/README.md) — POST add now
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

func TestRESTRouteTokensRejectCrossFolder(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}, folder: "alice"})
	r := doGet(t, h, "/v1/route_tokens?owner_folder=bob")
	if r.Code != http.StatusForbidden {
		t.Fatalf("cross-folder route tokens = %d, want 403: %s", r.Code, r.Body.String())
	}
}

func TestOutboundFolderBoundary(t *testing.T) {
	db := memDB(t)
	deliver := &recDeliverer{pid: "sent"}
	for _, tc := range []struct {
		folder string
		jid    string
		want   int
	}{{"alice", "web:alice", 200}, {"alice", "web:alice/team", 200},
		{"alice", "web:bob", 403}, {"", "web:bob", 200}} {
		v := fakeVerifier{sub: "user:a", folder: tc.folder, scope: []string{"messages:write"}}
		h := NewServer(db, nil, deliver, v, 0, "").Handler()
		before := len(deliver.sends)
		r := doJSON(t, h, "POST", "/v1/outbound", "", apiv1.OutboundRequest{JID: tc.jid, Text: "hello"})
		if r.Code != tc.want {
			t.Errorf("folder %q jid %q: status %d, want %d", tc.folder, tc.jid, r.Code, tc.want)
		}
		if tc.want == 403 {
			if len(deliver.sends) != before {
				t.Error("denied outbound delivered a message")
			}
			rows, err := db.MessagesBefore(tc.jid, "", 10)
			if err != nil || len(rows) != 0 {
				t.Errorf("denied outbound stored rows: %v, err: %v", rows, err)
			}
		}
	}
}
