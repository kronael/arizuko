package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// ProxydRoutesRow mirrors proxyd_routes. PreserveHeaders and StripPrefix
// DECLARE the encoding that renders them (resreg/codec.go).
type ProxydRoutesRow struct {
	Path       string `db:"path"             yaml:"path"             json:"path"`
	Backend    string `db:"backend"          yaml:"backend"          json:"backend"`
	Auth       string `db:"auth"             yaml:"auth"             json:"auth"`
	GatedBy    string `db:"gated_by"         yaml:"gated_by,omitempty"         json:"gated_by,omitempty"`
	RedirectTo string `db:"redirect_to"      yaml:"redirect_to,omitempty"      json:"redirect_to,omitempty"`
	// Owner is who may rewrite the row: "arizuko" (every `arizuko generate`
	// does) or "operator" (generate never touches it). Read-only — the writer
	// stamps it, which is why it appears in no MCPArgs list below.
	Owner           string   `db:"owner"            yaml:"owner,omitempty"            json:"owner,omitempty"`
	PreserveHeaders []string `db:"preserve_headers,json" yaml:"preserve_headers,omitempty" json:"preserve_headers,omitempty"`
	StripPrefix     bool     `db:"strip_prefix,bool"     yaml:"strip_prefix,omitempty"     json:"strip_prefix,omitempty"`
}

// ProxydRoutesMCPDoc is the single owner of proxyd_routes' agent-facing
// one-liners, read by this catalog, proxyd's dispatch decl and webd's
// forwarder decl.
var ProxydRoutesMCPDoc = map[resreg.Action]string{
	resreg.ActionList:   "List proxyd's runtime route table.",
	resreg.ActionGet:    "Read one proxyd route by path.",
	resreg.ActionCreate: "Create a proxyd route. Body fields mirror the TOML proxyd_route block.",
	resreg.ActionUpdate: "Update fields on an existing proxyd route. Path is the key.",
	resreg.ActionDelete: "Delete a proxyd route. Idempotent.",
}

// ProxydRoutesMCPArgs overrides RowType reflection: the wire args carry
// descriptions and an `auth` requirement no struct tag can express.
var ProxydRoutesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionGet: {{Name: "path", Type: "string", Required: true}},
	resreg.ActionCreate: {
		{Name: "path", Type: "string", Required: true},
		{Name: "backend", Type: "string", Description: "proxy target; backend OR redirect_to is required"},
		{Name: "redirect_to", Type: "string", Description: "redirect target; alternative to backend"},
		{Name: "auth", Type: "string", Required: true, Description: "public | user | operator"},
		{Name: "gated_by", Type: "string"},
		{Name: "preserve_headers", Type: "array"},
		{Name: "strip_prefix", Type: "bool"},
	},
	resreg.ActionUpdate: {
		{Name: "path", Type: "string", Required: true},
		{Name: "backend", Type: "string"},
		{Name: "redirect_to", Type: "string"},
		{Name: "auth", Type: "string"},
		{Name: "gated_by", Type: "string"},
		{Name: "preserve_headers", Type: "array"},
		{Name: "strip_prefix", Type: "bool"},
	},
	resreg.ActionDelete: {{Name: "path", Type: "string", Required: true}},
}

// ProxydRoutesResponses: proxyd's handler answers its own Route struct, whose
// json tags are this row's field for field, on every face but the 204 delete.
var ProxydRoutesResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList:   reflect.TypeFor[[]ProxydRoutesRow](),
	resreg.ActionGet:    reflect.TypeFor[ProxydRoutesRow](),
	resreg.ActionCreate: reflect.TypeFor[ProxydRoutesRow](),
	resreg.ActionUpdate: reflect.TypeFor[ProxydRoutesRow](),
}

// ProxydRoutesEndpoints is the single owner of the REST endpoint set, read by
// proxyd's dispatch decl and webd's forwarder decl.
var ProxydRoutesEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/proxyd_routes", Action: resreg.ActionList, Status: http.StatusOK},
	{Verb: "GET", Path: "/v1/proxyd_routes/{path...}", Action: resreg.ActionGet, Status: http.StatusOK},
	{Verb: "POST", Path: "/v1/proxyd_routes", Action: resreg.ActionCreate, Status: http.StatusCreated},
	{Verb: "PATCH", Path: "/v1/proxyd_routes/{path...}", Action: resreg.ActionUpdate, Status: http.StatusOK},
	{Verb: "DELETE", Path: "/v1/proxyd_routes/{path...}", Action: resreg.ActionDelete, Status: http.StatusNoContent},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          ProxydRoutesName,
		Table:         "proxyd_routes",
		DB:            resreg.SubsystemRoutd,
		RowType:       reflect.TypeFor[ProxydRoutesRow](),
		PKFields:      []string{"Path"},
		EntitySortKey: []string{"Path"},
		Endpoints:     ProxydRoutesEndpoints,
		Responses:     ProxydRoutesResponses,
		MCPDoc:        ProxydRoutesMCPDoc,
		MCPArgs:       ProxydRoutesMCPArgs,
	})
}
