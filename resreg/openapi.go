package resreg

// OpenAPI emission (spec 5/8 §"OpenAPI emission"): one walk over the registry
// yields an OpenAPI 3.1 document, served from every daemon's `/openapi.json`.
// No huma, no swag, no codegen. Schemas cannot drift: handler and doc read the
// same struct, and a response body, which reflection cannot see, is DECLARED
// (Resource.Responses). Paths cannot drift: the advertised set is READ OFF THE
// MUX (MountedResources), so a daemon documents exactly the faces RegisterREST
// mounted (BUGS F33, F21, F27, F32).

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
// MountedResources(mux) and never assemble it by hand (BUGS F33). One
// operation per Endpoint, its real mounted verb+path; there is no convention
// fallback that could document a resource somewhere it is not served.
// Deterministic; resources without RowType contribute nothing.
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

// MountedResources returns the registry resources mux ACTUALLY serves, each
// copied down to the endpoints RegisterREST mounted on it. Derivation is by
// mount IDENTITY, not by path: proxyd's `/` catch-all answers every probe.
// Registration order is preserved.
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
// resource. Every field is compared because each is a drift shape seen in the
// wild: a re-path, a look-alike face, a hand-rolled or catch-all mount.
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

// ConcretePath substitutes every {placeholder} in a mux pattern with "x" so
// the pattern can be looked up as a request path; "x" collides with no
// hand-registered sibling literal route.
func ConcretePath(p string) string { return pathPlaceholder.ReplaceAllString(p, "x") }

// rowSchema reflects a RowType into an OpenAPI 3.1 schema object; `json:`
// tags drive property names.
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
			// encoding/json PROMOTES an untagged embedded struct's fields into
			// the outer object, so the schema inlines them (router.RouteView
			// embeds core.Route).
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
// outer object. A TAGGED embedded field is a named member, not one of these.
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
// returns the per-action fragment naming each. A type is registered under its
// Go NAME; two different types under one name is refused rather than letting
// the later walk overwrite the earlier schema.
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

// responseFragment renders one declared response type, registering every
// named struct it reaches in schemas. Anything that is not a struct or a
// slice of one is refused at emission: a doc that cannot be built is louder
// than one that guesses.
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
	// Answering with the row itself reuses the resource's own component.
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

// endpointPaths renders one operation per declared Endpoint: the path key +
// parameters from the mux Path, the request shape from the verb, the response
// shape ONLY from Resource.Responses.
func endpointPaths(r *Resource, responses map[Action]map[string]any) map[string]map[string]any {
	schemaRef := map[string]any{"$ref": "#/components/schemas/" + schemaName(r.Name)}
	out := map[string]map[string]any{}
	for _, e := range r.Endpoints {
		// RegisterREST skips MCP-only actions too, so doc and mount stay in step.
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
// body, a body-addressed DELETE (no path placeholder) included; the success
// status is Endpoint.Status when set, else 200 (matching restHandler).
func endpointOp(r *Resource, e Endpoint, schemaRef map[string]any, hasPathParam bool, resp map[string]any) map[string]any {
	status := e.Status
	if status == 0 {
		status = http.StatusOK
	}
	op := map[string]any{
		"summary":     fmt.Sprintf("%s %s", titleAction(e.Action), r.Name),
		"operationId": fmt.Sprintf("%s_%s", e.Action, r.Name),
	}
	switch e.Verb {
	case http.MethodGet:
	case http.MethodDelete:
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

// successResponse renders the success entry. Only a DECLARED response type
// puts a body schema in the document; a guess is worse than a gap (BUGS L120).
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
// parameters.
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

// OpenAPIPathKey renders a stdlib-mux path as its OpenAPI path key: OpenAPI
// 3.1 has no multi-segment template, so `{path...}` documents as `{path}`.
// Exported because resregtest compares an advertised path against the mux
// pattern serving it and must apply the SAME rule, or the guard reads
// proxyd's `/v1/proxyd_routes/{path...}` as an endpoint that 404s.
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

// titleAction capitalizes an action verb for an operation summary.
func titleAction(a Action) string {
	s := string(a)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// withMCPDoc folds Resource.MCPDoc[action] into an operation as both its
// `description` and a machine-findable `x-mcp-when` extension, so an agent
// can pick REST operations straight from the published doc. No entry →
// operation unchanged.
func withMCPDoc(r *Resource, action Action, op map[string]any) map[string]any {
	doc, ok := r.MCPDoc[action]
	if !ok {
		return op
	}
	op["description"] = doc
	op["x-mcp-when"] = doc
	return op
}

// mergeResponses returns the per-operation responses map with the standard
// error refs merged in.
func mergeResponses(success map[string]any) map[string]any {
	out := map[string]any{}
	maps.Copy(out, success)
	for _, code := range []string{"400", "401", "403", "404", "409", "500"} {
		out[code] = map[string]any{"$ref": "#/components/responses/" + code}
	}
	return out
}

// stdResponses returns the standard error responses block referenced by every
// operation.
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

// errorSchema is the `{"error": "<msg>"}` envelope writeREST emits.
func errorSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"error": map[string]any{"type": "string"},
		},
		"required": []any{"error"},
	}
}

// schemaName picks the components/schemas key for a resource: snake_case to
// PascalCase.
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

// marshalDeterministic JSON-encodes v with sorted object keys + 2-space indent.
func marshalDeterministic(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// OpenAPIHandler returns an http.HandlerFunc serving the OpenAPI spec for
// whatever mux serves. The mux is the ONLY input besides the daemon name,
// which makes advertised-vs-mounted drift unrepresentable (BUGS F33). The
// document is built on FIRST REQUEST, so this may be mounted on the same mux
// it documents, and is then cached for the process lifetime. Public: mount
// BEFORE any auth middleware (spec 5/8 §"OpenAPI emission").
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
