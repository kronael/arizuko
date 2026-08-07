// Package resregtest holds the per-daemon surface assertions every
// resreg-serving daemon runs against its own mux. Import it only from _test
// files.
//
// READ THIS BEFORE TRUSTING AssertServesWhatItAdvertises. It used to be the
// guard for "a daemon's /openapi.json promises a path it mounts nowhere" —
// BUGS F21 (scheduled_tasks overrode Endpoints at mount time), F27 (`GET
// /v1/acl` advertised, mounted nowhere), F32 (timed advertised four of routd's
// operations). F33 removed the CAUSE those guards watched for: the advertised
// set is now derived from the mux (resreg.MountedResources), so a daemon
// cannot name a resource it does not mount, and that assertion can no longer
// fail for the original reason. What it still checks is real but narrower —
// the path-rendering round trip (a mux `{path...}` documents as `{path}`) and
// non-vacuity. The falsifiable proof that derivation drops undocumentable
// mounts lives in resreg's own tests, which can build a drifted mux; a daemon
// test cannot.
//
// AssertServesNoneOf did NOT weaken: nothing about deriving from the mux stops
// two daemons from mounting one resource name, and that is a live hazard —
// THREE daemons serve GET /v1/sessions over three different tables.
//
// Why a shared package rather than one guard covering every daemon: only
// `routd` is an importable package. `onbod`, `proxyd`, `authd`, `webd`,
// `dashd`, `runed/cmd/runed` and `timed` are all `package main`, which Go
// cannot import, so no neutral test can reach their muxes. The assertions are
// therefore single-sourced HERE and each daemon's own in-package test calls
// them with its real mux — never a copy of it. A guard keeping its own list of
// the truth is the defect being fixed: `resreg/resources`' `daemonOwnership`
// map computed expectation and actual from the same table, so it could not
// fail, and it silently drifted four daemons out of date anyway.
package resregtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"

	"github.com/kronael/arizuko/resreg"
)

// TB is the slice of *testing.T these assertions use. Declared as an interface
// rather than taking testing.TB so this package's OWN tests can hand it a
// recorder and prove each assertion fails on the case it claims to catch —
// testing.TB has an unexported method and cannot be implemented outside
// package testing, which would leave these guards unfalsifiable.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// httpMethods are the OpenAPI path-item keys that are real operations. A path
// item puts a "parameters" array beside its verbs, and treating that as a
// method asks the mux for `PARAMETERS /v1/tasks/{taskId}`.
var httpMethods = []string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"}

// Op is one advertised (method, path) pair.
type Op struct{ Method, Path string }

// AdvertisedOps returns every operation the daemon's /openapi.json promises,
// sorted. Same input as production: the mux, and nothing else.
func AdvertisedOps(t TB, daemon string, mux *http.ServeMux) []Op {
	t.Helper()
	return opsOf(t, daemon, resreg.MountedResources(mux))
}

