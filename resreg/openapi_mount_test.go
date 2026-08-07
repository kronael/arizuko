package resreg

// The falsifiable proof for BUGS F33. Each test REINTRODUCES one of the four
// drift shapes that shipped, then pins that MountedResources excludes it.
//
// Every test is anchored the same way and the anchor is the point: it first
// asserts the phantom operation IS present in the by-name rendering — what
// OpenAPIHandler used to be handed — and only then that it is absent from the
// mux-derived one. Without that half a test could "pass" because the emitter
// produced nothing at all, which is how twelve guards in this repo passed
// while checking nothing.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type driftRow struct {
	Key   string `db:"key"   json:"key"`
	Value string `db:"value" json:"value"`
}

const driftName = "f33_drift"

// driftResource declares one listable REST face. Handler stays nil: these
// tests read the mux, they never dispatch a request.
func driftResource() Resource {
	return Resource{
		Name:      driftName,
		Table:     driftName,
		RowType:   reflect.TypeFor[driftRow](),
		PKFields:  []string{"Key"},
		Endpoints: []Endpoint{{Verb: "GET", Path: "/v1/" + driftName, Action: ActionList}},
	}
}

// registerDrift resets the registry to exactly the drift resource, so the
// by-name anchor and the mux-derived set are comparing over one known thing.
func registerDrift(t *testing.T) {
	t.Helper()
	reset()
	Register(driftResource())
}

func nopCaller(*http.Request) (Caller, error) { return Caller{}, nil }

// ops renders rs and returns its operations as "VERB /path".
func ops(t *testing.T, rs []*Resource) []string {
	t.Helper()
	raw, err := OpenAPI("probe", "/", rs)
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("emitted doc is not JSON: %v", err)
	}
	var out []string
	for p, keys := range doc.Paths {
		for k := range keys {
			if k != "parameters" {
				out = append(out, strings.ToUpper(k)+" "+p)
			}
		}
	}
	slices.Sort(out)
	return out
}

// assertDrops is the shared shape: the operation must be advertised by the
// by-name rendering (the anchor — proving the operation is real and the
// emitter works) and absent from what mux derives. All() with the registry
// holding only the drift resource IS the old by-name selection, which is the
// mechanism F33 removed.
func assertDrops(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	op := "GET /v1/" + driftName
	if anchor := ops(t, All()); !slices.Contains(anchor, op) {
		t.Fatalf("anchor broken: the by-name rendering does not advertise %q (got %v) — "+
			"this test would pass on an emitter that produced nothing", op, anchor)
	}
	if got := ops(t, MountedResources(mux)); slices.Contains(got, op) {
		t.Errorf("mux-derived doc advertises %q, which this mux does not mount as %s (got %v)",
			op, driftName, got)
	}
}

// TestMountedResources_DropsUnmountedResource is the F27 shape: the resource is
// registered and renders an operation, and the daemon mounts nothing at all.
// `GET /v1/acl` shipped exactly this way — declared, advertised, mounted
// nowhere.
func TestMountedResources_DropsUnmountedResource(t *testing.T) {
	registerDrift(t)
	assertDrops(t, http.NewServeMux())
}

// TestMountedResources_DropsRepathedMount is the F21 shape and the one a
// path-probe is blind to: the daemon DOES mount the resource, at a path other
// than the one it declares. scheduled_tasks served /v1/tasks while its
// declaration said /v1/scheduled_tasks.
func TestMountedResources_DropsRepathedMount(t *testing.T) {
	registerDrift(t)
	r := driftResource()
	r.Endpoints = []Endpoint{{Verb: "GET", Path: "/elsewhere/" + driftName, Action: ActionList}}
	mux := http.NewServeMux()
	RegisterREST(mux, r, nopCaller)
	assertDrops(t, mux)
}

// TestMountedResources_DropsCatchAllMount is why derivation asks WHICH handler
// answers rather than WHETHER one does. proxyd mounts `mux.HandleFunc("/", …)`,
// so every probe resolves to a handler; a presence-only derivation would
// advertise every registered resource on a daemon that mounts none.
func TestMountedResources_DropsCatchAllMount(t *testing.T) {
	registerDrift(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	assertDrops(t, mux)
}

// TestMountedResources_DropsForeignHandRolledMount is the collision shape, and
// the reason mount identity beats path identity. THREE daemons serve
// GET /v1/sessions over three different tables — authd's refresh-token
// families (the resreg resource), runed's session_log, routd's
// core.SessionRecord. A hand-rolled mount sitting at a resource's path must
// not make that daemon publish the resource's schema.
func TestMountedResources_DropsForeignHandRolledMount(t *testing.T) {
	registerDrift(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/"+driftName, func(http.ResponseWriter, *http.Request) {})
	assertDrops(t, mux)
}

// TestMountedResources_KeepsRealMount is the control for all four. Without it
// they would be satisfied by a MountedResources that returned nothing ever.
func TestMountedResources_KeepsRealMount(t *testing.T) {
	registerDrift(t)
	mux := http.NewServeMux()
	RegisterREST(mux, driftResource(), nopCaller)
	got := ops(t, MountedResources(mux))
	if !slices.Contains(got, "GET /v1/"+driftName) {
		t.Fatalf("a genuinely mounted face is missing from the derived doc: %v", got)
	}
}

// TestMountedResources_DropsMCPOnlyEndpoint pins that an action with no REST
// face stays out. RegisterREST skips MCPOnly, so nothing is mounted to derive
// from — network_rules is entirely MCPOnly and drifted into a document once.
func TestMountedResources_DropsMCPOnlyEndpoint(t *testing.T) {
	reset()
	r := driftResource()
	r.Endpoints = append(r.Endpoints, Endpoint{Action: Action("custom"), MCPOnly: true})
	Register(r)
	mux := http.NewServeMux()
	RegisterREST(mux, r, nopCaller)
	if got := ops(t, MountedResources(mux)); len(got) != 1 {
		t.Errorf("derived doc = %v, want only the one REST face", got)
	}
}
