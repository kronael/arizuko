package routd

// Single-source guard for the spec 5/16 + 5/17 fold: each folded routd resource
// mounts the canonical resources.<X>Endpoints — the SAME slice the resreg
// registry emits into /openapi.json — so the mounted REST faces, the derived
// MCP tools, and the published doc can never drift.
//
// TestResourceEndpoints_SingleSource reads each CONSTRUCTOR, which is blind to a
// post-construction `res.Endpoints = ...` in the mount function: that is exactly
// how BUGS F21 stayed green while scheduled_tasks served /v1/tasks against a
// declaration saying /v1/scheduled_tasks. TestMountTasks_ServesCanonicalEndpoints
// closes it for scheduled_tasks by probing the mux mountTasks actually builds.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

func TestResourceEndpoints_SingleSource(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, fakeVerifier{}, 0, "")

	if !reflect.DeepEqual(srv.routesResource(nil).Endpoints, resources.RoutesEndpoints) {
		t.Error("routes: mounted Endpoints != resources.RoutesEndpoints")
	}
	if !reflect.DeepEqual(srv.webRoutesResource().Endpoints, resources.WebRoutesEndpoints) {
		t.Error("web_routes: mounted Endpoints != resources.WebRoutesEndpoints")
	}
	if !reflect.DeepEqual(srv.scheduledTasksResource(nil, false).Endpoints, resources.ScheduledTasksEndpoints) {
		t.Error("scheduled_tasks: mounted Endpoints != resources.ScheduledTasksEndpoints")
	}
	if !reflect.DeepEqual(srv.aclResource().Endpoints, resources.ACLEndpoints) {
		t.Error("acl: mounted Endpoints != resources.ACLEndpoints")
	}
	if !reflect.DeepEqual(srv.networkRulesResource().Endpoints, resources.NetworkRulesEndpoints) {
		t.Error("network_rules: mounted Endpoints != resources.NetworkRulesEndpoints")
	}
	if !reflect.DeepEqual(srv.routeTokensResource().Endpoints, resources.RouteTokensEndpoints) {
		t.Error("route_tokens: mounted Endpoints != resources.RouteTokensEndpoints")
	}
	if !reflect.DeepEqual(srv.groupsResource(nil).Endpoints, resources.GroupsAgentEndpoints) {
		t.Error("groups: mounted Endpoints != resources.GroupsAgentEndpoints")
	}
}

// pathPlaceholder matches a stdlib-mux wildcard segment, e.g. {taskId}.
var pathPlaceholder = regexp.MustCompile(`\{[^}]+\}`)

// concretePath substitutes every {placeholder} with a literal so the path can be
// looked up on a real mux. "x" cannot collide with a sibling literal route
// (/v1/tasks/due, /v1/tasks/runs) — and mountTasks registers none of those.
func concretePath(p string) string { return pathPlaceholder.ReplaceAllString(p, "x") }

// TestMountTasks_ServesCanonicalEndpoints asserts the ROUTING TABLE mountTasks
// builds, not the resource it starts from. Every non-MCPOnly canonical endpoint
// must resolve to a pattern byte-equal to its own declaration; an inline
// Endpoints literal in mountTasks makes the canonical paths resolve to the
// 404 handler (pattern "") and fails here.
func TestMountTasks_ServesCanonicalEndpoints(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, fakeVerifier{}, 0, "")

	mux := http.NewServeMux()
	srv.mountTasks(mux)

	rest := 0
	for _, e := range resources.ScheduledTasksEndpoints {
		if e.MCPOnly {
			// An MCPOnly endpoint has no REST face at all; a Verb/Path here
			// would be a route silently never mounted.
			if e.Verb != "" || e.Path != "" {
				t.Errorf("scheduled_tasks: MCPOnly action %q declares %q %q — MCPOnly endpoints carry no REST face",
					e.Action, e.Verb, e.Path)
			}
			continue
		}
		rest++
		want := e.Verb + " " + e.Path
		h, pattern := mux.Handler(httptest.NewRequest(e.Verb, concretePath(e.Path), nil))
		if h == nil {
			t.Errorf("%s: mux returned no handler", want)
			continue
		}
		if pattern != want {
			t.Errorf("%s: mux serves pattern %q, want %q — mountTasks is not mounting resources.ScheduledTasksEndpoints",
				want, pattern, want)
		}
	}
	if rest == 0 {
		t.Fatal("resources.ScheduledTasksEndpoints declares no REST endpoint — this guard would pass vacuously")
	}
}