// byName picks registered resources by name. Test-only, and deliberately not
// exported by resreg: selecting resources by hand is exactly what F33 removed
// from production. It exists so AssertServesNoneOf can render the paths of
// resources the daemon under test must NOT serve — those have no mount to
// derive from, which is the point of the assertion.
func byName(names []string) []*resreg.Resource {
	var out []*resreg.Resource
	for _, r := range resreg.All() {
		if slices.Contains(names, r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// MuxOf builds a fresh mux carrying exactly the named registry resources' REST
// faces, through the real RegisterREST. The Caller builder is never invoked;
// only the mount identity is read.
func MuxOf(names ...string) *http.ServeMux {
	mux := http.NewServeMux()
	for _, r := range byName(names) {
		resreg.RegisterREST(mux, *r, func(*http.Request) (resreg.Caller, error) {
			return resreg.Caller{}, nil
		})
	}
	return mux
}

// Mounted is what resreg.MountedResources derives from MuxOf(names...). A test
// asking "what does the document say about resource X" gets its answer the way
// production does: by mounting X and reading the routing table.
func Mounted(names ...string) []*resreg.Resource {
	return resreg.MountedResources(MuxOf(names...))
}

// opsOf runs the OpenAPI emitter and returns every operation the document
// promises, sorted. It reads the EMITTER, not the Endpoints declarations, so a
// change in how paths are rendered cannot make a guard disagree with the
// document the daemon actually serves.
func opsOf(t TB, daemon string, rs []*resreg.Resource) []Op {
	t.Helper()
	raw, err := resreg.OpenAPI(daemon, "/", rs)
	if err != nil {
		t.Fatalf("%s: OpenAPI: %v", daemon, err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: emitted doc is not JSON: %v", daemon, err)
	}
	var ops []Op
	for p, keys := range doc.Paths {
		for k := range keys {
			if m := strings.ToUpper(k); slices.Contains(httpMethods, m) {
				ops = append(ops, Op{m, p})
			}
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops
}

// pattern returns the mux pattern that serves verb+path, or "" for the 404
// fallback. The pattern is rendered through the emitter's OWN path rule
// (resreg.OpenAPIPathKey) so both sides of the comparison speak one syntax:
// a mux `{path...}` documents as `{path}` because OpenAPI has no multi-segment
// template, and reimplementing that here would make the guard disagree with the
// document it guards. Every other difference — path, verb, param name, a
// missing mount, a catch-all — survives the rendering and still fails.
func pattern(mux *http.ServeMux, verb, path string) string {
	_, p := mux.Handler(httptest.NewRequest(verb, resreg.ConcretePath(path), nil))
	return resreg.OpenAPIPathKey(p)
}

// AssertAdvertises pins a daemon's WHOLE advertised surface against want,
// as "VERB /path" strings. It replaces the old AssertServesWhatItAdvertises,
// which compared the document to the mux — a comparison F33 made tautological
// by deriving the document FROM the mux. A guard that cannot fail is worse
// than no guard, so this one pins the derived set against a list written down
// by hand instead: mounting or unmounting any REST face fails it until someone
// states the new surface, which is how a change stays visible in review.
//
// The hand-written list is a test EXPECTATION, never a production input. The
// hand-written list feeding OpenAPIHandler was the bug (F33); a hand-written
// list a test compares the truth against is the opposite — it is the only
// thing here that can be wrong and say so.
func AssertAdvertises(t TB, daemon string, mux *http.ServeMux, want []string) {
	t.Helper()
	var got []string
	for _, op := range AdvertisedOps(t, daemon, mux) {
		got = append(got, op.Method+" "+op.Path)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s advertises:\n  %s\nwant:\n  %s\n(a REST face was mounted or unmounted; state the new surface here)",
			daemon, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// AssertServesNoneOf is the ownership half, and it needs no ownership table:
// it renders the paths of resources this daemon does NOT own and pins that its
// mux serves none of them. This is the assertion F33 did NOT subsume — a
// daemon that MOUNTS a foreign resource now advertises it truthfully, so
// deriving from the mux makes the collision documented rather than caught.
//
// It doubles as the non-vacuity anchor: it t.Fatals if the foreign resources
// render no operation at all, so a guard cannot pass merely because the
// emitter produced nothing.
func AssertServesNoneOf(t TB, daemon string, foreign []string, mux *http.ServeMux) int {
	t.Helper()
	ops := opsOf(t, daemon, byName(foreign))
	if len(ops) == 0 {
		t.Fatalf("%s: resources %v render no operation — this anchor has nothing to compare against", daemon, foreign)
	}
	for _, op := range ops {
		want := op.Method + " " + op.Path
		if got := pattern(mux, op.Method, op.Path); got == want {
			t.Errorf("%s's mux serves %q, a path belonging to a resource it does not own — "+
				"two daemons mounting one resource name is a wire-identity collision (root CLAUDE.md)", daemon, want)
		}
	}
	return len(ops)
}
