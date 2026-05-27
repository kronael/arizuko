package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

const testHMAC = "test-hmac-secret"

// testResourceMux builds a stdlib mux with the routes resource mounted at
// /v1/routes, backed by an in-memory store. callerBuild lets a test swap
// in a custom caller; nil uses the operator-grade builder.
func testResourceMux(t *testing.T, callerBuild resreg.CallerFromHTTPFunc) (*http.ServeMux, *routesResource, *store.Store) {
	t.Helper()
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Seed ACL so the operator (predicate operator=1) admits everywhere.
	// Spec 4/9 — operator is emergent from the `**` row, not a role.
	if err := st.AddACLRow(core.ACLRow{
		Principal: "**", Action: "*", Scope: "**",
		Effect: "allow", Predicate: "operator=1",
	}); err != nil {
		t.Fatal(err)
	}
	rr := newRoutesResource(st, nil)
	if callerBuild == nil {
		callerBuild = callerFromHTTP(testHMAC)
	}
	mux := http.NewServeMux()
	resreg.RegisterREST(mux, routesResourceDecl(rr), callerBuild)
	return mux, rr, st
}

// signOperator stamps the request with operator identity (the `**`
// group marker, today's operator gate). Matches what proxyd injects on
// the authenticated REST surface.
func signOperator(req *http.Request) {
	groups := `["**"]`
	req.Header.Set("X-User-Sub", "op@example")
	req.Header.Set("X-User-Name", "op")
	req.Header.Set("X-User-Groups", groups)
	req.Header.Set("X-User-Sig",
		auth.SignHMAC(testHMAC, auth.UserSigMessage("op@example", "op", groups)))
}

