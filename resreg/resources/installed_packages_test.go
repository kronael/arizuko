package resources

// The catalog half of the spec 5/28 registration: the resource is registered, it
// reaches /openapi.json, and the doc advertises exactly the read-only surface the
// handler serves — no phantom write verb an operator would get a 404 from.

import (
	"encoding/json"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resregtest"
)

// routdOpenAPI renders the document for this one resource, mounted through the
// real RegisterREST. Whether ROUTD's doc carries it is routd's own assertion
// (resregtest.AssertAdvertises against routd's mux, tested there); here we check
// the catalog decl emits a correct and read-only surface at all.
func routdOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	b, err := resreg.OpenAPI("routd", "/", resregtest.Mounted("installed_packages"))
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("doc not JSON: %v", err)
	}
	return doc
}

func TestInstalledPackagesRegistered(t *testing.T) {
	r := resreg.Lookup("installed_packages")
	if r == nil {
		t.Fatal("installed_packages is not in the resreg registry")
	}
	if r.Table != "installed_packages" || r.DB != resreg.SubsystemRoutd {
		t.Fatalf("table=%q db=%q, want installed_packages/routd", r.Table, r.DB)
	}
	if r.RowType == nil {
		t.Fatal("no RowType — /openapi.json emits nothing for a schema-less resource")
	}
	// The name IS the wire identity: nothing else may claim it.
	for _, other := range resreg.All() {
		if other.Name == "installed_packages" && other != r {
			t.Fatal("two resources registered under installed_packages")
		}
	}
}

// Every declared action must carry an MCPDoc entry, or it silently gets no agent
// tool and no x-mcp-when annotation (5/17: strict, no fallback to summary).
func TestInstalledPackagesEveryActionHasAgentDoc(t *testing.T) {
	for _, e := range InstalledPackagesEndpoints {
		if InstalledPackagesMCPDoc[e.Action] == "" {
			t.Errorf("action %q has no MCPDoc entry — no MCP tool, no annotation", e.Action)
		}
		if InstalledPackagesMCPNames[e.Action] == "" {
			t.Errorf("action %q has no MCP tool name", e.Action)
		}
	}
}

func TestInstalledPackagesInOpenAPI(t *testing.T) {
	doc := routdOpenAPI(t)
	paths, _ := doc["paths"].(map[string]any)

	coll, ok := paths["/v1/installed_packages"].(map[string]any)
	if !ok {
		t.Fatal("/v1/installed_packages missing from routd's OpenAPI document")
	}
	list, ok := coll["get"].(map[string]any)
	if !ok {
		t.Fatal("/v1/installed_packages has no get operation")
	}
	if list["x-mcp-when"] == nil {
		t.Error("list operation carries no x-mcp-when annotation")
	}
	if list["operationId"] != "list_installed_packages" {
		t.Errorf("operationId = %v, want list_installed_packages", list["operationId"])
	}

	item, ok := paths["/v1/installed_packages/{name}"].(map[string]any)
	if !ok {
		t.Fatal("/v1/installed_packages/{name} missing from routd's OpenAPI document")
	}
	if item["get"] == nil {
		t.Error("the by-name path has no get operation")
	}

	// The schema the handler renders is the schema the doc publishes.
	schemas, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)
	schema, ok := schemas["InstalledPackages"].(map[string]any)
	if !ok {
		t.Fatal("InstalledPackages schema missing from components")
	}
	props, _ := schema["properties"].(map[string]any)
	for _, f := range []string{"name", "source", "revision", "installed_at", "manifest", "asset_hashes"} {
		if props[f] == nil {
			t.Errorf("schema property %q missing", f)
		}
	}
	// The raw JSON shadow columns are engine plumbing, never wire fields.
	for _, f := range []string{"ManifestRaw", "AssetHashesRaw", "manifest_raw"} {
		if props[f] != nil {
			t.Errorf("shadow column %q leaked into the published schema", f)
		}
	}
}

// The doc must not advertise a write verb: install/upgrade/remove is the CLI's
// side-effecting pipeline, and a documented POST would be a 404 for an operator
// and a second install path for anyone who then built it.
func TestInstalledPackagesOpenAPIIsReadOnly(t *testing.T) {
	doc := routdOpenAPI(t)
	paths, _ := doc["paths"].(map[string]any)
	for path, item := range paths {
		if path != "/v1/installed_packages" && path != "/v1/installed_packages/{name}" {
			continue
		}
		ops, _ := item.(map[string]any)
		for verb := range ops {
			switch verb {
			case "get", "parameters":
			default:
				t.Errorf("%s advertises %q — installed_packages is read-only", path, verb)
			}
		}
	}
}
