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

func TestOpenAPI_Deterministic(t *testing.T) {
	registerOAPI(t)
	a, _ := OpenAPI("testd", "/", nil)
	b, _ := OpenAPI("testd", "/", nil)
	if string(a) != string(b) {
		t.Errorf("non-deterministic emit")
	}
}