// TestOpenAPI_ScheduledTasksAdvertised proves spec 5/17's acceptance for
// scheduled_tasks against the real emitted document: every served endpoint is in
// the doc, no unserved path is, and x-mcp-when marks exactly the actions with an
// MCPDoc entry. Before F21 the resource was absent from OpenAPIResources
// entirely, so /v1/tasks* worked but no generated client could find it.
func TestOpenAPI_ScheduledTasksAdvertised(t *testing.T) {
	if !slices.Contains(OpenAPIResources, "scheduled_tasks") {
		t.Fatalf("OpenAPIResources = %v, want scheduled_tasks advertised", OpenAPIResources)
	}
	raw, err := resreg.OpenAPI("routd", "/", OpenAPIResources)
	if err != nil {
		t.Fatal(err)
	}
	// A path item mixes method keys with a "parameters" array, so decode the
	// operations lazily and only for the verb under test.
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("emitted doc has no paths at all")
	}

	checked := 0
	for _, e := range resources.ScheduledTasksEndpoints {
		if e.MCPOnly {
			continue
		}
		ops, ok := doc.Paths[e.Path]
		if !ok {
			t.Errorf("openapi.json has no path %q (have %v)", e.Path, keysOf(doc.Paths))
			continue
		}
		method := strings.ToLower(e.Verb)
		rawOp, ok := ops[method]
		if !ok {
			t.Errorf("openapi.json %q has no %s operation", e.Path, method)
			continue
		}
		var op struct {
			OperationID string `json:"operationId"`
			MCPWhen     string `json:"x-mcp-when"`
		}
		if err := json.Unmarshal(rawOp, &op); err != nil {
			t.Errorf("%s %s: decode operation: %v", e.Verb, e.Path, err)
			continue
		}
		checked++
		// openapi.go composes operationId as <action>_<name>. Spec 5/17
		// §"Resource name = wire identity" says <name>.<action>; the emitted
		// form is the one every generated client already holds, so pin the
		// emitted form here and track the wording drift in BUGS.
		if want := string(e.Action) + "_scheduled_tasks"; op.OperationID != want {
			t.Errorf("%s %s: operationId = %q, want %q", e.Verb, e.Path, op.OperationID, want)
		}
		// x-mcp-when is present iff the action carries agent-facing prose —
		// spec 5/17's "no MCPDoc entry → no tool and no annotation".
		_, hasDoc := resources.ScheduledTasksMCPDoc[e.Action]
		if hasDoc && op.MCPWhen == "" {
			t.Errorf("%s %s: action %q has an MCPDoc entry but no x-mcp-when", e.Verb, e.Path, e.Action)
		}
		if !hasDoc && op.MCPWhen != "" {
			t.Errorf("%s %s: action %q has no MCPDoc entry but emits x-mcp-when %q", e.Verb, e.Path, e.Action, op.MCPWhen)
		}
	}
	if checked == 0 {
		t.Fatal("no scheduled_tasks operation checked — this guard would pass vacuously")
	}
	// The old declaration's paths are served by nothing; advertising them would
	// be the phantom-404 the OpenAPIResources comment forbids.
	for p := range doc.Paths {
		if strings.HasPrefix(p, "/v1/scheduled_tasks") {
			t.Errorf("openapi.json advertises %q, which routd mounts nowhere", p)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
