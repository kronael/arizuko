// Package resregtest holds the one doc-vs-mux guard every resreg-serving
// daemon runs against its own surface. Import it only from _test files.
//
// The class it closes is "a daemon's /openapi.json promises a path the daemon
// mounts nowhere" — BUGS F21 (scheduled_tasks overrode Endpoints at mount
// time), F27 (`GET /v1/acl` advertised, mounted nowhere), F32 (timed
// advertised four of routd's operations). Each was invisible until something
// derived BOTH sides: the advertised set from the emitter, the served set from
// the routing table the daemon actually builds.
//
// Why a shared package rather than one guard covering every daemon: only
// `routd` is an importable package. `onbod`, `proxyd`, `authd`, `webd`,
// `dashd`, `runed/cmd/runed` and `timed` are all `package main`, which Go
// cannot import, so no neutral test can reach their muxes. The assertion is
// therefore single-sourced HERE and each daemon's own in-package test calls it
// with its real mux and its real resource list — never a copy of either. A
// guard keeping its own list of the truth is the defect being fixed:
// `resreg/resources`' `daemonOwnership` map computed expectation and actual
// from the same table, so it could not fail, and it silently drifted four
// daemons out of date anyway.
package resregtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
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

// pathPlaceholder matches a stdlib-mux wildcard segment, e.g. {taskId}.
var pathPlaceholder = regexp.MustCompile(`\{[^}]+\}`)

// concretePath substitutes every {placeholder} with a literal so the path can
// be looked up on a real mux. "x" cannot collide with a sibling literal route
// (/v1/tasks/due, /v1/routing/resolve, …) that a daemon registers by hand.
func concretePath(p string) string { return pathPlaceholder.ReplaceAllString(p, "x") }

// Op is one advertised (method, path) pair.
type Op struct{ Method, Path string }

// AdvertisedOps runs the OpenAPI emitter for the given daemon + resources and
// returns every operation the document promises, sorted. It reads the EMITTER,
// not the Endpoints declarations, so a change in how paths are rendered cannot
// make a guard disagree with the document the daemon actually serves.
func AdvertisedOps(t TB, daemon string, resources []string) []Op {
	t.Helper()
	raw, err := resreg.OpenAPI(daemon, "/", resources)
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
	_, p := mux.Handler(httptest.NewRequest(verb, concretePath(path), nil))
	return resreg.OpenAPIPathKey(p)
}

// AssertServesWhatItAdvertises is the class guard: every operation the
// daemon's /openapi.json promises must resolve on the mux the daemon actually
// builds, to a pattern byte-equal to the advertised one. Byte-equality rather
// than "some handler answered" is what makes it survive a catch-all route — a
// daemon with `mux.HandleFunc("/", …)` answers every probe, and only the
// pattern distinguishes the real mount from the fallback.
//
// Returns the number of operations checked. A daemon advertising a non-empty
// resource list should assert that count is non-zero; a daemon that owns
// nothing (timed, webd, dashd) legitimately checks zero and anchors
// non-vacuity with AssertServesNoneOf instead.
func AssertServesWhatItAdvertises(t TB, daemon string, resources []string, mux *http.ServeMux) int {
	t.Helper()
	ops := AdvertisedOps(t, daemon, resources)
	for _, op := range ops {
		want := op.Method + " " + op.Path
		if got := pattern(mux, op.Method, op.Path); got != want {
			t.Errorf("%s advertises %q but its mux serves pattern %q — an advertised endpoint that 404s "+
				"(a generated client aimed at %s would fail; BUGS F21/F27/F32 class)",
				daemon, want, got, daemon)
		}
	}
	return len(ops)
}

// AssertServesNoneOf is the ownership half, and it needs no ownership table:
// it renders the paths of resources this daemon does NOT own and pins that its
// mux serves none of them. That subsumes what a hand-maintained per-daemon
// ownership map claimed to check — a daemon advertising a foreign resource
// fails AssertServesWhatItAdvertises because it mounts none of those paths, and
// a daemon MOUNTING a foreign resource fails here.
//
// It doubles as the non-vacuity anchor: it t.Fatals if the foreign resources
// render no operation at all, so a guard cannot pass merely because the
// emitter produced nothing.
func AssertServesNoneOf(t TB, daemon string, foreign []string, mux *http.ServeMux) int {
	t.Helper()
	ops := AdvertisedOps(t, daemon, foreign)
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
