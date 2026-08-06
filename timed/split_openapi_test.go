package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
)

// pathPlaceholder matches a stdlib-mux wildcard segment, e.g. {taskId}.
var pathPlaceholder = regexp.MustCompile(`\{[^}]+\}`)

// httpMethods are the OpenAPI path-item keys that are actually operations.
var httpMethods = map[string]bool{
	"GET": true, "PUT": true, "POST": true, "DELETE": true,
	"OPTIONS": true, "HEAD": true, "PATCH": true, "TRACE": true,
}

// concretePath substitutes every {placeholder} with a literal so the path can be
// looked up on a real mux.
func concretePath(p string) string { return pathPlaceholder.ReplaceAllString(p, "x") }

// emittedOps runs the OpenAPI emitter and returns every (VERB, path) pair the
// document advertises, sorted. It reads the EMITTER, not the declarations, so a
// change in how paths are rendered cannot make this guard disagree with the
// document timed actually serves.
func emittedOps(t *testing.T, resources []string) [][2]string {
	t.Helper()
	raw, err := resreg.OpenAPI("timed", "/", resources)
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("emitted doc is not JSON: %v", err)
	}
	var ops [][2]string
	for p, keys := range doc.Paths {
		for k := range keys {
			// A path item's keys are not all verbs — OpenAPI puts a path-level
			// "parameters" array beside them, and treating it as a method asked the
			// mux for "PARAMETERS /v1/tasks/{taskId}". Filter to real methods.
			v := strings.ToUpper(k)
			if !httpMethods[v] {
				continue
			}
			ops = append(ops, [2]string{v, p})
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i][1] != ops[j][1] {
			return ops[i][1] < ops[j][1]
		}
		return ops[i][0] < ops[j][0]
	})
	return ops
}

// serves reports whether timed's real mux resolves verb+path to a handler other
// than the 404 fallback. A pattern of "" is the fallback.
func serves(mux *http.ServeMux, verb, path string) bool {
	_, pattern := mux.Handler(httptest.NewRequest(verb, concretePath(path), nil))
	return pattern != ""
}

// TestTimedOpenAPI_AdvertisesOnlyWhatItMounts is the F32 guard, and it is the
// F21/F27 shape applied to the daemon those guards structurally cannot see:
// both of those live in package routd and probe the mux routd builds.
//
// It derives BOTH sides — the advertised set from the emitter, the served set
// from the mux newTimedMux actually returns. Neither is a hand-maintained copy;
// the copy shape (resreg/resources' daemonOwnership) computes expectation and
// actual from the same table and so cannot fail.
//
// Two halves, because the first alone would be VACUOUS while
// timedOpenAPIResources is empty — zero advertised paths means zero assertions.
// The second half is the anchor: it proves the emitter has teeth by rendering
// the resource F32 wrongly named, then pins that timed serves none of it.
func TestTimedOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	mux := newTimedMux(&dashServer{})

	// Half 1 — the class guard. Everything timed advertises must resolve on the
	// mux timed builds. Fires the moment a resource is added to the list without
	// a matching mount.
	for _, op := range emittedOps(t, timedOpenAPIResources) {
		if !serves(mux, op[0], op[1]) {
			t.Errorf("timed advertises %s %s but its mux serves no such route — "+
				"a generated client aimed at timed would 404 (BUGS F32 class)",
				op[0], op[1])
		}
	}

	// Half 2 — the anchor, and the F32 regression itself. scheduled_tasks is
	// routd's; timed only CALLS it. Emitting it here is what advertised four
	// phantom operations.
	foreign := emittedOps(t, []string{"scheduled_tasks"})
	if len(foreign) == 0 {
		t.Fatal("emitter produced no operations for scheduled_tasks — " +
			"this guard has nothing to compare against and half 1 is vacuous")
	}
	live := map[[2]string]bool{}
	for _, op := range emittedOps(t, timedOpenAPIResources) {
		live[op] = true
	}
	for _, op := range foreign {
		if serves(mux, op[0], op[1]) {
			t.Errorf("timed's mux unexpectedly serves %s %s — "+
				"this guard assumed routd was the only host of scheduled_tasks", op[0], op[1])
		}
		if live[op] {
			t.Errorf("timed advertises %s %s, which it does not serve (BUGS F32)", op[0], op[1])
		}
	}
}

// TestTimedMux_ServesItsThreeRoutes pins what timed DOES serve, so half 1 above
// cannot be satisfied by a mux that serves nothing at all.
func TestTimedMux_ServesItsThreeRoutes(t *testing.T) {
	mux := newTimedMux(&dashServer{})
	for _, want := range [][2]string{
		{"GET", "/health"},
		{"GET", "/openapi.json"},
		{"GET", "/dash/timed/"},
	} {
		if !serves(mux, want[0], want[1]) {
			t.Errorf("timed's mux does not serve %s %s", want[0], want[1])
		}
	}
}
