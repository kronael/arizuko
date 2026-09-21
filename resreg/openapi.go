package resreg

// OpenAPI emission — spec 5/8 §"OpenAPI emission".
//
// The same `RowType` reflection that drives YAML/JSON/SQL also yields an OpenAPI
// 3.1 document: one walk over the registry, served from every daemon's
// `/openapi.json`. No huma, no swag, no codegen.
//
// Schemas cannot drift — handler and doc read the same struct. A response body
// is the one thing reflection cannot see, because a Handler returns `any`; so
// the resource DECLARES the answering type (Resource.Responses) and an action
// that declares none documents no body schema at all. Paths cannot
// drift either, and for a stronger reason: the advertised set is READ OFF THE MUX
// (MountedResources), so a daemon documents exactly the faces RegisterREST put on
// its routing table. Each daemon used to hand OpenAPIHandler a list of resource
// names with no reference to its mux, the cause under every drift found (BUGS
// F33, F21, F27, F32).

import (
	"encoding/json"
	"errors"
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

// OpenAPI emits an OpenAPI 3.1 JSON document for rs, each resource already
// trimmed to the endpoints it serves. Production callers get rs from
// MountedResources(mux) via OpenAPIHandler and never assemble it by hand — that
// hand-assembly WAS BUGS F33.
//
// The document is deterministic: schema and path keys sort as encoding/json sorts
// map keys. Resources without RowType contribute nothing.
//
// One operation per Endpoint — its real mounted verb+path. There is no second
// renderer: a PK-CRUD convention fallback used to sit beside this, so a resource
// could be DOCUMENTED at `/v1/<name>` while being SERVED somewhere else.
//
// Standard error responses are referenced from `components.responses` so per-path
// bloat stays small.
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
		responses, err := responseRefs(r, schemas)
		if err != nil {
			return nil, fmt.Errorf("%s: response schema: %w", r.Name, err)
		}
		for path, ops := range endpointPaths(r, responses) {
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

// MountedResources returns the registry resources mux ACTUALLY serves, each copied
// down to just the endpoints RegisterREST mounted on it. A daemon can no longer
// name what it documents, so it can no longer name something it does not serve.
//
// Derivation is by mount IDENTITY, not by path. Asking only "does something answer
// here?" would be a new lie in place of the old one — proxyd's `/` catch-all
// answers every probe. Registration order is preserved so output stays stable.
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
		if embedded, ok := embeddedStruct(sf); ok {
			// encoding/json PROMOTES an untagged embedded struct's fields into the
			// outer object, so the schema has to inline them. Left to the field walk
			// below they render as one member named after the type, and the document
			// then describes an object the wire never carries: router.RouteView
			// embeds core.Route, which is where every route field lives.
			sub, err := rowSchema(embedded)
			if err != nil {
				return nil, err
			}
			maps.Copy(props, sub["properties"].(map[string]any))
			if req, ok := sub["required"].([]string); ok {
				required = append(required, req...)
			}
			continue
		}
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

// embeddedStruct reports the struct type whose fields sf promotes into its
// outer object — an anonymous field with no name of its own. A TAGGED embedded
// field is a named member like any other, so it is not one of these.
func embeddedStruct(sf reflect.StructField) (reflect.Type, bool) {
	if !sf.Anonymous || sf.Tag.Get("json") != "" || sf.Tag.Get("yaml") != "" {
		return nil, false
	}
	t := sf.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t, t.Kind() == reflect.Struct
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

// responseRefs reflects a resource's declared response types into schemas and
// returns the per-action JSON-Schema fragment naming each. A type is registered
// under its Go NAME, so two resources answering with one type share the
// component; two different types under one name is refused rather than letting
// the later walk overwrite the earlier resource's schema.
func responseRefs(r *Resource, schemas map[string]any) (map[Action]map[string]any, error) {
	if len(r.Responses) == 0 {
		return nil, nil
	}
	out := map[Action]map[string]any{}
	for action, rt := range r.Responses {
		if rt == nil {
			return nil, fmt.Errorf("%s: nil type", action)
		}
		frag, err := responseFragment(r, rt, schemas)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", action, err)
		}
		out[action] = frag
	}
	return out, nil
}

// responseFragment renders one declared response type, registering every named
// struct it reaches in schemas. A slice is the collection shape; anything that
// is not a struct or a slice of one has no schema to reflect and is refused at
// emission — a doc that cannot be built is louder than one that guesses.
func responseFragment(r *Resource, rt reflect.Type, schemas map[string]any) (map[string]any, error) {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
		items, err := responseFragment(r, rt.Elem(), schemas)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	}
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("want a struct or a slice of one, got %s", rt.Kind())
	}
	// Answering with the row itself is the common case, and it reuses the
	// resource's own component: a second one under the Go type name would put
	// the same schema in the document twice under two names.
	name := rt.Name()
	if rt == r.RowType {
		name = schemaName(r.Name)
	}
	if name == "" {
		return nil, errors.New("an anonymous struct has no component name; declare a named type")
	}
	schema, err := rowSchema(rt)
	if err != nil {
		return nil, err
	}
	if prev, ok := schemas[name]; ok && !reflect.DeepEqual(prev, schema) {
		return nil, fmt.Errorf("two different types both name component %q", name)
	}
	schemas[name] = schema
	return map[string]any{"$ref": "#/components/schemas/" + name}, nil
}

