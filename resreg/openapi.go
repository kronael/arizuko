package resreg

// OpenAPI emission — spec 5/8 §"OpenAPI emission".
//
// The same `RowType` reflection that drives YAML/JSON/SQL also yields
// an OpenAPI 3.1 schema document for free. One walk over the registry,
// one JSON blob, served from every daemon's `/openapi.json` so the
// platform's HTTP surface is discoverable without hand-written specs.
//
// Subsumes spec 5/4 (`openapi-discoverable`): no `huma`, no `swag`, no
// codegen — `encoding/json` + `reflect` + the existing per-resource
// catalog. Schemas can't drift — both handler and doc read the same
// struct. Paths can't drift either, and for a stronger reason: the
// advertised set is READ OFF THE MUX (MountedResources), so a daemon
// documents exactly the faces RegisterREST put on its routing table and
// has no way to name anything else. Before BUGS F33 each daemon handed
// OpenAPIHandler a list of resource names with no reference to its mux,
// which is the cause under every drift found: F21 (scheduled_tasks),
// F27 (acl, groups), F32 (timed), network_rules, and the inverse case
// of route_tokens/resolve mounted but never advertised.

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// OpenAPI emits an OpenAPI 3.1 JSON document for rs.
//
//   - daemon       title + identifier in `info.title`
//   - baseURL      single entry in `servers[]` (use "/" for relative)
//   - rs           the resources to document, each already trimmed to the
//     endpoints it serves. Production callers get this from
//     MountedResources(mux) via OpenAPIHandler and never
//     assemble it by hand — that hand-assembly WAS BUGS F33.
//
// The document is deterministic: components/schemas + paths keys are
// emitted via the same map-key sort `encoding/json` applies. Resources
// without `RowType` (forwarders, MCP-only) contribute nothing.
//
// One operation per Endpoint — its real mounted verb+path.
//
// Standard error responses (400, 401, 403, 404, 409, 500) are referenced
// from `components.responses` so per-path bloat stays small.
func OpenAPI(daemon, baseURL string, rs []*Resource) ([]byte, error) {
	if baseURL == "" {
		baseURL = "/"
	}

	schemas := map[string]any{}
	paths := map[string]any{}
	for _, r := range rs {
		if r.RowType == nil {
			continue
		}
		schema, err := rowSchema(r.RowType)
		if err != nil {
			return nil, fmt.Errorf("%s: schema: %w", r.Name, err)
		}
		schemas[schemaName(r.Name)] = schema
		for path, ops := range resourcePaths(r) {
			paths[path] = ops
		}
	}
	schemas["Error"] = errorSchema()

	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       fmt.Sprintf("arizuko %s API", daemon),
			"description": fmt.Sprintf("Engine-generated OpenAPI for the %s daemon. Spec: arizuko/specs/5/8-yaml-manifests.md.", daemon),
			"version":     "v1",
		},
		"servers": []any{
			map[string]any{"url": baseURL},
		},
		"paths": paths,
		"components": map[string]any{
			"schemas":   schemas,
			"responses": stdResponses(),
		},
	}
	return marshalDeterministic(doc)
}

// MountedResources returns the registry resources mux ACTUALLY serves, each
// copied down to just the endpoints RegisterREST mounted on it. This is the
// advertised set: a daemon can no longer name what it documents, so it can no
// longer name something it does not serve.
//
// Derivation is by mount IDENTITY, not by path. Asking only "does something
// answer here?" would be a new lie in place of the old one — proxyd's `/`
// catch-all answers every probe, and routd and runed both hand-roll a
// GET /v1/sessions over tables that are not authd's sessions resource. Both
// drop out here because neither is a *restMount carrying this resource+action.
//
// Registration order is preserved so output stays stable across runs.
func MountedResources(mux *http.ServeMux) []*Resource {
	if mux == nil {
		return nil
	}
	var out []*Resource
	for _, r := range All() {
		var served []Endpoint
		for _, e := range r.Endpoints {
			if e.MCPOnly {
				continue
			}
			if serves(mux, r.Name, e) {
				served = append(served, e)
			}
		}
		if len(served) == 0 {
			continue
		}
		c := *r
		c.Endpoints = served
		out = append(out, &c)
	}
	return out
}

