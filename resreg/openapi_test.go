package resreg

// OpenAPI emitter tests — synthetic schema, no arizuko resource imports.
// Validates: doc parses; servers[0].url honoured; paths cover the four
// CRUD ops; components/schemas reflects struct fields with json/yaml
// tags; composite PKs collapse to one URL parameter.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type oapiTestRow struct {
	Seq    int    `db:"seq"    json:"seq"`
	Match  string `db:"match"  json:"match"`
	Target string `db:"target" json:"target,omitempty"`
}

func registerOAPI(t *testing.T) {
	t.Helper()
	reset()
	Register(Resource{
		Name:     "oapi_rows",
		Table:    "oapi_rows",
		RowType:  reflect.TypeOf(oapiTestRow{}),
		PKFields: []string{"Seq", "Match", "Target"},
	})
}

func TestOpenAPI_BasicShape(t *testing.T) {
	registerOAPI(t)
	out, err := OpenAPI("testd", "http://localhost:9999/", nil)
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if v, _ := doc["openapi"].(string); v != "3.1.0" {
		t.Errorf("openapi version = %q, want 3.1.0", v)
	}
	servers := doc["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("servers len = %d, want 1", len(servers))
	}
	if servers[0].(map[string]any)["url"] != "http://localhost:9999/" {
		t.Errorf("servers[0].url = %v", servers[0])
	}
	info := doc["info"].(map[string]any)
	if got := info["title"]; got != "arizuko testd API" {
		t.Errorf("info.title = %q", got)
	}
}

func TestOpenAPI_PathsCRUD(t *testing.T) {
	registerOAPI(t)
	out, _ := OpenAPI("testd", "/", nil)
	var doc map[string]any
	json.Unmarshal(out, &doc)
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/oapi_rows"]; !ok {
		t.Fatalf("missing /v1/oapi_rows: %v", paths)
	}
	if _, ok := paths["/v1/oapi_rows/{seq}"]; !ok {
		t.Fatalf("missing /v1/oapi_rows/{seq}: %v", paths)
	}
	collection := paths["/v1/oapi_rows"].(map[string]any)
	for _, m := range []string{"get", "post"} {
		if _, ok := collection[m]; !ok {
			t.Errorf("collection missing %s", m)
		}
	}
	item := paths["/v1/oapi_rows/{seq}"].(map[string]any)
	for _, m := range []string{"patch", "delete"} {
		if _, ok := item[m]; !ok {
			t.Errorf("item missing %s", m)
		}
	}
	// Composite PK should be flagged in the parameter description.
	params := item["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("params len = %d, want 1", len(params))
	}
	desc, _ := params[0].(map[string]any)["description"].(string)
	if want := "Composite primary key:"; !strings.Contains(desc, want) {
		t.Errorf("description missing composite hint: %q", desc)
	}
}

func TestOpenAPI_SchemaReflection(t *testing.T) {
	registerOAPI(t)
	out, _ := OpenAPI("testd", "/", nil)
	var doc map[string]any
	json.Unmarshal(out, &doc)
	comp := doc["components"].(map[string]any)
	schemas := comp["schemas"].(map[string]any)
	row, ok := schemas["OapiRows"].(map[string]any)
	if !ok {
		t.Fatalf("schemas missing OapiRows: %v", schemas)
	}
	if row["type"] != "object" {
		t.Errorf("type = %v, want object", row["type"])
	}
	props := row["properties"].(map[string]any)
	if props["seq"].(map[string]any)["type"] != "integer" {
		t.Errorf("seq type = %v, want integer", props["seq"])
	}
	if props["match"].(map[string]any)["type"] != "string" {
		t.Errorf("match type = %v, want string", props["match"])
	}
	// Required: only seq + match (target is omitempty).
	req := row["required"].([]any)
	if len(req) != 2 {
		t.Errorf("required = %v, want [match seq]", req)
	}
}

func TestOpenAPI_StandardErrors(t *testing.T) {
	registerOAPI(t)
	out, _ := OpenAPI("testd", "/", nil)
	var doc map[string]any
	json.Unmarshal(out, &doc)
	comp := doc["components"].(map[string]any)
	resp := comp["responses"].(map[string]any)
	for _, code := range []string{"400", "401", "403", "404", "409", "500"} {
		if _, ok := resp[code]; !ok {
			t.Errorf("missing standard response %s", code)
		}
	}
	// Each operation refs at least one standard error response.
	paths := doc["paths"].(map[string]any)
	col := paths["/v1/oapi_rows"].(map[string]any)
	getOp := col["get"].(map[string]any)
	getResp := getOp["responses"].(map[string]any)
	if _, ok := getResp["400"]; !ok {
		t.Errorf("get responses missing 400 ref")
	}
}

func TestOpenAPI_ResourceFilter(t *testing.T) {
	reset()
	Register(Resource{
		Name:     "first",
		Table:    "first",
		RowType:  reflect.TypeOf(oapiTestRow{}),
		PKFields: []string{"Seq"},
	})
	Register(Resource{
		Name:     "second",
		Table:    "second",
		RowType:  reflect.TypeOf(oapiTestRow{}),
		PKFields: []string{"Seq"},
	})
	out, _ := OpenAPI("testd", "/", []string{"second"})
	var doc map[string]any
	json.Unmarshal(out, &doc)
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/first"]; ok {
		t.Errorf("filter leaked: /v1/first present")
	}
	if _, ok := paths["/v1/second"]; !ok {
		t.Errorf("filter excluded /v1/second")
	}
}