// endpointPaths renders one operation per declared Endpoint — the doc mirrors
// what RegisterREST mounts, so the two cannot drift. The path key + parameters
// come from the stdlib-mux Path (placeholders {id}/{path...} → OpenAPI {param});
// the request shape comes from the verb, the response shape ONLY from
// Resource.Responses. withMCPDoc adds the agent-facing x-mcp-when per operation
// when the resource carries an MCPDoc entry.
func endpointPaths(r *Resource, responses map[Action]map[string]any) map[string]map[string]any {
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
		item[strings.ToLower(e.Verb)] = endpointOp(r, e, schemaRef, len(params) > 0, responses[e.Action])
	}
	return out
}

// endpointOp builds one operation for an Endpoint. Mutations carry a request
// body (a body-addressed DELETE — one with no path placeholder — also reads the
// target from the JSON body); the success status is Endpoint.Status when set,
// else 200 (matching restHandler). The success BODY is whatever the resource
// declared for this action and nothing otherwise — see successResponse.
func endpointOp(r *Resource, e Endpoint, schemaRef map[string]any, hasPathParam bool, resp map[string]any) map[string]any {
	status := e.Status
	if status == 0 {
		status = http.StatusOK
	}
	op := map[string]any{
		"summary":     fmt.Sprintf("%s %s", titleAction(e.Action), r.Name),
		"operationId": fmt.Sprintf("%s_%s", e.Action, r.Name),
	}
	switch {
	case e.Verb == http.MethodGet:
	case e.Verb == http.MethodDelete:
		if !hasPathParam { // body-addressed delete reads the key from the body
			op["requestBody"] = requestBody(schemaRef)
		}
	default: // POST / PUT / PATCH
		op["requestBody"] = requestBody(schemaRef)
	}
	op["responses"] = mergeResponses(map[string]any{
		strconv.Itoa(status): successResponse(status, resp),
	})
	return withMCPDoc(r, e.Action, op)
}

// successResponse renders the success entry. A DECLARED response type is the
// only thing that puts a body schema in the document: an action without one
// documents "OK" and says nothing about the body, because the emitter cannot
// see what a Handler returns and a guess is worse than a gap (BUGS L120).
func successResponse(status int, resp map[string]any) map[string]any {
	if status == http.StatusNoContent {
		return map[string]any{"description": "No Content"}
	}
	if resp == nil {
		return map[string]any{"description": "OK"}
	}
	return map[string]any{
		"description": "OK",
		"content":     map[string]any{"application/json": map[string]any{"schema": resp}},
	}
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

// requestBody is the shared JSON shape for a mutation body.
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
