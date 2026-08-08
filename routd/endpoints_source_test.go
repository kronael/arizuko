package routd

// Single-source guard for the spec 5/16 + 5/17 fold: each folded routd resource
// mounts the endpoint set the resreg registry publishes under that resource's
// own Name — the SAME slice the registry emits into /openapi.json — so the
// mounted REST faces, the derived MCP tools, and the published doc can never
// drift.
//
// The expectation is resolved BY the mounted Name, never by a hand-written
// pairing. A pairing table checks a resource against the slice the table says it
// should have, so a resource whose Name drifts off its wire identity keeps
// comparing clean — the shape proxyd shipped on 2026-07-01, its live resource
// reading Name: "routes" while its catalog and OpenAPI said proxyd_routes.
//
// Reading the CONSTRUCTOR is not enough either: a post-construction
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
	"github.com/kronael/arizuko/resreg/resregtest"
)

// routdResources enumerates EVERY resreg.Resource routd constructs — the
// guard's only hand-maintained input, and it names no endpoint slice: the
// expectation is looked up in the REGISTRY by each resource's own Name, so
// there is no second place to keep a resource's canonical shape.
// TestRoutdResources_CoverAdvertised pins that this list covers everything
// routd advertises, so an advertised resource cannot slip past the mux probe.
func routdResources(s *Server) []resreg.Resource {
	return []resreg.Resource{
		s.routesResource(nil),
		s.webRoutesResource(),
		s.scheduledTasksResource(nil, false),
		s.aclResource(),
		s.membershipResource(nil),
		s.networkRulesResource(),
		s.routeTokensResource(),
		s.groupsResource(nil),
		s.secretsResource(),
		s.installedPackagesResource(),
		s.auditResource(),
		s.pendingActionsResource(),
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

// TestResourceEndpoints_SingleSource is spec 5/16's "shared-identity test":
// every resource routd MOUNTS carries a Name that is a registered wire identity,
// and the endpoint set the registry publishes under that name. Resolving the
// expectation through mounted.Name rather than a hand-written pairing is what
// makes it catch the 2026-07-01 shape, where proxyd's live resource drifted to
// Name: "routes" while its catalog and OpenAPI still said proxyd_routes — a
// pairing table would have compared the drifted resource against the slice the
// table said it should have, and agreed.
func TestResourceEndpoints_SingleSource(t *testing.T) {
	for _, got := range routdResources(testServer(t)) {
		reg := resreg.Lookup(got.Name)
		if reg == nil {
			t.Errorf("routd mounts a resource named %q that the registry does not know — "+
				"its name is its wire identity (/v1/%s and the MCP tool prefix), so an "+
				"unregistered one is served but absent from every catalog", got.Name, got.Name)
			continue
		}
		if !reflect.DeepEqual(got.Endpoints, reg.Endpoints) {
			t.Errorf("%s: mounted Endpoints != the registry's for that name\n got %v\nwant %v",
				got.Name, got.Endpoints, reg.Endpoints)
		}
	}
}

// routdDoc emits the /openapi.json routd actually serves: the document derived
// from the mux Handler builds, byte-identical to what the daemon returns. Tests
// asserting "the doc advertises X" must read THIS, never a resource list they
// assemble themselves — a doc built from a hand-picked list proves only that
// the emitter works on that list (BUGS F33).
func routdDoc(t *testing.T) []byte {
	t.Helper()
	srv := testServer(t)
	mux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Server.Handler() is %T, not *http.ServeMux", srv.Handler())
	}
	raw, err := resreg.OpenAPI("routd", "/", resreg.MountedResources(mux))
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	return raw
}

// TestRoutdResources_CoverAdvertised keeps routdResources honest: every resource
// routd's /openapi.json advertises must be probed by the mux guard below.
// Without this, dropping a row from routdResources would silently exempt a
// resource whose paths the doc still promises.
func TestRoutdResources_CoverAdvertised(t *testing.T) {
	covered := make([]string, 0, len(routdResources(testServer(t))))
	for _, r := range routdResources(testServer(t)) {
		covered = append(covered, r.Name)
	}
	srv := testServer(t)
	mux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Server.Handler() is %T, not *http.ServeMux", srv.Handler())
	}
	advertised := resreg.MountedResources(mux)
	if len(advertised) == 0 {
		t.Fatal("routd's mux advertises no resource — this guard has nothing to check")
	}
	for _, r := range advertised {
		if !slices.Contains(covered, r.Name) {
			t.Errorf("routd's /openapi.json advertises %q but routdResources does not cover it — "+
				"its mounted endpoints are unguarded", r.Name)
		}
	}
}

// pathPlaceholder matches a stdlib-mux wildcard segment, e.g. {taskId}.
var pathPlaceholder = regexp.MustCompile(`\{[^}]+\}`)

