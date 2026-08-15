package resreg

// OpenAPI emitter tests — synthetic schema, no arizuko resource imports.
// Validates: doc parses; servers[0].url honoured; paths cover the four
// CRUD ops; components/schemas reflects struct fields with json/yaml
// tags; composite PKs collapse to one URL parameter.

import (
	"encoding/json"
	"net/http"
	"reflect"
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
		Name:      "oapi_rows",
		Table:     "oapi_rows",
		RowType:   reflect.TypeFor[oapiTestRow](),
		PKFields:  []string{"Seq", "Match", "Target"},
		Endpoints: oapiCRUDEndpoints,
	})
}

// oapiCRUDEndpoints is the plain PK-CRUD face, spelled out. It used to be
// implied — a resource declaring no Endpoints got these five operations from a
// convention inside the emitter, which meant the doc could describe a path
// nothing served. There is one renderer now, so a resource says what it serves.
var oapiCRUDEndpoints = []Endpoint{
	{Verb: "GET", Path: "/v1/oapi_rows", Action: ActionList},
	{Verb: "POST", Path: "/v1/oapi_rows", Action: ActionCreate, Status: http.StatusCreated},
	{Verb: "GET", Path: "/v1/oapi_rows/{seq}", Action: ActionGet},
	{Verb: "PATCH", Path: "/v1/oapi_rows/{seq}", Action: ActionUpdate},
	{Verb: "DELETE", Path: "/v1/oapi_rows/{seq}", Action: ActionDelete, Status: http.StatusNoContent},
}

func TestOpenAPI_BasicShape(t *testing.T) {
	registerOAPI(t)
	out, err := OpenAPI("testd", "http://localhost:9999/", All())
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
	out, _ := OpenAPI("testd", "/", All())
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
	// The item path's parameter is named after the placeholder in the DECLARED
	// path, not after the PK. Those diverge — this resource's PK is the triple
	// (seq, match, target) — and the path is what a caller sends.
	params := item["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("params len = %d, want 1", len(params))
	}
	if name, _ := params[0].(map[string]any)["name"].(string); name != "seq" {
		t.Errorf("param name = %q, want seq (the placeholder in the declared path)", name)
	}
}

func TestOpenAPI_SchemaReflection(t *testing.T) {
	registerOAPI(t)
	out, _ := OpenAPI("testd", "/", All())
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
	out, _ := OpenAPI("testd", "/", All())
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

// TestOpenAPI_ResourceFilter: two resources are registered, ONE is mounted,
// and only the mounted one is documented. Selection used to be by name, which
// is what let a daemon document a sibling daemon's resource (BUGS F33); it is
// now the mux, so `first` cannot appear on a daemon that never mounts it.
func TestOpenAPI_ResourceFilter(t *testing.T) {
	reset()
	one := func(name string) Resource {
		return Resource{
			Name:      name,
			Table:     name,
			RowType:   reflect.TypeFor[oapiTestRow](),
			PKFields:  []string{"Seq"},
			Endpoints: []Endpoint{{Verb: "GET", Path: "/v1/" + name, Action: ActionList}},
		}
	}
	Register(one("first"))
	Register(one("second"))

	mux := http.NewServeMux()
	RegisterREST(mux, one("second"), nopCaller)

	out, _ := OpenAPI("testd", "/", MountedResources(mux))
	var doc map[string]any
	json.Unmarshal(out, &doc)
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/first"]; ok {
		t.Errorf("filter leaked: /v1/first present though nothing mounts it")
	}
	if _, ok := paths["/v1/second"]; !ok {
		t.Errorf("filter excluded the mounted /v1/second")
	}
}

func TestOpenAPI_MCPDoc(t *testing.T) {
	reset()
	Register(Resource{
		Name:      "oapi_rows",
		Table:     "oapi_rows",
		RowType:   reflect.TypeFor[oapiTestRow](),
		PKFields:  []string{"Seq"},
		Endpoints: oapiCRUDEndpoints,
		MCPDoc: map[Action]string{
			ActionCreate: "Create a row. When to use: seeding.",
			ActionList:   "List rows.",
		},
	})
	out, _ := OpenAPI("testd", "/", All())
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
	a, _ := OpenAPI("testd", "/", All())
	b, _ := OpenAPI("testd", "/", All())
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
		RowType:  reflect.TypeFor[oapiTestRow](),
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
	out, _ := OpenAPI("testd", "/", All())
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

// TestOpenAPI_NoEndpointsNoPaths pins the deletion of the PK-CRUD convention
// fallback. A resource that declares no Endpoints once had five operations
// invented for it from its Name and RowType — paths nothing had to mount, which
// is the drift shape the whole "advertised set comes from the mux" design
// removes everywhere else. Now such a resource contributes NOTHING, and
// resources.TestEveryResourceDeclaresEndpoints is what stops one from being
// registered that way in the first place.
func TestOpenAPI_NoEndpointsNoPaths(t *testing.T) {
	reset()
	Register(Resource{
		Name:     "oapi_rows",
		Table:    "oapi_rows",
		RowType:  reflect.TypeFor[oapiTestRow](),
		PKFields: []string{"Seq"},
	})
	out, _ := OpenAPI("testd", "/", All())
	var doc map[string]any
	json.Unmarshal(out, &doc)
	paths := doc["paths"].(map[string]any)
	if len(paths) != 0 {
		t.Errorf("an Endpoints-less resource was documented at %v — the emitter invented a path", paths)
	}
	// The schema still lands: it is reflected from RowType, which is real.
	comp := doc["components"].(map[string]any)
	if _, ok := comp["schemas"].(map[string]any)["OapiRows"]; !ok {
		t.Error("RowType schema went missing — only the invented PATHS were meant to go")
	}
}