// serves reports whether mux routes e to the mount RegisterREST made for
// resource. Every field is compared because each one is a drift shape seen in
// the wild: a different Path is a re-path, a different Verb or Action is a
// look-alike face, and a non-*restMount is a hand-rolled or catch-all mount.
func serves(mux *http.ServeMux, resource string, e Endpoint) bool {
	h, _ := mux.Handler(&http.Request{
		Method: e.Verb,
		Host:   "openapi.invalid",
		URL:    &url.URL{Path: ConcretePath(e.Path)},
	})
	m, ok := h.(*restMount)
	return ok && m.resource == resource && m.endpoint.Verb == e.Verb &&
		m.endpoint.Path == e.Path && m.endpoint.Action == e.Action
}

// pathPlaceholder matches a stdlib-mux wildcard segment, e.g. {taskId} or
// {path...}.
var pathPlaceholder = regexp.MustCompile(`\{[^}]+\}`)

// ConcretePath substitutes every {placeholder} in a mux pattern with a literal
// so the pattern can be looked up as a request path. "x" cannot collide with a
// sibling literal route (/v1/tasks/due, /v1/routing/resolve, …) that a daemon
// registers by hand.
func ConcretePath(p string) string { return pathPlaceholder.ReplaceAllString(p, "x") }

// rowSchema reflects a RowType into an OpenAPI 3.1 schema object.
// `json:` tags drive property names; Go kinds → JSON Schema types via
// kindToSchema. Unsupported kinds fall back to a generic object.
func rowSchema(rt reflect.Type) (map[string]any, error) {
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("RowType must be struct, got %s", rt.Kind())
	}
	props := map[string]any{}
	var required []string
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		name, omit := parseJSONTag(sf)
		if name == "" {
			continue
		}
		props[name] = kindToSchema(sf.Type)
		if !omit {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

// parseJSONTag returns (name, omitempty) from a struct field's `json:`
// tag, falling back to `yaml:` if json is absent, then the field name.
// Empty string + omit=false means "skip this field" (e.g. `json:"-"`).
func parseJSONTag(sf reflect.StructField) (string, bool) {
	tag := sf.Tag.Get("json")
	if tag == "" {
		tag = sf.Tag.Get("yaml")
	}
	if tag == "" {
		return sf.Name, false
	}
	if tag == "-" {
		return "", false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = sf.Name
	}
	omit := false
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omit = true
		}
	}
	return name, omit
}

// kindToSchema maps a Go reflect.Type to a JSON Schema type fragment.
// Slices/arrays render as `{type:"array", items:…}`; maps render as
// `{type:"object", additionalProperties:true}`; pointers unwrap.
func kindToSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": kindToSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": true}
	case reflect.Struct:
		return map[string]any{"type": "object"}
	default:
		return map[string]any{}
	}
}

// resourcePaths builds the OpenAPI operations for one resource. A resource
// that declares Endpoints (its real mounted REST faces) emits exactly those;
// one with none falls back to the spec 5/8 PK-CRUD convention. Returns a
// map[path]ops; ops is keyed by HTTP method (lowercased per OpenAPI).
func resourcePaths(r *Resource) map[string]map[string]any {
	if len(r.Endpoints) > 0 {
		return endpointPaths(r)
	}
	return conventionPaths(r)
}

