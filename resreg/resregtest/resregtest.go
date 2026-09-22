// Package resregtest holds the per-daemon surface assertions every
// resreg-serving daemon runs against its own mux. Import it only from _test
// files.
//
// The advertised set is derived from the mux (BUGS F33), so a daemon cannot
// name a resource it does not mount. AssertServesNoForeignResource is the
// guard that outlives that: nothing stops two daemons mounting one resource
// name, and THREE daemons served GET /v1/sessions over three tables (BUGS
// F46). A shared package because every daemon but routd is `package main`,
// which no neutral test can import; each daemon's own test calls these with
// its real mux. A guard keeping its own list of the truth cannot fail.
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

// TB is the slice of *testing.T these assertions use. Not testing.TB, which
// has an unexported method and cannot be implemented outside package testing:
// this package's OWN tests hand it a recorder to prove each assertion fails on
// the case it claims to catch.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// httpMethods are the OpenAPI path-item keys that are real operations; a path
// item also carries a "parameters" array beside its verbs.
var httpMethods = []string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"}

// Op is one advertised (method, path) pair.
type Op struct{ Method, Path string }

// AdvertisedOps returns every operation the daemon's /openapi.json promises,
// sorted. Same input as production: the mux, and nothing else.
func AdvertisedOps(t TB, daemon string, mux *http.ServeMux) []Op {
	t.Helper()
	return opsOf(t, daemon, resreg.MountedResources(mux))
}

// byName picks registered resources by name, to build the drifted muxes the
// assertions must fail on. Selecting resources by hand is exactly what F33
// removed from production, so resreg does not export this.
func byName(names []string) []*resreg.Resource {
	var out []*resreg.Resource
	for _, r := range resreg.All() {
		if slices.Contains(names, r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// foreignTo returns every registered resource mux does NOT own, read back by
// mount identity. Derived, never listed: a hand-written foreign list can omit
// the name that matters (BUGS F46); a complement cannot.
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
// faces, through the real RegisterREST.
func MuxOf(names ...string) *http.ServeMux {
	mux := http.NewServeMux()
	for _, r := range byName(names) {
		resreg.RegisterREST(mux, *r, func(*http.Request) (resreg.Caller, error) {
			return resreg.Caller{}, nil
		})
	}
	return mux
}

// Mounted is what resreg.MountedResources derives from MuxOf(names...).
func Mounted(names ...string) []*resreg.Resource {
	return resreg.MountedResources(MuxOf(names...))
}

// opsOf runs the OpenAPI emitter and returns every operation the document
// promises, sorted. It reads the EMITTER, not the Endpoints declarations, so
// the guard cannot disagree with the document the daemon serves.
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
// fallback, rendered through the emitter's OWN path rule
// (resreg.OpenAPIPathKey) so both sides of the comparison speak one syntax.
func pattern(mux *http.ServeMux, verb, path string) string {
	_, p := mux.Handler(httptest.NewRequest(verb, resreg.ConcretePath(path), nil))
	return resreg.OpenAPIPathKey(p)
}

// AssertAdvertises pins a daemon's WHOLE advertised surface against want, as
// "VERB /path" strings. Comparing the document to the mux is tautological
// since the document is derived FROM the mux (F33), so this pins the derived
// set against a list written by hand: mounting or unmounting any REST face
// fails it until someone states the new surface. The list is a test
// EXPECTATION, never a production input.
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

// AssertServesNoForeignResource renders the paths of EVERY registered resource
// this daemon does not own and pins that its mux serves none of them. This is
// the assertion F33 did NOT subsume: a daemon that MOUNTS a foreign resource
// advertises it truthfully. A hand-rolled mux.HandleFunc counts as serving.
// It t.Fatals if the foreign resources render no operation at all, so it
// cannot pass vacuously.
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
// schema /openapi.json advertises for that operation; it returns true when a
// schema was there to check. A Handler returns `any`, so the only proof that
// the doc describes the BODY is a real request (BUGS L120). The check is
// CONTAINMENT: an omitempty field absent from the body is legal, a key the
// schema does not name is the failure. Sub-objects are not walked.
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

// successSchema resolves the JSON schema the document gives for one
// operation's status, following a $ref. Missing at any step means the
// operation advertises no body.
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
	// A collection's element schema is a ref of its own; unresolved, every
	// list would read as undocumented.
	if items, ok := schema["items"].(map[string]any); ok {
		return map[string]any{"type": schema["type"], "items": resolveRef(doc, items)}, true
	}
	return schema, true
}

// resolveRef swaps a $ref for the component it names, one level.
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

// checkObject fails on a key the schema does not declare: the doc's reader
// decodes into the documented struct and silently loses every other field.
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
