package routd

// Single-source guard for the spec 5/16 + 5/17 fold: each folded routd resource
// mounts the canonical resources.<X>Endpoints — the SAME slice the resreg
// registry emits into /openapi.json — so the mounted REST faces, the derived
// MCP tools, and the published doc can never drift.
//
// Reading the CONSTRUCTOR is not enough: a post-construction
// `res.Endpoints = ...` in a mount function is invisible to it. That is how BUGS
// F21 stayed green while scheduled_tasks served /v1/tasks against a declaration
// saying /v1/scheduled_tasks, and how F27's two survivors (mountACL trimming
// list away while /openapi.json still advertised GET /v1/acl, mountGroups
// trimming register) stayed green after it. So the guard here probes the mux
// routd ACTUALLY builds, for EVERY resource routd constructs — not just tasks.

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

// routdResource pairs one constructed resource with the canonical endpoint slice
// it must carry.
type routdResource struct {
	name string
	got  []resreg.Endpoint
	want []resreg.Endpoint
}

// routdResources enumerates EVERY resreg.Resource routd constructs. It is the
// guard's only hand-maintained input; TestRoutdResources_CoverAdvertised pins
// that it covers everything routd advertises, so an advertised resource cannot
// slip past the mux probe below.
func routdResources(s *Server) []routdResource {
	return []routdResource{
		{"routes", s.routesResource(nil).Endpoints, resources.RoutesEndpoints},
		{"web_routes", s.webRoutesResource().Endpoints, resources.WebRoutesEndpoints},
		{"scheduled_tasks", s.scheduledTasksResource(nil, false).Endpoints, resources.ScheduledTasksEndpoints},
		{"acl", s.aclResource().Endpoints, resources.ACLEndpoints},
		{"acl_membership", s.membershipResource(nil).Endpoints, resources.ACLMembershipEndpoints},
		{"network_rules", s.networkRulesResource().Endpoints, resources.NetworkRulesEndpoints},
		{"route_tokens", s.routeTokensResource().Endpoints, resources.RouteTokensEndpoints},
		{"groups", s.groupsResource(nil).Endpoints, resources.GroupsAgentEndpoints},
		{"secrets", s.secretsResource().Endpoints, resources.SecretsEndpoints},
		{"installed_packages", s.installedPackagesResource().Endpoints, resources.InstalledPackagesEndpoints},
		{"audit", s.auditResource().Endpoints, resources.AuditEndpoints},
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewServer(db, nil, &recDeliverer{}, fakeVerifier{}, 0, "")
}

func TestResourceEndpoints_SingleSource(t *testing.T) {
	for _, r := range routdResources(testServer(t)) {
		if !reflect.DeepEqual(r.got, r.want) {
			t.Errorf("%s: constructed Endpoints != the canonical resources slice\n got %v\nwant %v",
				r.name, r.got, r.want)
		}
	}
}

// TestRoutdResources_CoverAdvertised keeps routdResources honest: every resource
// routd's /openapi.json advertises must be probed by the mux guard below.
// Without this, dropping a row from routdResources would silently exempt a
// resource whose paths the doc still promises.
func TestRoutdResources_CoverAdvertised(t *testing.T) {
	covered := make([]string, 0, len(routdResources(testServer(t))))
	for _, r := range routdResources(testServer(t)) {
		covered = append(covered, r.name)
	}
	for _, name := range OpenAPIResources {
		if !slices.Contains(covered, name) {
			t.Errorf("OpenAPIResources advertises %q but routdResources does not cover it — "+
				"its mounted endpoints are unguarded", name)
		}
	}
}

// pathPlaceholder matches a stdlib-mux wildcard segment, e.g. {taskId}.
var pathPlaceholder = regexp.MustCompile(`\{[^}]+\}`)

// concretePath substitutes every {placeholder} with a literal so the path can be
// looked up on a real mux. "x" cannot collide with a sibling literal route
// (/v1/tasks/due, /v1/route_tokens/resolve, …) that routd registers by hand.
func concretePath(p string) string { return pathPlaceholder.ReplaceAllString(p, "x") }

// TestRoutdMux_ServesEveryDeclaredEndpoint is the class-wide F21/F27 guard: it
// asserts the ROUTING TABLE routd actually builds, not the resources it starts
// from. For every routd resource, every non-MCPOnly canonical endpoint must
// resolve on routd's real mux to a pattern byte-equal to its own declaration —
// so a mount function that reassigns res.Endpoints (dropping, adding, or
// re-pathing a face) makes the canonical pattern resolve to the 404 handler
// (pattern "") and fails here, whichever resource it happens in.
//
// The mirror half is MCPOnly: an MCPOnly endpoint declares "no REST face at
// all", so a Verb/Path on one is a route silently never mounted — the shape
// network_rules carried before F27.
func TestRoutdMux_ServesEveryDeclaredEndpoint(t *testing.T) {
	srv := testServer(t)
	mux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Server.Handler() is %T, not *http.ServeMux — this guard cannot read its patterns", srv.Handler())
	}

	rest := 0
	for _, r := range routdResources(srv) {
		for _, e := range r.want {
			if e.MCPOnly {
				if e.Verb != "" || e.Path != "" {
					t.Errorf("%s: MCPOnly action %q declares %q %q — MCPOnly endpoints carry no REST face",
						r.name, e.Action, e.Verb, e.Path)
				}
				continue
			}
			rest++
			want := e.Verb + " " + e.Path
			h, pattern := mux.Handler(httptest.NewRequest(e.Verb, concretePath(e.Path), nil))
			if h == nil {
				t.Errorf("%s: %s: mux returned no handler", r.name, want)
				continue
			}
			if pattern != want {
				t.Errorf("%s: %s: routd's mux serves pattern %q, want %q — its mount function is not mounting the canonical Endpoints slice",
					r.name, want, pattern, want)
			}
		}
	}
	if rest == 0 {
		t.Fatal("no REST endpoint declared by any routd resource — this guard would pass vacuously")
	}
}

// TestOpenAPI_EveryAdvertisedPathIsMounted closes the same class from the doc
// side, and needs no hand-maintained list at all: every (method, path) routd's
// /openapi.json promises must resolve on routd's real mux. `acl` failed this
// before F27 — ACLEndpoints declared GET /v1/acl, mountACL trimmed it away, and
// the doc shipped an endpoint that 404s.
func TestOpenAPI_EveryAdvertisedPathIsMounted(t *testing.T) {
	srv := testServer(t)
	mux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Server.Handler() is %T, not *http.ServeMux", srv.Handler())
	}
	raw, err := resreg.OpenAPI("routd", "/", OpenAPIResources)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	checked := 0
	for path, item := range doc.Paths {
		for key := range item {
			// A path item mixes verb keys with a "parameters" array.
			method := strings.ToUpper(key)
			if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, method) {
				continue
			}
			checked++
			want := method + " " + path
			_, pattern := mux.Handler(httptest.NewRequest(method, concretePath(path), nil))
			if pattern != want {
				t.Errorf("/openapi.json advertises %q but routd's mux serves pattern %q — an advertised endpoint that 404s",
					want, pattern)
			}
		}
	}
	if checked == 0 {
		t.Fatal("emitted doc advertises no operation at all — this guard would pass vacuously")
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
		// operationId is <action>_<name> — spec 5/17 §"Resource name = wire
		// identity" ruled for the emitted form (BUGS F28): OpenAPI RECOMMENDS an
		// operationId follow "common programming naming conventions" because
		// generators turn it into a method name, and every major generator
		// sanitizes a dot out. Pinned so the convention cannot drift.
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