// endpointPaths renders one operation per declared Endpoint — the doc mirrors
// what RegisterREST mounts, so the two cannot drift. The path key + parameters
// come from the stdlib-mux Path (placeholders {id}/{path...} → OpenAPI {param});
// request/response shape is picked from the verb + action so the doc matches
// what the handler actually accepts and returns. withMCPDoc adds the agent-
// facing x-mcp-when per operation when the resource carries an MCPDoc entry.
func endpointPaths(r *Resource) map[string]map[string]any {
	schemaRef := map[string]any{"$ref": "#/components/schemas/" + schemaName(r.Name)}
	out := map[string]map[string]any{}
	for _, e := range r.Endpoints {
		// MCP-only actions have no REST face to document; RegisterREST skips
		// them too, so doc and mount stay in step.
		if e.MCPOnly {
			continue
		}
		pathKey, params := openAPIPath(e.Path)
		item := out[pathKey]
		if item == nil {
			item = map[string]any{}
			if len(params) > 0 {
				item["parameters"] = params
			}
			out[pathKey] = item
		}
		item[strings.ToLower(e.Verb)] = endpointOp(r, e, schemaRef, len(params) > 0)
	}
	return out
}

// endpointOp builds one operation for an Endpoint. Read actions (list/get) get
// their read shapes; mutations carry a request body (a body-addressed DELETE —
// one with no path placeholder — also reads the target from the JSON body); the
// success status is Endpoint.Status when set, else 200 (matching restHandler).
func endpointOp(r *Resource, e Endpoint, schemaRef map[string]any, hasPathParam bool) map[string]any {
	status := e.Status
	if status == 0 {
		status = http.StatusOK
	}
	code := strconv.Itoa(status)
	op := map[string]any{
		"summary":     fmt.Sprintf("%s %s", titleAction(e.Action), r.Name),
		"operationId": fmt.Sprintf("%s_%s", e.Action, r.Name),
	}
	var success map[string]any
	switch {
	case e.Action == ActionList:
		success = map[string]any{code: listResponse(schemaRef)}
	case e.Verb == http.MethodGet:
		success = map[string]any{code: itemResponse(schemaRef)}
	case e.Verb == http.MethodDelete:
		if !hasPathParam { // body-addressed delete reads the key from the body
			op["requestBody"] = requestBody(schemaRef)
		}
		if status == http.StatusNoContent {
			success = map[string]any{code: map[string]any{"description": "No Content"}}
		} else {
			success = map[string]any{code: map[string]any{"description": "OK"}}
		}
	default: // POST / PUT / PATCH
		op["requestBody"] = requestBody(schemaRef)
		success = map[string]any{code: itemResponse(schemaRef)}
	}
	op["responses"] = mergeResponses(success)
	return withMCPDoc(r, e.Action, op)
}