func doJSON(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	signOperator(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestRoutesResource_EndToEnd(t *testing.T) {
	mux, rr, _ := testResourceMux(t, nil)

	// create
	w := doJSON(t, mux, "POST", "/v1/routes", map[string]any{
		"path": "/slack/", "backend": "http://slakd:8080", "auth": "public",
		"preserve_headers": []string{"X-Slack-Signature"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST status = %d body=%s", w.Code, w.Body.String())
	}

	// route must be visible immediately via the DB-backed snapshot
	if cur, _ := rr.snapshot(); len(cur) != 1 || cur[0].Path != "/slack/" {
		t.Errorf("rr.snapshot() = %+v", cur)
	}

	// list
	w = doJSON(t, mux, "GET", "/v1/routes", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET list = %d", w.Code)
	}
	var listResp struct {
		Routes []Route `json:"routes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Routes) != 1 {
		t.Errorf("list = %+v", listResp.Routes)
	}

	// get — `/slack/` urlencoded
	w = doJSON(t, mux, "GET", "/v1/routes/"+url.PathEscape("/slack/"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET one = %d body=%s", w.Code, w.Body.String())
	}

	// patch
	w = doJSON(t, mux, "PATCH", "/v1/routes/"+url.PathEscape("/slack/"), map[string]any{
		"backend": "http://slakd2:8080",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH = %d body=%s", w.Code, w.Body.String())
	}
	cur, _ := rr.snapshot()
	if len(cur) == 0 || cur[0].Backend != "http://slakd2:8080" {
		t.Errorf("backend after patch = %+v", cur)
	}

	// delete
	w = doJSON(t, mux, "DELETE", "/v1/routes/"+url.PathEscape("/slack/"), nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", w.Code)
	}
	if cur, _ := rr.snapshot(); len(cur) != 0 {
		t.Errorf("rr.snapshot() after delete = %+v", cur)
	}

	// delete is idempotent
	w = doJSON(t, mux, "DELETE", "/v1/routes/"+url.PathEscape("/slack/"), nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE idempotent = %d", w.Code)
	}
}

func TestRoutesResource_Validation(t *testing.T) {
	mux, _, _ := testResourceMux(t, nil)

	// no leading slash
	w := doJSON(t, mux, "POST", "/v1/routes", map[string]any{
		"path": "slack/", "backend": "http://x", "auth": "public",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("no slash = %d", w.Code)
	}

	// empty backend
	w = doJSON(t, mux, "POST", "/v1/routes", map[string]any{
		"path": "/x/", "backend": "", "auth": "public",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty backend = %d", w.Code)
	}

	// unknown auth
	w = doJSON(t, mux, "POST", "/v1/routes", map[string]any{
		"path": "/y/", "backend": "http://x", "auth": "admin",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad auth = %d", w.Code)
	}

	// duplicate
	w = doJSON(t, mux, "POST", "/v1/routes", map[string]any{
		"path": "/z/", "backend": "http://x", "auth": "public",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("first POST = %d", w.Code)
	}
	w = doJSON(t, mux, "POST", "/v1/routes", map[string]any{
		"path": "/z/", "backend": "http://x", "auth": "public",
	})
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate = %d", w.Code)
	}
}

func TestRoutesResource_GetNotFound(t *testing.T) {
	mux, _, _ := testResourceMux(t, nil)
	w := doJSON(t, mux, "GET", "/v1/routes/"+url.PathEscape("/nope/"), nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRoutesResource_PatchNotFound(t *testing.T) {
	mux, _, _ := testResourceMux(t, nil)
	w := doJSON(t, mux, "PATCH", "/v1/routes/"+url.PathEscape("/nope/"), map[string]any{
		"backend": "http://x",
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRoutesResource_Unauthorized(t *testing.T) {
	mux, _, _ := testResourceMux(t, nil)
	req := httptest.NewRequest("GET", "/v1/routes", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d", w.Code)
	}
}

func TestRoutesResource_NonOperatorForbidden(t *testing.T) {
	mux, _, _ := testResourceMux(t, nil)
	groups := `["atlas/support"]`
	req := httptest.NewRequest("GET", "/v1/routes", nil)
	req.Header.Set("X-User-Sub", "user@example")
	req.Header.Set("X-User-Name", "user")
	req.Header.Set("X-User-Groups", groups)
	req.Header.Set("X-User-Sig",
		auth.SignHMAC(testHMAC, auth.UserSigMessage("user@example", "user", groups)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-operator status = %d body=%s", w.Code, w.Body.String())
	}
}

// Persistence: a new routesResource opened against the same store sees
// what the previous one wrote. Validates the "mutations are durable"
// invariant from spec 6/2 Phase-3.
func TestRoutesResource_Durable(t *testing.T) {
	mux, _, st := testResourceMux(t, nil)
	w := doJSON(t, mux, "POST", "/v1/routes", map[string]any{
		"path": "/api/", "backend": "http://api:8080", "auth": "user",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d", w.Code)
	}
	// simulate restart: rehydrate from db
	stored, err := st.AllProxydRoutes()
	if err != nil || len(stored) != 1 || stored[0].Path != "/api/" {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
	fresh := make([]Route, 0, len(stored))
	for _, r := range stored {
		fresh = append(fresh, fromStoreRoute(r))
	}
	rr2 := newRoutesResource(st, fresh)
	if cur, _ := rr2.snapshot(); len(cur) != 1 || cur[0].Backend != "http://api:8080" {
		t.Errorf("rehydrated = %+v", cur)
	}
}

// One Handler, two adapters: the handler used by REST is the exact same
// function value the MCP tool dispatches to. Spec 5/5 §"One renderer,
// many sinks" — structurally proves no duplication.
func TestRoutesResource_SingleHandler(t *testing.T) {
	st, _ := store.OpenMem()
	defer st.Close()
	rr := newRoutesResource(st, nil)
	res := routesResourceDecl(rr)
	if res.Handler == nil {
		t.Fatal("handler nil")
	}
	// Sanity: List works in-process via the Handler field (read-only,
	// no tx needed).
	x := resreg.Execution{
		Caller:   resreg.Caller{Sub: "op"},
		Action:   resreg.ActionList,
		Resource: "routes",
		Args:     resreg.Args{},
	}
	out, err := res.Handler(nil, x)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOf(out), `"routes":`) {
		t.Errorf("list out = %s", jsonOf(out))
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
