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
// catalog. Drift between handler and doc is structurally impossible
// because both read the same struct.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
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
// Endpoints follow the convention from spec 5/5 + 5/36:
//
//	GET    /v1/<name>                 → list (200: array<Schema>)
//	POST   /v1/<name>                 → create (201: Schema)
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

// resourcePaths builds the four `/v1/<name>` operations for one
// resource. Returns a map[path]ops; ops is itself a map keyed by HTTP
// method (lowercased per OpenAPI convention).
func resourcePaths(r *Resource) map[string]map[string]any {
	schemaRef := map[string]any{"$ref": "#/components/schemas/" + schemaName(r.Name)}
	listResp := map[string]any{
		"description": "OK",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type":  "array",
					"items": schemaRef,
				},
			},
		},
	}
	itemResp := map[string]any{
		"description": "OK",
		"content": map[string]any{
			"application/json": map[string]any{"schema": schemaRef},
		},
	}
	noContent := map[string]any{"description": "No Content"}
	body := map[string]any{
		"required": true,
		"content": map[string]any{
			"application/json": map[string]any{"schema": schemaRef},
		},
	}

	collection := fmt.Sprintf("/v1/%s", r.Name)
	out := map[string]map[string]any{
		collection: {
			"get": map[string]any{
				"summary":     fmt.Sprintf("List %s", r.Name),
				"operationId": fmt.Sprintf("list_%s", r.Name),
				"responses":   mergeResponses(map[string]any{"200": listResp}),
			},
			"post": map[string]any{
				"summary":     fmt.Sprintf("Create %s", r.Name),
				"operationId": fmt.Sprintf("create_%s", r.Name),
				"requestBody": body,
				"responses":   mergeResponses(map[string]any{"201": itemResp}),
			},
		},
	}

	if pkPath := pkPathTemplate(r); pkPath != "" {
		item := collection + "/" + pkPath
		params := pkParams(r)
		out[item] = map[string]any{
			"parameters": params,
			"patch": map[string]any{
				"summary":     fmt.Sprintf("Update %s", r.Name),
				"operationId": fmt.Sprintf("update_%s", r.Name),
				"requestBody": body,
				"responses":   mergeResponses(map[string]any{"200": itemResp}),
			},
			"delete": map[string]any{
				"summary":     fmt.Sprintf("Delete %s", r.Name),
				"operationId": fmt.Sprintf("delete_%s", r.Name),
				"responses":   mergeResponses(map[string]any{"204": noContent}),
			},
		}
	}
	return out
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
