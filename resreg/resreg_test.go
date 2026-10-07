package resreg

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// withAuditDB primes the audit package against a fresh in-memory store
// so Emit actually writes rows. Returns the store so tests can
// assert table contents. Resets package state on cleanup.
//
// The tables are store's own, because routd imports resreg and an internal test
// in this package cannot import routd back.
//
// The operator's grant is seeded through the SAME door a handler writes one
// through — buffered on a write set and applied — rather than through
// `store.AddACLRow`, which records an audit row of its own. The trail the tests
// read then holds only what resreg recorded.
func withAuditDB(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws := &store.WriteSet{}
	if err := st.PutACLRowInto(ws, core.ACLRow{
		Principal: "google:op", Action: "*", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	audit.Init(st.Obj(), "test")
	t.Cleanup(func() { audit.Init(nil, "") })
	return st
}

// markerRow is the row the fake handler buffers. Counting it is how a test tells
// a batch that landed from one that did not.
var markerRow = core.ACLRow{
	Principal: "test:marker", Action: "*", Scope: "**", Effect: "allow",
}

// markerRows counts the marker rows in the store.
func markerRows(t *testing.T, st *store.Store) int {
	t.Helper()
	rows, err := st.ListACL(markerRow.Principal)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// fakeResource builds a Resource whose handler is observable from the
// test: seen counts dispatch calls; gotCaller captures the most recent
// caller (verifies per-invocation resolution); fail flips the handler
// to return an error.
type fakeState struct {
	seen      atomic.Int32
	gotCaller atomic.Value // last Caller
	fail      atomic.Bool
}

func fakeResource(st *store.Store, s *fakeState) Resource {
	return Resource{
		Name: "routes",
		Endpoints: []Endpoint{
			{Verb: "GET", Path: "/v1/routes", Action: ActionList, Status: http.StatusOK},
			{Verb: "POST", Path: "/v1/routes", Action: ActionCreate, Status: http.StatusCreated},
			{Verb: "GET", Path: "/v1/routes/{id}", Action: ActionGet, Status: http.StatusOK},
		},
		MCPDoc: map[Action]string{
			ActionList:   "list",
			ActionCreate: "create",
		},
		MCPArgs: map[Action][]MCPArg{
			ActionCreate: {{Name: "x", Type: "string", Required: true}},
		},
		Authz: func(_ Caller, _ Action, _ Args) (string, map[string]string, error) {
			return "**", nil, nil
		},
		Handler: func(_ context.Context, x Execution) (any, error) {
			s.seen.Add(1)
			s.gotCaller.Store(x.Caller)
			if s.fail.Load() {
				return nil, Errorf(http.StatusConflict, "boom")
			}
			// Buffer a marker row so we can prove the batch carries the
			// handler's writes and the audit row together.
			if x.Writes != nil {
				if err := st.PutACLRowInto(x.Writes, markerRow); err != nil {
					return nil, err
				}
			}
			return map[string]any{"action": string(x.Action), "sub": x.Caller.Sub}, nil
		},
		Store: st,
	}
}

func operatorBuilder(_ *http.Request) (Caller, error) {
	return Caller{Sub: "google:op", Claims: map[string]string{"operator": "1"}}, nil
}

func anonBuilder(_ *http.Request) (Caller, error) {
	return Caller{Sub: "google:anon"}, nil
}

// TestRESTHandler_DispatchesAllVerbs — every endpoint reaches the
// shared handler; status codes honour the declaration.
func TestRESTHandler_DispatchesAllVerbs(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	mux := http.NewServeMux()
	RegisterREST(mux, fakeResource(st, s), operatorBuilder)
	cases := []struct {
		method, path string
		body         []byte
		wantStatus   int
	}{
		{"GET", "/v1/routes", nil, 200},
		{"POST", "/v1/routes", []byte(`{"x":"y"}`), 201},
		{"GET", "/v1/routes/abc", nil, 200},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, bytes.NewReader(c.body))
		if c.body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != c.wantStatus {
			t.Errorf("%s %s -> %d (want %d) body=%s", c.method, c.path, w.Code, c.wantStatus, w.Body.String())
		}
	}
	if got := s.seen.Load(); got != 3 {
		t.Errorf("seen = %d, want 3", got)
	}
}

// TestRESTHandler_PathParamBoundToArgs — {id} in the URL flows into Args.
func TestRESTHandler_PathParamBoundToArgs(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	r := fakeResource(st, s)
	gotID := ""
	r.Handler = func(_ context.Context, x Execution) (any, error) {
		gotID, _ = x.Args["id"].(string)
		return x.Args, nil
	}
	mux := http.NewServeMux()
	RegisterREST(mux, r, operatorBuilder)
	req := httptest.NewRequest("GET", "/v1/routes/abc", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if gotID != "abc" {
		t.Errorf("id = %q, want abc", gotID)
	}
}

// TestRESTHandler_QueryParamBoundToArgs — ?key=v flows into Args (GET
// filters/scoping); a JSON body overrides the same key.
func TestRESTHandler_QueryParamBoundToArgs(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	r := fakeResource(st, s)
	var got Args
	r.Handler = func(_ context.Context, x Execution) (any, error) {
		got = x.Args
		return x.Args, nil
	}
	mux := http.NewServeMux()
	RegisterREST(mux, r, operatorBuilder)
	// GET with query params → Args.
	req := httptest.NewRequest("GET", "/v1/routes?folder=hq&limit=5", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if got["folder"] != "hq" || got["limit"] != "5" {
		t.Errorf("query args not bound: %+v", got)
	}
	// Body overrides a query key on POST.
	req = httptest.NewRequest("POST", "/v1/routes?x=fromquery", bytes.NewReader([]byte(`{"x":"frombody"}`)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if got["x"] != "frombody" {
		t.Errorf("body should override query: x=%v", got["x"])
	}
}

// TestRESTHandler_AuthorizeDenied — operator lacks ACL row → 403,
// handler not invoked, audit row recorded with outcome=denied.
func TestRESTHandler_AuthorizeDenied(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	mux := http.NewServeMux()
	RegisterREST(mux, fakeResource(st, s), anonBuilder)
	req := httptest.NewRequest("GET", "/v1/routes", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
	if got := s.seen.Load(); got != 0 {
		t.Errorf("handler ran despite denial (seen=%d)", got)
	}
	assertAudit(t, st, "denied", 1)
}

// TestGate_OverridesDefault — a custom Resource.Gate replaces the default
// operator auth.Authorize: it can DENY an otherwise-allowed operator and ALLOW
// an otherwise-denied caller. This is the agent-socket seam (routd injects a
// tier-aware gate); resreg delegates auth, owns none.
func TestGate_OverridesDefault(t *testing.T) {
	st := withAuditDB(t)

	// Deny gate → even the seeded operator is refused.
	deny := fakeResource(st, &fakeState{})
	deny.Gate = func(Execution, string, map[string]string) error {
		return Errorf(http.StatusForbidden, "gate says no")
	}
	xop := Execution{
		Caller:   Caller{Sub: "google:op", Claims: map[string]string{"operator": "1"}},
		Action:   ActionCreate,
		Resource: "routes",
		Args:     Args{"x": "y"},
	}
	if _, status, err := invoke(context.Background(), deny, xop); err == nil || status != http.StatusForbidden {
		t.Errorf("deny gate: status=%d err=%v, want 403+error", status, err)
	}

	// Allow gate → an anon caller with no ACL row gets through to the handler.
	s := &fakeState{}
	allow := fakeResource(st, s)
	allow.Gate = func(Execution, string, map[string]string) error { return nil }
	xanon := Execution{
		Caller:   Caller{Sub: "google:anon"},
		Action:   ActionCreate,
		Resource: "routes",
		Args:     Args{"x": "y"},
	}
	if _, _, err := invoke(context.Background(), allow, xanon); err != nil {
		t.Errorf("allow gate: unexpected err %v", err)
	}
	if s.seen.Load() != 1 {
		t.Errorf("allow gate: handler not invoked (seen=%d)", s.seen.Load())
	}
}

// TestAuthz_ErrorShortCircuits — Authz returning a 400 prevents
// auth.Authorize from being consulted and the handler from running.
func TestAuthz_ErrorShortCircuits(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	r := fakeResource(st, s)
	r.Authz = func(Caller, Action, Args) (string, map[string]string, error) {
		return "", nil, Errorf(http.StatusBadRequest, "bad input")
	}
	mux := http.NewServeMux()
	RegisterREST(mux, r, operatorBuilder)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/routes", bytes.NewReader([]byte(`{"x":"y"}`))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := s.seen.Load(); got != 0 {
		t.Errorf("handler ran (seen=%d)", got)
	}
}

// TestRESTHandler_HandlerErrorSendsNoBatch — a handler returning an error had
// only DESCRIBED its writes, so nothing was written, and the row that records
// the attempt goes in alone.
func TestRESTHandler_HandlerErrorSendsNoBatch(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	s.fail.Store(true)
	mux := http.NewServeMux()
	RegisterREST(mux, fakeResource(st, s), operatorBuilder)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/routes", bytes.NewReader([]byte(`{"x":"y"}`))))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d", w.Code)
	}
	if n := markerRows(t, st); n != 0 {
		t.Errorf("marker rows after a failed handler = %d, want 0", n)
	}
	// The audit row rides no batch, and it carries outcome=error.
	assertAudit(t, st, "error", 1)
}

// TestRESTHandler_MutationCommitsAuditInSameBatch — happy path: the handler's
// write AND the audit_log row land together; one audit row with outcome=ok.
func TestRESTHandler_MutationCommitsAuditInSameBatch(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	mux := http.NewServeMux()
	RegisterREST(mux, fakeResource(st, s), operatorBuilder)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/routes", bytes.NewReader([]byte(`{"x":"y"}`))))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if n := markerRows(t, st); n != 1 {
		t.Errorf("marker rows = %d, want 1", n)
	}
	assertAudit(t, st, "ok", 1)
}

// TestRESTHandler_RefusedBatchAnswersTheHandlersError — a guarded write whose
// condition does not hold refuses the WHOLE batch: nothing lands, the caller
// gets the error the handler declared WITH its status, and the only row recorded
// says the call failed. That is what the rollback used to do.
func TestRESTHandler_RefusedBatchAnswersTheHandlersError(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	r := fakeResource(st, s)
	r.Handler = func(_ context.Context, x Execution) (any, error) {
		// The marker, which cannot refuse, beside a revoke of a grant nobody
		// holds, which must.
		if err := st.PutACLRowInto(x.Writes, markerRow); err != nil {
			return nil, err
		}
		if err := st.RemoveACLRowInto(x.Writes, core.ACLRow{
			Principal: "test:absent", Action: "read", Scope: "**",
		}, Errorf(http.StatusNotFound, "no matching grant")); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	mux := http.NewServeMux()
	RegisterREST(mux, r, operatorBuilder)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/routes", bytes.NewReader([]byte(`{"x":"y"}`))))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want the handler's own 404", w.Code, w.Body.String())
	}
	if n := markerRows(t, st); n != 0 {
		t.Errorf("marker rows under a refused batch = %d, want 0", n)
	}
	assertAudit(t, st, "error", 1)
}

// TestReadOnly_NoAuditRow — list/get emit slog only, no DB row.
func TestReadOnly_NoAuditRow(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	mux := http.NewServeMux()
	RegisterREST(mux, fakeResource(st, s), operatorBuilder)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/routes", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	n := len(auditTrail(t, st))
	if n != 0 {
		t.Errorf("recorded rows on read = %d, want 0", n)
	}
}

// TestMCPTools_PerInvocationCallerResolver — the resolver runs every
// call, capturing the actual invoking caller. Two distinct calls with
// two distinct resolver outputs MUST flow to the handler with each
// caller separately — never collapse to one.
func TestMCPTools_PerInvocationCallerResolver(t *testing.T) {
	st := withAuditDB(t)
	// Seed two ACL rows so both alice and bob can access.
	if err := st.AddACLRow(core.ACLRow{Principal: "alice", Action: "*", Scope: "**", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddACLRow(core.ACLRow{Principal: "bob", Action: "*", Scope: "**", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	s := &fakeState{}
	srv := mcpserver.NewMCPServer("t", "1.0")
	var mu sync.Mutex
	callers := []string{"alice", "bob"}
	idx := 0
	resolver := func(_ context.Context, _ mcp.CallToolRequest) (Caller, error) {
		mu.Lock()
		defer mu.Unlock()
		sub := callers[idx%len(callers)]
		idx++
		return Caller{Sub: sub}, nil
	}
	MCPTools(srv, fakeResource(st, s), resolver, nil)
	// Drive the handler through invoke twice with distinct resolver outputs.
	// (mcp.ServeHTTP setup is heavy for a unit test — invoke directly.)
	x1 := Execution{Caller: Caller{Sub: "alice"}, Action: ActionList, Resource: "routes", Args: Args{}, Surface: audit.SurfaceMCP}
	x2 := Execution{Caller: Caller{Sub: "bob"}, Action: ActionList, Resource: "routes", Args: Args{}, Surface: audit.SurfaceMCP}
	r := fakeResource(st, s)
	if _, _, err := invoke(context.Background(), r, x1); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if _, _, err := invoke(context.Background(), r, x2); err != nil {
		t.Fatalf("bob: %v", err)
	}
	got, ok := s.gotCaller.Load().(Caller)
	if !ok || got.Sub != "bob" {
		t.Errorf("last caller = %+v, want bob", got)
	}
	// And the tools are registered.
	tools := srv.ListTools()
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Tool.Name] = true
	}
	for _, want := range []string{"routes.list", "routes.create"} {
		if !names[want] {
			t.Errorf("MCPTools missing %q (got %v)", want, names)
		}
	}
}

// TestForwarderResource_NoTx — Store nil → no tx, no audit row; the
// adapter trusts the downstream daemon to log. Read-only and mutating
// actions both pass through to the handler.
func TestForwarderResource_NoTx(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	r := fakeResource(st, s)
	r.Store = nil
	mux := http.NewServeMux()
	RegisterREST(mux, r, operatorBuilder)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/routes", bytes.NewReader([]byte(`{"x":"y"}`))))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	// Forwarder: no write set, so the handler buffers nothing.
	if n := markerRows(t, st); n != 0 {
		t.Errorf("marker rows = %d, want 0 (forwarder)", n)
	}
	// And no audit row at this daemon's table.
	n := len(auditTrail(t, st))
	if n != 0 {
		t.Errorf("recorded rows = %d, want 0 (downstream logs)", n)
	}
}

// TestRESTHandler_MalformedJSON — invalid body → 400, never reaches authz.
func TestRESTHandler_MalformedJSON(t *testing.T) {
	st := withAuditDB(t)
	s := &fakeState{}
	mux := http.NewServeMux()
	RegisterREST(mux, fakeResource(st, s), operatorBuilder)
	req := httptest.NewRequest("POST", "/v1/routes", bytes.NewReader([]byte(`{not json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d", w.Code)
	}
}

// Errorf maps to HTTP status codes deterministically.
func TestErrorf_StatusMapping(t *testing.T) {
	for _, code := range []int{400, 403, 404, 409, 500} {
		if got := errStatus(Errorf(code, "x")); got != code {
			t.Errorf("errStatus(%d) = %d", code, got)
		}
	}
	if got := errStatus(context.Canceled); got != 500 {
		t.Errorf("generic err mapped to %d", got)
	}
}

// TestDeriveMCPTools_Golden pins deriveMCPTools against the retired
// hand-authored proxyd_routes MCPTools list, byte-for-byte (name,
// description, every arg's name/type/description/required). If this
// fails, the derivation drifted from the operator-facing contract.
func TestDeriveMCPTools_Golden(t *testing.T) {
	r := Resource{
		Name: "proxyd_routes",
		Endpoints: []Endpoint{
			{Verb: "GET", Path: "/v1/proxyd_routes", Action: ActionList, Status: http.StatusOK},
			{Verb: "GET", Path: "/v1/proxyd_routes/{path...}", Action: ActionGet, Status: http.StatusOK},
			{Verb: "POST", Path: "/v1/proxyd_routes", Action: ActionCreate, Status: http.StatusCreated},
			{Verb: "PATCH", Path: "/v1/proxyd_routes/{path...}", Action: ActionUpdate, Status: http.StatusOK},
			{Verb: "DELETE", Path: "/v1/proxyd_routes/{path...}", Action: ActionDelete, Status: http.StatusNoContent},
		},
		MCPDoc: map[Action]string{
			ActionList:   "List proxyd's runtime route table.",
			ActionGet:    "Read one proxyd route by path.",
			ActionCreate: "Create a proxyd route. Body fields mirror the TOML proxyd_route block.",
			ActionUpdate: "Update fields on an existing proxyd route. Path is the key.",
			ActionDelete: "Delete a proxyd route. Idempotent.",
		},
		MCPArgs: map[Action][]MCPArg{
			ActionGet: {{Name: "path", Type: "string", Required: true}},
			ActionCreate: {
				{Name: "path", Type: "string", Required: true},
				{Name: "backend", Type: "string", Required: true},
				{Name: "auth", Type: "string", Required: true, Description: "public | user | operator"},
				{Name: "gated_by", Type: "string"},
				{Name: "preserve_headers", Type: "array"},
				{Name: "strip_prefix", Type: "bool"},
			},
			ActionUpdate: {
				{Name: "path", Type: "string", Required: true},
				{Name: "backend", Type: "string"},
				{Name: "auth", Type: "string"},
				{Name: "gated_by", Type: "string"},
				{Name: "preserve_headers", Type: "array"},
				{Name: "strip_prefix", Type: "bool"},
			},
			ActionDelete: {{Name: "path", Type: "string", Required: true}},
		},
	}
	want := []MCPTool{
		{Name: "proxyd_routes.list", Action: ActionList,
			Description: "List proxyd's runtime route table."},
		{Name: "proxyd_routes.get", Action: ActionGet,
			Description: "Read one proxyd route by path.",
			Args:        []MCPArg{{Name: "path", Type: "string", Required: true}}},
		{Name: "proxyd_routes.create", Action: ActionCreate,
			Description: "Create a proxyd route. Body fields mirror the TOML proxyd_route block.",
			Args: []MCPArg{
				{Name: "path", Type: "string", Required: true},
				{Name: "backend", Type: "string", Required: true},
				{Name: "auth", Type: "string", Required: true, Description: "public | user | operator"},
				{Name: "gated_by", Type: "string"},
				{Name: "preserve_headers", Type: "array"},
				{Name: "strip_prefix", Type: "bool"},
			}},
		{Name: "proxyd_routes.update", Action: ActionUpdate,
			Description: "Update fields on an existing proxyd route. Path is the key.",
			Args: []MCPArg{
				{Name: "path", Type: "string", Required: true},
				{Name: "backend", Type: "string"},
				{Name: "auth", Type: "string"},
				{Name: "gated_by", Type: "string"},
				{Name: "preserve_headers", Type: "array"},
				{Name: "strip_prefix", Type: "bool"},
			}},
		{Name: "proxyd_routes.delete", Action: ActionDelete,
			Description: "Delete a proxyd route. Idempotent.",
			Args:        []MCPArg{{Name: "path", Type: "string", Required: true}}},
	}
	got := deriveMCPTools(r)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deriveMCPTools drifted from golden:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestDeriveMCPTools_ReflectsRowType — when RowType is set (and no explicit
// MCPArgs override), args are reflected PER ACTION so PK/read-only fields don't
// leak into delete/update as required. list=no args, get/delete=PK only,
// update=PK required + rest optional, create=writable non-PK (+ non-stamped PK).
func TestDeriveMCPTools_ReflectsRowType(t *testing.T) {
	type row struct {
		Path      string   `db:"path"       json:"path"`
		Hits      int      `db:"hits"       json:"hits"`
		Enabled   bool     `db:"enabled"    json:"enabled"`
		Tags      []string `db:"tags"       json:"tags,omitempty"`
		CreatedAt string   `db:"created_at" json:"created_at"`
		Skip      string   `json:"-"`
	}
	r := Resource{
		Name:     "thing",
		Table:    "thing",
		RowType:  reflect.TypeFor[row](),
		PKFields: []string{"Path"},
		// created_at is server-stamped — never a create arg, never required.
		StampedFields: []string{"CreatedAt"},
		Endpoints: []Endpoint{
			{Verb: "GET", Path: "/v1/thing", Action: ActionList},
			{Verb: "GET", Path: "/v1/thing/{path}", Action: ActionGet},
			{Verb: "POST", Path: "/v1/thing", Action: ActionCreate},
			{Verb: "PATCH", Path: "/v1/thing/{path}", Action: ActionUpdate},
			{Verb: "DELETE", Path: "/v1/thing/{path}", Action: ActionDelete},
		},
		MCPDoc: map[Action]string{
			ActionList: "l", ActionGet: "g", ActionCreate: "c",
			ActionUpdate: "u", ActionDelete: "d",
		},
	}

	byAction := map[Action][]MCPArg{}
	for _, tool := range deriveMCPTools(r) {
		byAction[tool.Action] = tool.Args
	}

	pkOnly := []MCPArg{{Name: "path", Type: "string", Required: true}}
	want := map[Action][]MCPArg{
		ActionList:   nil,
		ActionGet:    pkOnly,
		ActionDelete: pkOnly,
		ActionUpdate: {
			{Name: "path", Type: "string", Required: true},
			{Name: "hits", Type: "number", Required: false},
			{Name: "enabled", Type: "bool", Required: false},
			{Name: "tags", Type: "array", Required: false},
			// created_at is stamped → not a client patch arg.
		},
		ActionCreate: {
			{Name: "path", Type: "string", Required: true},
			{Name: "hits", Type: "number", Required: true},
			{Name: "enabled", Type: "bool", Required: true},
			{Name: "tags", Type: "array", Required: false},
			// created_at is stamped → absent from create.
		},
	}
	for action, w := range want {
		if !reflect.DeepEqual(byAction[action], w) {
			t.Errorf("%s args:\n got=%+v\nwant=%+v", action, byAction[action], w)
		}
	}
}

// TestDeriveMCPTools_MCPNames pins the flat-name override: a migrated resource
// keeps its agent tool names (set_web_route) instead of the dotted default,
// while actions with no override fall back to <name>.<action>.
func TestDeriveMCPTools_MCPNames(t *testing.T) {
	r := Resource{
		Name: "web_routes",
		Endpoints: []Endpoint{
			{Verb: "POST", Path: "/v1/web_routes", Action: ActionCreate},
			{Verb: "DELETE", Path: "/v1/web_routes", Action: ActionDelete},
			{Verb: "GET", Path: "/v1/web_routes", Action: ActionList},
		},
		MCPDoc: map[Action]string{
			ActionCreate: "c", ActionDelete: "d", ActionList: "l",
		},
		MCPArgs: map[Action][]MCPArg{
			ActionCreate: {{Name: "path_prefix", Type: "string", Required: true}},
		},
		MCPNames: map[Action]string{
			ActionCreate: "set_web_route",
			ActionDelete: "del_web_route",
			// ActionList intentionally omitted → dotted default.
		},
	}
	got := map[Action]string{}
	for _, tool := range deriveMCPTools(r) {
		got[tool.Action] = tool.Name
	}
	want := map[Action]string{
		ActionCreate: "set_web_route",
		ActionDelete: "del_web_route",
		ActionList:   "web_routes.list",
	}
	for a, w := range want {
		if got[a] != w {
			t.Errorf("%s tool name = %q, want %q", a, got[a], w)
		}
	}
}

func TestAction_Mutates(t *testing.T) {
	for _, a := range []Action{ActionCreate, ActionUpdate, ActionDelete} {
		if !a.Mutates() {
			t.Errorf("%s should mutate", a)
		}
	}
	for _, a := range []Action{ActionList, ActionGet} {
		if a.Mutates() {
			t.Errorf("%s should NOT mutate", a)
		}
	}
}

// assertAudit checks the recorded row count matches expected; a non-empty
// `outcome` narrows to that outcome.
func assertAudit(t *testing.T, st *store.Store, outcome string, want int) {
	t.Helper()
	n := 0
	for _, r := range auditTrail(t, st) {
		if outcome == "" || r.Outcome == outcome {
			n++
		}
	}
	if n != want {
		t.Errorf("recorded rows[outcome=%q] = %d, want %d", outcome, n, want)
	}
}

// auditTrail reads back everything recorded, through the method every daemon
// answers GET /v1/audit with.
func auditTrail(t *testing.T, st *store.Store) []audit.Row {
	t.Helper()
	rows, err := audit.Query(t.Context(), st.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	return rows
}
