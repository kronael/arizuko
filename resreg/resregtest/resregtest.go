// Package resregtest holds the per-daemon surface assertions every resreg-serving
// daemon runs against its own mux. Import it only from _test files.
//
// READ THIS BEFORE TRUSTING AssertServesWhatItAdvertises. It used to guard "a daemon's
// /openapi.json promises a path it mounts nowhere" (BUGS F21, F27, F32). F33 removed
// the CAUSE: the advertised set is now derived from the mux, so a daemon cannot name a
// resource it does not mount, and that assertion can no longer fail for the original
// reason. What it still checks is real but narrower — the path-rendering round trip and
// non-vacuity. The falsifiable proof lives in resreg's own tests, which can build a
// drifted mux; a daemon test cannot.
//
// AssertServesNoForeignResource did NOT weaken: nothing about deriving from the mux
// stops two daemons from mounting one resource name, and THREE daemons served GET
// /v1/sessions over three different tables until BUGS F46 renamed two of them.
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
	"strconv"
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
// from production. Only this package's own tests use it, to build the drifted
// muxes the assertions must fail on.
func byName(names []string) []*resreg.Resource {
	var out []*resreg.Resource
	for _, r := range resreg.All() {
		if slices.Contains(names, r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// foreignTo returns every registered resource mux does NOT own — owning a
// resource means RegisterREST put at least one of its faces on this mux, which
// resreg.MountedResources reads back by mount identity.
//
// Derived, never listed. Six daemons each carried a hand-written foreign list,
// and not one of them named `sessions` — which is precisely how routd, runed and
// authd all came to serve GET /v1/sessions over three different tables (BUGS
// F46). A list can omit the name that matters; a complement cannot.
func foreignTo(mux *http.ServeMux) []*resreg.Resource {
	owned := map[string]bool{}
	for _, r := range resreg.MountedResources(mux) {
		owned[r.Name] = true
	}
	var out []*resreg.Resource
	for _, r := range resreg.All() {
		if !owned[r.Name] {
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

// AssertServesNoForeignResource is the ownership half, and it needs no
// ownership table AND no foreign list: it renders the paths of EVERY registered
// resource this daemon does not own and pins that its mux serves none of them.
// This is the assertion F33 did NOT subsume — a daemon that MOUNTS a foreign
// resource now advertises it truthfully, so deriving from the mux makes the
// collision documented rather than caught.
//
// A hand-rolled mux.HandleFunc counts as serving: ownership is read off
// RegisterREST's mount identity, so squatting a foreign path without
// registering fails here rather than hiding.
//
// It doubles as the non-vacuity anchor: it t.Fatals if the foreign resources
// render no operation at all, so a guard cannot pass merely because the
// emitter produced nothing.
func AssertServesNoForeignResource(t TB, daemon string, mux *http.ServeMux) int {
	t.Helper()
	foreign := foreignTo(mux)
	ops := opsOf(t, daemon, foreign)
	if len(ops) == 0 {
		t.Fatalf("%s: every registered resource is owned by this mux — this anchor has nothing to compare against", daemon)
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

// AssertDocumentedResponse serves req on mux and checks the body against the
// schema /openapi.json advertises for that operation. It returns true when a
// schema was actually there to check, so a caller can pin non-vacuity.
//
// This is the guard nothing else provides. Paths are derived from the mux and
// request schemas from RowType, but a Handler returns `any`, so the only proof
// that the doc describes the BODY is to serve a real request and read it back.
// The operations named in BUGS L120 answered with a shape the doc had invented
// from the verb — []router.RouteView documented as RoutesRow, core.Task's Go
// field names documented as snake_case columns — and every emitter-level test
// passed throughout, because both sides read the same invented convention.
//
// The check is CONTAINMENT, not equality: an omitempty field absent from the
// body is legal, so a key the schema does not name is the failure. Sub-objects
// are not walked — the top level is where every recorded drift lived.
func AssertDocumentedResponse(t TB, daemon string, mux *http.ServeMux, req *http.Request) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	op := req.Method + " " + req.URL.Path
	if rec.Code >= 300 {
		t.Fatalf("%s: %s answered %d: %s — a doc check needs a real success body",
			daemon, op, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	doc := emitDoc(t, daemon, mux)
	// A mux pattern carries its method ("GET /v1/tasks/{taskId}"); a document
	// path key does not.
	key := strings.TrimPrefix(pattern(mux, req.Method, req.URL.Path), req.Method+" ")
	schema, ok := successSchema(doc, key, req.Method, rec.Code)
	if !ok {
		return false
	}
	var body any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %s answered non-JSON: %v", daemon, op, err)
	}
	checkAgainst(t, daemon, op, schema, body)
	return true
}

// emitDoc runs the production emitter over the daemon's own mux and returns the
// decoded document.
func emitDoc(t TB, daemon string, mux *http.ServeMux) map[string]any {
	t.Helper()
	raw, err := resreg.OpenAPI(daemon, "/", resreg.MountedResources(mux))
	if err != nil {
		t.Fatalf("%s: OpenAPI: %v", daemon, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: emitted doc is not JSON: %v", daemon, err)
	}
	return doc
}

// successSchema resolves the JSON schema the document gives for one operation's
// status, following a $ref into components.schemas. Missing at any step means
// the operation advertises no body — the honest answer for an action whose
// resource declares no response type.
func successSchema(doc map[string]any, pathKey, method string, status int) (map[string]any, bool) {
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[pathKey].(map[string]any)
	op, _ := item[strings.ToLower(method)].(map[string]any)
	responses, _ := op["responses"].(map[string]any)
	resp, _ := responses[strconv.Itoa(status)].(map[string]any)
	content, _ := resp["content"].(map[string]any)
	js, _ := content["application/json"].(map[string]any)
	schema, ok := js["schema"].(map[string]any)
	if !ok {
		return nil, false
	}
	schema = resolveRef(doc, schema)
	// A collection's element schema is a ref of its own, and an unresolved one
	// declares no properties — which would read as "the handler answers with
	// nothing the doc knows" for every list.
	if items, ok := schema["items"].(map[string]any); ok {
		return map[string]any{"type": schema["type"], "items": resolveRef(doc, items)}, true
	}
	return schema, true
}

// resolveRef swaps a $ref for the component it names, one level, which is all
// the emitter ever produces.
func resolveRef(doc map[string]any, schema map[string]any) map[string]any {
	ref, ok := schema["$ref"].(string)
	if !ok {
		return schema
	}
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	out, _ := schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
	if out == nil {
		return schema
	}
	return out
}

// checkAgainst compares one body against its schema: array-ness first, then
// every object's keys against the declared properties.
func checkAgainst(t TB, daemon, op string, schema map[string]any, body any) {
	t.Helper()
	items, isArray := schema["items"].(map[string]any)
	rows, bodyIsArray := body.([]any)
	if isArray != bodyIsArray {
		t.Errorf("%s: %s answers %s but the doc advertises %v",
			daemon, op, jsonKind(body), schema["type"])
		return
	}
	if !isArray {
		checkObject(t, daemon, op, schema, body)
		return
	}
	if len(rows) == 0 {
		t.Errorf("%s: %s answered an empty array — seed a row, or this proves nothing", daemon, op)
		return
	}
	for _, row := range rows {
		checkObject(t, daemon, op, items, row)
	}
}

// checkObject fails on a key the schema does not declare. Extra keys are the
// drift: the doc's reader decodes into the documented struct and silently loses
// every field the document never named.
func checkObject(t TB, daemon, op string, schema map[string]any, body any) {
	t.Helper()
	obj, ok := body.(map[string]any)
	if !ok {
		t.Errorf("%s: %s answers %s where the doc advertises an object", daemon, op, jsonKind(body))
		return
	}
	props, _ := schema["properties"].(map[string]any)
	var undocumented []string
	for k := range obj {
		if _, ok := props[k]; !ok {
			undocumented = append(undocumented, k)
		}
	}
	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Errorf("%s: %s answers with %s, which /openapi.json does not document — "+
			"declare the answering type in the resource's Responses (root CLAUDE.md "+
			"\"one declaration, many readers\")", daemon, op, strings.Join(undocumented, ", "))
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	case nil:
		return "null"
	}
	return "a scalar"
}
