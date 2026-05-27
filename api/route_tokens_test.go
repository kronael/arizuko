package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// fakeRouteTokenFns gives the api.Server an in-memory writer/reader so
// tests don't drag in the full gateway.
func wireFakeRouteTokens(t *testing.T, srv *Server, st *store.Store) {
	t.Helper()
	fns := ipc.GatedFns{
		IssueRouteToken: func(kind, owner, target, src, suffix string) (ipc.RouteTokenInfo, error) {
			var jid string
			switch kind {
			case "chat":
				jid = "web:" + target
			case "hook":
				jid = "hook:" + target + "/" + src
			}
			if suffix != "" {
				jid += "/" + suffix
			}
			raw := store.GenRouteToken()
			if err := st.InsertRouteToken(raw, store.RouteToken{JID: jid, OwnerFolder: owner}); err != nil {
				return ipc.RouteTokenInfo{}, err
			}
			return ipc.RouteTokenInfo{RawToken: raw, JID: jid, OwnerFolder: owner}, nil
		},
		ListRouteTokens: func(owner string) []ipc.RouteTokenInfo {
			rows := st.ListRouteTokens(owner)
			out := make([]ipc.RouteTokenInfo, len(rows))
			for i, r := range rows {
				out[i] = ipc.RouteTokenInfo{JID: r.JID, OwnerFolder: r.OwnerFolder}
			}
			return out
		},
		RevokeRouteToken: st.RevokeRouteToken,
	}
	srv.SetRouteTokenFns(fns, "test-hmac")
}

func operatorReq(method, path string, body any) *http.Request {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	sub := "user-op"
	name := "Operator"
	groups := `["**"]`
	r.Header.Set("X-User-Sub", sub)
	r.Header.Set("X-User-Name", name)
	r.Header.Set("X-User-Groups", groups)
	r.Header.Set("X-User-Sig", auth.SignHMAC("test-hmac", auth.UserSigMessage(sub, name, groups)))
	return r
}

func TestRouteTokens_CreateListRevoke(t *testing.T) {
	srv, _, st := setup(t)
	wireFakeRouteTokens(t, srv, st)
	h := srv.Handler()
	// 0069 added FK route_tokens.owner_folder → groups.folder. Seed the
	// group before minting tokens against it.
	if err := st.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}

	// Create chat.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, operatorReq("POST", "/v1/route_tokens", map[string]any{
		"kind":          "chat",
		"target_folder": "acme",
	}))
	if w.Code != 200 {
		t.Fatalf("create chat: %d %s", w.Code, w.Body.String())
	}
	var info ipc.RouteTokenInfo
	json.Unmarshal(w.Body.Bytes(), &info)
	if info.JID != "web:acme" || info.RawToken == "" {
		t.Fatalf("bad info: %+v", info)
	}

	// Create hook.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, operatorReq("POST", "/v1/route_tokens", map[string]any{
		"kind":          "hook",
		"target_folder": "acme",
		"source_label":  "github",
	}))
	if w.Code != 200 {
		t.Fatalf("create hook: %d %s", w.Code, w.Body.String())
	}
	var hook ipc.RouteTokenInfo
	json.Unmarshal(w.Body.Bytes(), &hook)
	if hook.JID != "hook:acme/github" {
		t.Fatalf("bad hook jid: %s", hook.JID)
	}

	// List.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, operatorReq("GET", "/v1/route_tokens?owner_folder=acme", nil))
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listResp struct{ Tokens []ipc.RouteTokenInfo }
	json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Tokens) != 2 {
		t.Fatalf("want 2 tokens, got %d", len(listResp.Tokens))
	}

	// Revoke chat.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, operatorReq("DELETE", "/v1/route_tokens/web:acme?owner_folder=acme", nil))
	if w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
}

func TestRouteTokens_RequiresOperator(t *testing.T) {
	srv, _, st := setup(t)
	wireFakeRouteTokens(t, srv, st)
	h := srv.Handler()

	// No headers → 401.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/route_tokens?owner_folder=acme", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed: want 401, got %d", w.Code)
	}

	// Signed but not operator → 403.
	r := httptest.NewRequest("GET", "/v1/route_tokens?owner_folder=acme", nil)
	sub := "user-foo"
	name := ""
	groups := `["other"]`
	r.Header.Set("X-User-Sub", sub)
	r.Header.Set("X-User-Name", name)
	r.Header.Set("X-User-Groups", groups)
	r.Header.Set("X-User-Sig", auth.SignHMAC("test-hmac", auth.UserSigMessage(sub, name, groups)))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-operator: want 403, got %d", w.Code)
	}
}
