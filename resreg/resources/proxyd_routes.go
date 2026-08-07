package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// ProxydRoutesRow mirrors proxyd_routes. PreserveHeaders is a JSON
// array string in DB but exposed as []string on the Row. StripPrefix
// is 0/1 INTEGER in DB but a clean Go field — engine uses two
// "shadow" columns for the raw DB representation and BeforeInsert /
// AfterScan keep them in sync.
//
// Trade-off note (spec 5/8 §"What does NOT come for free"): JSON-
// blob columns force a hook. We keep the public field shape natural
// (`[]string`, `bool`) and pay the conversion in two short hooks.
type ProxydRoutesRow struct {
	Path               string   `db:"path"             yaml:"path"             json:"path"`
	Backend            string   `db:"backend"          yaml:"backend"          json:"backend"`
	Auth               string   `db:"auth"             yaml:"auth"             json:"auth"`
	GatedBy            string   `db:"gated_by"         yaml:"gated_by,omitempty"         json:"gated_by,omitempty"`
	RedirectTo         string   `db:"redirect_to"      yaml:"redirect_to,omitempty"      json:"redirect_to,omitempty"`
	PreserveHeadersRaw string   `db:"preserve_headers" yaml:"-"                json:"-"`
	StripPrefixRaw     int      `db:"strip_prefix"     yaml:"-"                json:"-"`
	PreserveHeaders    []string `db:"-"                yaml:"preserve_headers,omitempty" json:"preserve_headers,omitempty"`
	StripPrefix        bool     `db:"-"                yaml:"strip_prefix,omitempty"     json:"strip_prefix,omitempty"`
}

// ProxydRoutesMCPDoc is the single owner of proxyd_routes' per-action agent-
// facing one-liners. Three decls consume it: this catalog registration (drives
// /openapi.json via openapi.go withMCPDoc), proxyd's dispatch decl (the live
// handler), and webd's forwarder decl. Exporting it here keeps the operator
// surface from drifting across the three package-main copies (spec 5/5 §"Single
// source of truth").
var ProxydRoutesMCPDoc = map[resreg.Action]string{
	resreg.ActionList:   "List proxyd's runtime route table.",
	resreg.ActionGet:    "Read one proxyd route by path.",
	resreg.ActionCreate: "Create a proxyd route. Body fields mirror the TOML proxyd_route block.",
	resreg.ActionUpdate: "Update fields on an existing proxyd route. Path is the key.",
	resreg.ActionDelete: "Delete a proxyd route. Idempotent.",
}

// ProxydRoutesMCPArgs is the explicit per-action arg list for the two proxyd_routes
// decls that carry NO RowType (proxyd's dispatch decl + webd's forwarder), so their
// derived MCP tools can't reflect args. This catalog decl DOES have a RowType and
// reflects its own args; it doesn't consume this map. Single owner so proxyd + webd
// don't drift.
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

// ProxydRoutesEndpoints is the single owner of the REST endpoint set for
// proxyd_routes. proxyd's live dispatch decl and webd's forwarder decl both
// reference this so the mounted routes + the derived MCP action set never
// drift (the last drift-able copy after MCPDoc/MCPArgs were single-sourced).
var ProxydRoutesEndpoints = []resreg.Endpoint{
	{Verb: "GET", Path: "/v1/proxyd_routes", Action: resreg.ActionList, Status: http.StatusOK},
	{Verb: "GET", Path: "/v1/proxyd_routes/{path...}", Action: resreg.ActionGet, Status: http.StatusOK},
	{Verb: "POST", Path: "/v1/proxyd_routes", Action: resreg.ActionCreate, Status: http.StatusCreated},
	{Verb: "PATCH", Path: "/v1/proxyd_routes/{path...}", Action: resreg.ActionUpdate, Status: http.StatusOK},
	{Verb: "DELETE", Path: "/v1/proxyd_routes/{path...}", Action: resreg.ActionDelete, Status: http.StatusNoContent},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      ProxydRoutesName,
		Table:     "proxyd_routes",
		DB:        resreg.SubsystemRoutd,
		RowType:   reflect.TypeFor[ProxydRoutesRow](),
		PKFields:  []string{"Path"},
		Endpoints: ProxydRoutesEndpoints,
		MCPDoc:    ProxydRoutesMCPDoc,
		Hooks: resreg.Hooks{
			BeforeInsert: func(_ context.Context, _ *sql.Tx, row any) error {
				r := row.(*ProxydRoutesRow)
				headers := r.PreserveHeaders
				if headers == nil {
					headers = []string{}
				}
				b, err := json.Marshal(headers)
				if err != nil {
					return err
				}
				r.PreserveHeadersRaw = string(b)
				if r.StripPrefix {
					r.StripPrefixRaw = 1
				} else {
					r.StripPrefixRaw = 0
				}
				return nil
			},
			AfterScan: func(row any) error {
				r := row.(*ProxydRoutesRow)
				if r.PreserveHeadersRaw != "" {
					if err := json.Unmarshal([]byte(r.PreserveHeadersRaw), &r.PreserveHeaders); err != nil {
						return err
					}
				}
				r.StripPrefix = r.StripPrefixRaw != 0
				return nil
			},
		},
	})
}
