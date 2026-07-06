package resreg

// OpenAPI emission — spec 5/36 §"OpenAPI emission".
//
// The same `RowType` reflection that drives YAML/JSON/SQL also yields
// an OpenAPI 3.1 schema document for free. One walk over the registry,
// one JSON blob, served from every daemon's `/openapi.json` so the
// platform's HTTP surface is discoverable without hand-written specs.
//
// Subsumes spec 5/4 (`openapi-discoverable`): no `huma`, no `swag`, no
// codegen — `encoding/json` + `reflect` + the existing per-resource
// catalog. Schemas can't drift — both handler and doc read the same
// struct. Paths can't drift either: when a resource declares its real
// mounted faces in `Endpoints` (the same slice RegisterREST mounts), the
// doc emits exactly those verbs+paths. A resource with no Endpoints
// (engine-managed CRUD tables) falls back to the 5/36 PK-CRUD convention.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// OpenAPI walks the registry and emits an OpenAPI 3.1 JSON document.
//
//   - daemon       title + identifier in `info.title`
//   - baseURL      single entry in `servers[]` (use "/" for relative)
//   - resources    nil = include every registered resource with a RowType;
//     non-nil = include only the named resources (in the
//     order given). Resources not in the registry are skipped.
//
// The document is deterministic: components/schemas + paths keys are
// emitted via the same map-key sort `encoding/json` applies. Resources
// without `RowType` (forwarders, MCP-only) contribute nothing.
//
// A resource that declares `Endpoints` emits exactly those (one operation
// per endpoint — its real mounted verb+path). A resource with no Endpoints
// falls back to the PK-CRUD convention from spec 5/5 + 5/36:
//
//	GET    /v1/<name>                 → list (200: array<Schema>)
//	POST   /v1/<name>                 → create (201: Schema)
//	GET    /v1/<name>/{pk...}         → get (200: Schema)
//	PATCH  /v1/<name>/{pk...}         → update (200: Schema)
//	DELETE /v1/<name>/{pk...}         → delete (204)
//
// The PK URL segment concatenates each `pk:` field's `db:` column with
// dashes (`/v1/routes/{seq}-{match}-{target}`). Composite PKs collapse
// into one URL parameter named after the first PK field — descriptions
// flag the encoding so clients know to URL-encode separators.
//
// Standard error responses (400, 401, 403, 404, 409, 500) are referenced
// from `components.responses` so per-path bloat stays small.
func OpenAPI(daemon, baseURL string, resources []string) ([]byte, error) {
	if baseURL == "" {
		baseURL = "/"
	}
	rs := selectResources(resources)

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
			"description": fmt.Sprintf("Engine-generated OpenAPI for the %s daemon. Spec: arizuko/specs/5/36-yaml-manifests.md.", daemon),
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

// selectResources picks resources by name (when names is non-nil) or
// every resource (when names is nil), preserving registration order in
// the "all" case so output stays stable across runs.
func selectResources(names []string) []*Resource {
	all := All()
	if names == nil {
		return all
	}
	byName := make(map[string]*Resource, len(all))
	for _, r := range all {
		byName[r.Name] = r
	}
	out := make([]*Resource, 0, len(names))
	for _, n := range names {
		if r := byName[n]; r != nil {
			out = append(out, r)
		}
	}
	return out
}

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
// one with none falls back to the spec 5/36 PK-CRUD convention. Returns a
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
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		name := strings.TrimSuffix(strings.Trim(seg, "{}"), "...")
		segs[i] = "{" + name + "}"
		params = append(params, map[string]any{
			"name":     name,
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
	}
	return strings.Join(segs, "/"), params
}

// conventionPaths is the PK-CRUD fallback for engine-managed resources that
// declare no Endpoints (their REST face IS the generic 5/36 CRUD).
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
	for k, v := range success {
		out[k] = v
	}
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
		for k, v := range errRef {
			m[k] = v
		}
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

// OpenAPIHandler returns an http.HandlerFunc serving the OpenAPI spec
// for the given daemon + resources at relative baseURL "/". Lazy-builds
// the JSON on first hit and caches it for the process lifetime;
// reflection is one-time, steady-state requests are byte-copies.
//
// Endpoint is public per spec 5/36 §"OpenAPI emission" — schemas
// describe API surface, not data. Mount BEFORE any auth middleware so
// `/openapi.json` is reachable without credentials.
func OpenAPIHandler(daemon string, resources []string) http.HandlerFunc {
	var (
		mu     sync.Mutex
		cached []byte
	)
	return func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		if cached == nil {
			spec, err := OpenAPI(daemon, "/", resources)
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