func TestOpenAPI_MCPDoc(t *testing.T) {
	reset()
	Register(Resource{
		Name:     "oapi_rows",
		Table:    "oapi_rows",
		RowType:  reflect.TypeOf(oapiTestRow{}),
		PKFields: []string{"Seq"},
		MCPDoc: map[Action]string{
			ActionCreate: "Create a row. When to use: seeding.",
			ActionList:   "List rows.",
		},
	})
	out, _ := OpenAPI("testd", "/", nil)
	var doc map[string]any
	json.Unmarshal(out, &doc)
	paths := doc["paths"].(map[string]any)
	col := paths["/v1/oapi_rows"].(map[string]any)
	post := col["post"].(map[string]any)
	if post["description"] != "Create a row. When to use: seeding." {
		t.Errorf("post description = %v", post["description"])
	}
	if post["x-mcp-when"] != "Create a row. When to use: seeding." {
		t.Errorf("post x-mcp-when = %v", post["x-mcp-when"])
	}
	get := col["get"].(map[string]any)
	if get["description"] != "List rows." {
		t.Errorf("get description = %v", get["description"])
	}
	// Actions without an MCPDoc entry stay undocumented (delete has none).
	item := paths["/v1/oapi_rows/{seq}"].(map[string]any)
	del := item["delete"].(map[string]any)
	if _, ok := del["description"]; ok {
		t.Errorf("delete gained a description without an MCPDoc entry")
	}
}

func TestOpenAPI_Deterministic(t *testing.T) {
	registerOAPI(t)
	a, _ := OpenAPI("testd", "/", nil)
	b, _ := OpenAPI("testd", "/", nil)
	if string(a) != string(b) {
		t.Errorf("non-deterministic emit")
	}
}

// TestOpenAPI_EndpointsDriven: a resource that declares Endpoints (custom verbs,
// a non-PK {id} delete path) emits exactly those faces — no PK-convention
// phantom /{seq} path, no PATCH the resource never mounts — and withMCPDoc still
// rides the annotated operation.
func TestOpenAPI_EndpointsDriven(t *testing.T) {
	reset()
	Register(Resource{
		Name:     "ep_rows",
		Table:    "ep_rows",
		RowType:  reflect.TypeOf(oapiTestRow{}),
		PKFields: []string{"Seq", "Match", "Target"},
		Endpoints: []Endpoint{
			{Verb: "POST", Path: "/v1/ep_rows", Action: Action("add")},
			{Verb: "PUT", Path: "/v1/ep_rows", Action: Action("set")},
			{Verb: "DELETE", Path: "/v1/ep_rows/{id}", Action: ActionDelete},
			{Verb: "GET", Path: "/v1/ep_rows", Action: ActionList},
		},
		MCPDoc: map[Action]string{
			Action("set"): "Rewrite the table. When to use: full reconfiguration.",
		},
	})
	out, _ := OpenAPI("testd", "/", nil)
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	paths := doc["paths"].(map[string]any)

	col, ok := paths["/v1/ep_rows"].(map[string]any)
	if !ok {
		t.Fatalf("missing /v1/ep_rows: %v", paths)
	}
	for _, m := range []string{"get", "post", "put"} {
		if _, ok := col[m]; !ok {
			t.Errorf("collection missing %s", m)
		}
	}
	if _, ok := col["patch"]; ok {
		t.Errorf("collection gained a phantom patch")
	}
	item, ok := paths["/v1/ep_rows/{id}"].(map[string]any)
	if !ok {
		t.Fatalf("missing /v1/ep_rows/{id}: %v", paths)
	}
	if _, ok := item["delete"]; !ok {
		t.Errorf("item missing delete")
	}
	// The PK-convention phantom path must NOT exist.
	if _, ok := paths["/v1/ep_rows/{seq}"]; ok {
		t.Errorf("phantom PK path /v1/ep_rows/{seq} present")
	}
	// The {id} path parameter is described.
	params, _ := item["parameters"].([]any)
	if len(params) != 1 || params[0].(map[string]any)["name"] != "id" {
		t.Errorf("item parameters = %v, want one {id}", params)
	}
	// x-mcp-when rides the annotated op only.
	put := col["put"].(map[string]any)
	if put["x-mcp-when"] != "Rewrite the table. When to use: full reconfiguration." {
		t.Errorf("put x-mcp-when = %v", put["x-mcp-when"])
	}
	if _, ok := col["post"].(map[string]any)["x-mcp-when"]; ok {
		t.Errorf("post gained x-mcp-when without an MCPDoc entry")
	}
}

// TestOpenAPI_ConventionFallback: a resource with NO Endpoints still emits the
// full 5-op PK-CRUD convention (list/create on the collection; get/update/delete
// on the {pk} item) — the fallback engine-managed tables rely on.
func TestOpenAPI_ConventionFallback(t *testing.T) {
	registerOAPI(t) // oapi_rows declares no Endpoints
	out, _ := OpenAPI("testd", "/", nil)
	var doc map[string]any
	json.Unmarshal(out, &doc)
	paths := doc["paths"].(map[string]any)
	col := paths["/v1/oapi_rows"].(map[string]any)
	for _, m := range []string{"get", "post"} {
		if _, ok := col[m]; !ok {
			t.Errorf("fallback collection missing %s", m)
		}
	}
	item, ok := paths["/v1/oapi_rows/{seq}"].(map[string]any)
	if !ok {
		t.Fatalf("fallback missing item path /v1/oapi_rows/{seq}: %v", paths)
	}
	for _, m := range []string{"get", "patch", "delete"} {
		if _, ok := item[m]; !ok {
			t.Errorf("fallback item missing %s", m)
		}
	}
}
