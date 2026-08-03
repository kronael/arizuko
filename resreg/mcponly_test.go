package resreg

// An MCPOnly endpoint declares an action that has no human caller: the agent
// tool is still DERIVED from the one endpoint list, but RegisterREST does not
// mount it and /openapi.json does not advertise it (spec 5/31's
// issue_pairing_link).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type mcpOnlyRow struct {
	Name string `db:"name" json:"name"`
}

func mcpOnlyResource() Resource {
	return Resource{
		Name:     "widgets",
		Table:    "widgets",
		RowType:  reflect.TypeFor[mcpOnlyRow](),
		PKFields: []string{"Name"},
		Endpoints: []Endpoint{
			{Verb: "GET", Path: "/v1/widgets", Action: ActionList},
			{Action: Action("nudge"), MCPOnly: true},
		},
		MCPDoc: map[Action]string{
			ActionList:      "List widgets.",
			Action("nudge"): "Nudge a widget. Agent-only.",
		},
		MCPArgs: map[Action][]MCPArg{
			Action("nudge"): {{Name: "name", Type: "string", Required: true}},
		},
		Authz: func(Caller, Action, Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: func(context.Context, Execution) (any, error) { return nil, nil },
	}
}

func TestMCPOnly_DerivesToolWithoutRESTFace(t *testing.T) {
	r := mcpOnlyResource()

	var names []string
	for _, tool := range deriveMCPTools(r) {
		names = append(names, tool.Name)
	}
	want := []string{"widgets.list", "widgets.nudge"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("derived tools = %v, want %v", names, want)
	}

	mux := http.NewServeMux()
	RegisterREST(mux, r, func(*http.Request) (Caller, error) { return Caller{}, nil })
	// The mounted route answers; the MCP-only action has no path at all, so the
	// mux falls through to 404 rather than serving an empty pattern.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/widgets", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("declared REST endpoint was not mounted: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an MCP-only endpoint mounted a REST route: GET / = %d", rec.Code)
	}
}

func TestMCPOnly_AbsentFromOpenAPI(t *testing.T) {
	paths := resourcePaths(ptr(mcpOnlyResource()))
	if _, ok := paths["/v1/widgets"]; !ok {
		t.Fatalf("declared endpoint missing from the doc: %v", paths)
	}
	if len(paths) != 1 {
		t.Errorf("openapi paths = %v, want only /v1/widgets", paths)
	}
	for key, ops := range paths {
		if key == "" {
			t.Errorf("MCP-only endpoint emitted an empty path key: %v", ops)
		}
		blob, _ := json.Marshal(ops)
		if string(blob) == "" {
			t.Error("empty operation object")
		}
	}
}

func ptr[T any](v T) *T { return &v }