// concretePath substitutes every {placeholder} with a literal so the path can be
// looked up on a real mux. "x" cannot collide with a sibling literal route
// (/v1/tasks/due, /v1/routing/resolve, …) that routd registers by hand.
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
		for _, e := range r.Endpoints {
			if e.MCPOnly {
				if e.Verb != "" || e.Path != "" {
					t.Errorf("%s: MCPOnly action %q declares %q %q — MCPOnly endpoints carry no REST face",
						r.Name, e.Action, e.Verb, e.Path)
				}
				continue
			}
			rest++
			want := e.Verb + " " + e.Path
			h, pattern := mux.Handler(httptest.NewRequest(e.Verb, concretePath(e.Path), nil))
			if h == nil {
				t.Errorf("%s: %s: mux returned no handler", r.Name, want)
				continue
			}
			if pattern != want {
				t.Errorf("%s: %s: routd's mux serves pattern %q, want %q — its mount function is not mounting the canonical Endpoints slice",
					r.Name, want, pattern, want)
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
//
// The assertion itself lives in resreg/resregtest so every daemon runs the same
// one. It cannot live in a single cross-daemon test: routd is the only
// importable daemon package, so each daemon calls it from its own package with
// its own mux and its own list — both production values, never a copy.
func TestOpenAPI_EveryAdvertisedPathIsMounted(t *testing.T) {
	srv := testServer(t)
	mux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Server.Handler() is %T, not *http.ServeMux", srv.Handler())
	}
	// routd hand-rolls GET /v1/sessions (core.SessionRecord) and a dozen other
	// /v1/* reads with plain mux.HandleFunc. None may appear: only a
	// RegisterREST mount is a documented resource face (BUGS F33).
	resregtest.AssertAdvertises(t, "routd", mux, []string{
		"DELETE /v1/acl",
		"DELETE /v1/acl_membership",
		"DELETE /v1/route_tokens/{jid}",
		"DELETE /v1/routes/{id}",
		"DELETE /v1/secrets/{key}",
		"DELETE /v1/tasks/{taskId}",
		"DELETE /v1/web_routes",
		"GET /v1/audit",
		"GET /v1/groups",
		"GET /v1/installed_packages",
		"GET /v1/installed_packages/{name}",
		"GET /v1/pending_actions",
		"GET /v1/route_tokens",
		"GET /v1/routes",
		"GET /v1/tasks",
		"GET /v1/tasks/{taskId}",
		"GET /v1/web_routes",
		"PATCH /v1/tasks/{taskId}",
		"POST /v1/acl",
		"POST /v1/pending_actions/{id}/approve",
		"POST /v1/pending_actions/{id}/reject",
		"POST /v1/route_tokens/chat",
		"POST /v1/route_tokens/hook",
		"POST /v1/routes",
		"POST /v1/secrets",
		"PUT /v1/routes",
		"PUT /v1/web_routes",
	})
}

// TestRoutdMux_ServesNoForeignResource is the ownership half: routd must not
// mount a resource it does not advertise owning. onbod's onboarding tables and
// proxyd's reverse-proxy routes are the live foreign pair — `routes` and
// `proxyd_routes` are two different tables whose names once converged, so a
// second daemon serving either is the wire-identity collision root CLAUDE.md
// forbids.
func TestRoutdMux_ServesNoForeignResource(t *testing.T) {
	srv := testServer(t)
	mux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Server.Handler() is %T, not *http.ServeMux", srv.Handler())
	}
	resregtest.AssertServesNoneOf(t, "routd", []string{"proxyd_routes", "onboarding", "invites"}, mux)
}

// TestRouteTokens_NoHandRolledResolve pins the F13 deletion from both sides:
// the resource declares no resolve endpoint AND routd's mux serves none. Until
// 5/W was corrected, routd hand-mounted `POST /v1/route_tokens/resolve` beside
// the declared faces — a path with zero production callers, absent from
// /openapi.json, kept alive only by a contract test. Token DELIVERY is not a
// REST call: webd and proxyd are FS-mounted on routd.db and resolve in-process.
//
// The sibling GET assertion is the non-vacuity control: it proves the mux is
// live and this probe method can see a mounted route, so the resolve half
// cannot pass merely because nothing resolves.
func TestRouteTokens_NoHandRolledResolve(t *testing.T) {
	for _, e := range resources.RouteTokensEndpoints {
		if strings.HasSuffix(e.Path, "/resolve") {
			t.Fatalf("RouteTokensEndpoints declares %s %s — token delivery has no REST face (spec 5/W)", e.Verb, e.Path)
		}
	}

	srv := testServer(t)
	mux, ok := srv.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Server.Handler() is %T, not *http.ServeMux", srv.Handler())
	}

	if _, pattern := mux.Handler(
		httptest.NewRequest("GET", "/v1/route_tokens", nil),
	); pattern != "GET /v1/route_tokens" {
		t.Fatalf("control: mux serves %q for GET /v1/route_tokens, want %q — probe cannot see mounted routes",
			pattern, "GET /v1/route_tokens")
	}

	if _, pattern := mux.Handler(
		httptest.NewRequest("POST", "/v1/route_tokens/resolve", nil),
	); pattern != "" {
		t.Errorf("routd's mux serves %q — a hand-rolled route_tokens path outside the resource declaration, so /openapi.json cannot advertise it (F13)",
			pattern)
	}
}

// TestOpenAPI_ScheduledTasksAdvertised proves spec 5/17's acceptance for
// scheduled_tasks against the real emitted document: every served endpoint is in
// the doc, no unserved path is, and x-mcp-when marks exactly the actions with an
// MCPDoc entry. Before F21 the resource was absent from OpenAPIResources
// entirely, so /v1/tasks* worked but no generated client could find it.
func TestOpenAPI_ScheduledTasksAdvertised(t *testing.T) {
	raw := routdDoc(t)
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