// openAPIPath converts a stdlib-mux path to an OpenAPI path key plus its path
// parameters. Placeholders {id} and the catch-all {path...} both become {name}
// in the key (OpenAPI has no catch-all syntax) plus one string path parameter.
func openAPIPath(p string) (string, []any) {
	var params []any
	for _, seg := range strings.Split(p, "/") {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		params = append(params, map[string]any{
			"name":     strings.TrimSuffix(strings.Trim(seg, "{}"), "..."),
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
	}
	return OpenAPIPathKey(p), params
}

// OpenAPIPathKey renders a stdlib-mux path as its OpenAPI path key. The one
// rule it encodes: OpenAPI 3.1 has no multi-segment template syntax, so the
// wildcard `{path...}` documents as `{path}` — the document CANNOT express the
// arity, which is why losing it is translation and not drift.
//
// Exported because resreg/resregtest compares an advertised path against the
// mux pattern serving it, and must apply the SAME rule. A second copy of it
// there would make the doc-vs-mux guard disagree with the document it guards:
// proxyd mounts `/v1/proxyd_routes/{path...}` (its route keys contain slashes)
// and the guard read that as an advertised endpoint that 404s.
func OpenAPIPathKey(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		segs[i] = "{" + strings.TrimSuffix(strings.Trim(seg, "{}"), "...") + "}"
	}
	return strings.Join(segs, "/")
}

// conventionPaths is the PK-CRUD fallback for engine-managed resources that
// declare no Endpoints (their REST face IS the generic 5/8 CRUD).
func conventionPaths(r *Resource) map[string]map[string]any {
	schemaRef := map[string]any{"$ref": "#/components/schemas/" + schemaName(r.Name)}
	collection := fmt.Sprintf("/v1/%s", r.Name)
	out := map[string]map[string]any{
		collection: {
			"get": withMCPDoc(r, ActionList, map[string]any{
				"summary":     fmt.Sprintf("List %s", r.Name),
				"operationId": fmt.Sprintf("list_%s", r.Name),
				"responses":   mergeResponses(map[string]any{"200": listResponse(schemaRef)}),
			}),
			"post": withMCPDoc(r, ActionCreate, map[string]any{
				"summary":     fmt.Sprintf("Create %s", r.Name),
				"operationId": fmt.Sprintf("create_%s", r.Name),
				"requestBody": requestBody(schemaRef),
				"responses":   mergeResponses(map[string]any{"201": itemResponse(schemaRef)}),
			}),
		},
	}
	if pkPath := pkPathTemplate(r); pkPath != "" {
		item := collection + "/" + pkPath
		out[item] = map[string]any{
			"parameters": pkParams(r),
			"get": withMCPDoc(r, ActionGet, map[string]any{
				"summary":     fmt.Sprintf("Get %s", r.Name),
				"operationId": fmt.Sprintf("get_%s", r.Name),
				"responses":   mergeResponses(map[string]any{"200": itemResponse(schemaRef)}),
			}),
			"patch": withMCPDoc(r, ActionUpdate, map[string]any{
				"summary":     fmt.Sprintf("Update %s", r.Name),
				"operationId": fmt.Sprintf("update_%s", r.Name),
				"requestBody": requestBody(schemaRef),
				"responses":   mergeResponses(map[string]any{"200": itemResponse(schemaRef)}),
			}),
			"delete": withMCPDoc(r, ActionDelete, map[string]any{
				"summary":     fmt.Sprintf("Delete %s", r.Name),
				"operationId": fmt.Sprintf("delete_%s", r.Name),
				"responses":   mergeResponses(map[string]any{"204": map[string]any{"description": "No Content"}}),
			}),
		}
	}
	return out
}

// listResponse / itemResponse / requestBody are the shared JSON shapes for a
// collection read, a single-item read/write, and a mutation body.
func listResponse(schemaRef map[string]any) map[string]any {
	return map[string]any{
		"description": "OK",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"type": "array", "items": schemaRef},
			},
		},
	}
}

func itemResponse(schemaRef map[string]any) map[string]any {
	return map[string]any{
		"description": "OK",
		"content":     map[string]any{"application/json": map[string]any{"schema": schemaRef}},
	}
}

func requestBody(schemaRef map[string]any) map[string]any {
	return map[string]any{
		"required": true,
		"content":  map[string]any{"application/json": map[string]any{"schema": schemaRef}},
	}
}

