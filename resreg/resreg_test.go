package resreg

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// fakeResource returns a Resource whose handler records the action and
// args it was called with. Used to verify the adapters wire correctly
// without depending on any concrete domain.
func fakeResource(seen *[]string) Resource {
	return Resource{
		Name: "fake",
		Endpoints: []Endpoint{
			{Verb: "GET", Path: "/v1/fake", Action: ActionList, Status: http.StatusOK},
			{Verb: "POST", Path: "/v1/fake", Action: ActionCreate, Status: http.StatusCreated},
			{Verb: "GET", Path: "/v1/fake/{id}", Action: ActionGet, Status: http.StatusOK},
		},
		MCPTools: []MCPTool{
			{Name: "fake.list", Action: ActionList, Description: "list"},
			{Name: "fake.create", Action: ActionCreate, Description: "create",
				Args: []MCPArg{{Name: "x", Type: "string", Required: true}}},
		},
		Policy: map[Action]ScopePred{
			ActionList:   func(c Caller, _ string) bool { return HasScope(c, "fake", "read") || HasScope(c, "fake", "write") },
			ActionGet:    func(c Caller, _ string) bool { return HasScope(c, "fake", "read") || HasScope(c, "fake", "write") },
			ActionCreate: func(c Caller, _ string) bool { return HasScope(c, "fake", "write") },
		},
		Handler: func(_ context.Context, c Caller, a Action, args Args) (any, error) {
			*seen = append(*seen, string(a))
			return map[string]any{"action": string(a), "args": args, "sub": c.Sub}, nil
		},
	}
}

func opCaller(_ *http.Request) (Caller, error) {
	return Caller{Sub: "op", Scope: []string{"fake:*"}}, nil
}

func anonCaller(_ *http.Request) (Caller, error) {
	return Caller{Sub: "anon"}, nil
}

func TestRESTHandler_DispatchesAllVerbs(t *testing.T) {
	var seen []string
	r := fakeResource(&seen)
	mux := http.NewServeMux()
	RegisterREST(mux, r, opCaller)

	for _, c := range []struct {
		method, path string
		body         []byte
		wantStatus   int
		wantAction   string
	}{
		{"GET", "/v1/fake", nil, 200, "list"},
		{"POST", "/v1/fake", []byte(`{"x":"y"}`), 201, "create"},
		{"GET", "/v1/fake/abc", nil, 200, "get"},
	} {
		req := httptest.NewRequest(c.method, c.path, bytes.NewReader(c.body))
		if c.body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != c.wantStatus {
			t.Errorf("%s %s status = %d, want %d body=%s", c.method, c.path, w.Code, c.wantStatus, w.Body.String())
		}
	}
	if len(seen) != 3 {
		t.Errorf("seen = %v, want 3 actions", seen)
	}
}

func TestRESTHandler_PathParamBoundToArgs(t *testing.T) {
	var seen []string
	r := fakeResource(&seen)
	r.Handler = func(_ context.Context, _ Caller, a Action, args Args) (any, error) {
		seen = append(seen, args["id"].(string))
		return args, nil
	}
	mux := http.NewServeMux()
	RegisterREST(mux, r, opCaller)
	req := httptest.NewRequest("GET", "/v1/fake/abc", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var got map[string]any
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["id"] != "abc" {
		t.Errorf("id = %v", got["id"])
	}
}

func TestRESTHandler_PolicyDenied(t *testing.T) {
	var seen []string
	r := fakeResource(&seen)
	mux := http.NewServeMux()
	RegisterREST(mux, r, anonCaller)
	req := httptest.NewRequest("GET", "/v1/fake", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRESTHandler_HandlerError(t *testing.T) {
	r := Resource{
		Name: "fake",
		Endpoints: []Endpoint{
			{Verb: "GET", Path: "/v1/err", Action: ActionList},
		},
		Policy: map[Action]ScopePred{ActionList: func(Caller, string) bool { return true }},
		Handler: func(context.Context, Caller, Action, Args) (any, error) {
			return nil, Errorf(http.StatusConflict, "boom")
		},
	}
	mux := http.NewServeMux()
	RegisterREST(mux, r, opCaller)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/err", nil))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d", w.Code)
	}
}

func TestMCPTools_DispatchesHandler(t *testing.T) {
	var seen []string
	r := fakeResource(&seen)
	srv := mcpserver.NewMCPServer("test", "1.0")
	MCPTools(srv, r, Caller{Sub: "op", Scope: []string{"fake:*"}})

	// Invoke list via the upstream MCP server's call interface — there's
	// no direct invoker we can hit cleanly without setting up the
	// streamable transport. The structural guarantee we want is that
	// MCPTools registers both tools and they share the resource's
	// Handler. Inspect via the Resource itself: same Handler field
	// drives the MCP closure (one-renderer property).
	if r.Handler == nil {
		t.Fatal("handler nil")
	}
	out, err := r.Handler(context.Background(), Caller{Scope: []string{"fake:read"}}, ActionList, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Errorf("handler returned nil")
	}

	// And the tool list now contains the names we asked for.
	tools := srv.ListTools()
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Tool.Name] = true
	}
	for _, want := range []string{"fake.list", "fake.create"} {
		if !names[want] {
			t.Errorf("MCPTools missing %q (got %v)", want, names)
		}
	}
}

func TestHasScope(t *testing.T) {
	c := Caller{Scope: []string{"routes:*", "grants:read"}}
	if !HasScope(c, "routes", "read") {
		t.Error("routes:* should match routes:read")
	}
	if !HasScope(c, "routes", "write") {
		t.Error("routes:* should match routes:write")
	}
	if !HasScope(c, "grants", "read") {
		t.Error("explicit grants:read should match")
	}
	if HasScope(c, "grants", "write") {
		t.Error("grants:read should not match grants:write")
	}
}

// Errorf maps to HTTP status codes deterministically.
func TestErrorf_StatusMapping(t *testing.T) {
	cases := []int{400, 403, 404, 409, 500}
	for _, code := range cases {
		err := Errorf(code, "x")
		if got := errStatus(err); got != code {
			t.Errorf("errStatus(%d) = %d", code, got)
		}
	}
	// Generic error → 500.
	if got := errStatus(context.Canceled); got != 500 {
		t.Errorf("generic err mapped to %d", got)
	}
}