// titleAction capitalizes an action verb for an operation summary
// ("add" → "Add").
func titleAction(a Action) string {
	s := string(a)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// withMCPDoc folds a resource's per-action agent-facing one-liner
// (Resource.MCPDoc[action]) into an OpenAPI operation — as both the
// operation `description` (so any OpenAPI reader sees when-to-use text)
// and a machine-findable `x-mcp-when` vendor extension (so an agent can
// pick REST operations straight from the published doc without a
// separate MCP tool list). No entry → operation unchanged. Additive:
// the operation's existing summary/params/responses are untouched.
func withMCPDoc(r *Resource, action Action, op map[string]any) map[string]any {
	doc, ok := r.MCPDoc[action]
	if !ok {
		return op
	}
	op["description"] = doc
	op["x-mcp-when"] = doc
	return op
}

// pkPathTemplate returns the `{pk}` URL segment for a resource, or ""
// when the resource has no PKFields. Composite PKs collapse to one URL
// parameter named after the first PK field — the path is documented as
// taking a dash-separated composite via the parameter description.
func pkPathTemplate(r *Resource) string {
	if r.meta == nil || len(r.meta.pkFields) == 0 {
		return ""
	}
	return "{" + r.meta.pkFields[0].col + "}"
}

// pkParams describes the single path parameter that carries the (maybe
// composite) PK. Composite PKs document the encoding so clients know
// to URL-encode dashes/slashes in field values.
func pkParams(r *Resource) []any {
	if r.meta == nil || len(r.meta.pkFields) == 0 {
		return nil
	}
	first := r.meta.pkFields[0]
	desc := "Primary key."
	if len(r.meta.pkFields) > 1 {
		cols := make([]string, len(r.meta.pkFields))
		for i, fm := range r.meta.pkFields {
			cols[i] = fm.col
		}
		desc = "Composite primary key: " + strings.Join(cols, "-") + " (URL-encode separators inside fields)."
	}
	return []any{
		map[string]any{
			"name":        first.col,
			"in":          "path",
			"required":    true,
			"description": desc,
			"schema":      map[string]any{"type": "string"},
		},
	}
}

// mergeResponses returns the per-operation responses map with the
// standard error refs merged in. Each error response is a $ref into
// components.responses so per-path JSON stays compact.
func mergeResponses(success map[string]any) map[string]any {
	out := map[string]any{}
	maps.Copy(out, success)
	for _, code := range []string{"400", "401", "403", "404", "409", "500"} {
		out[code] = map[string]any{"$ref": "#/components/responses/" + code}
	}
	return out
}

// stdResponses returns the standard error responses block referenced
// by every operation. Definitions are kept tiny — Error schema is one
// `{code, message}` object.
func stdResponses() map[string]any {
	errRef := map[string]any{
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/Error"},
			},
		},
	}
	with := func(desc string) map[string]any {
		m := map[string]any{"description": desc}
		maps.Copy(m, errRef)
		return m
	}
	return map[string]any{
		"400": with("Bad Request"),
		"401": with("Unauthorized"),
		"403": with("Forbidden"),
		"404": with("Not Found"),
		"409": with("Conflict"),
		"500": with("Internal Server Error"),
	}
}

// errorSchema is the canonical error envelope shared across all daemons.
// Matches the `{"error": "<msg>"}` shape `resreg.writeREST` already emits.
func errorSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"error": map[string]any{"type": "string"},
		},
		"required": []any{"error"},
	}
}

// schemaName picks the components/schemas key for a resource. Snake-
// case resource names (`acl_membership`, `proxyd_routes`) convert to
// PascalCase so the generated SDK names look natural.
func schemaName(resource string) string {
	parts := strings.Split(resource, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// marshalDeterministic JSON-encodes v with sorted object keys + 2-space
// indent. encoding/json already sorts map keys; the indent makes the
// output reviewable as a regular text file.
func marshalDeterministic(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// OpenAPIHandler returns an http.HandlerFunc serving the OpenAPI spec for
// whatever mux serves, at relative baseURL "/". The mux is the ONLY input
// besides the daemon name: there is no resource list to keep in step, which is
// what makes advertised-vs-mounted drift unrepresentable rather than merely
// tested for (BUGS F33).
//
// The document is built on FIRST REQUEST, not at construction, so this may be
// mounted on the same mux it documents — every other mount is in place by the
// time anyone asks. It is then cached for the process lifetime; reflection and
// route resolution are one-time, steady-state requests are byte-copies.
//
// Endpoint is public per spec 5/8 §"OpenAPI emission" — schemas
// describe API surface, not data. Mount BEFORE any auth middleware so
// `/openapi.json` is reachable without credentials.
func OpenAPIHandler(daemon string, mux *http.ServeMux) http.HandlerFunc {
	var (
		mu     sync.Mutex
		cached []byte
	)
	return func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		if cached == nil {
			spec, err := OpenAPI(daemon, "/", MountedResources(mux))
			if err != nil {
				mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			cached = spec
		}
		body := cached
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}
