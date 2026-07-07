package resources

import (
	"context"
	"database/sql"
	"reflect"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// WebRoutesRow mirrors web_routes: PK on path_prefix; nullable redirect_to.
type WebRoutesRow struct {
	PathPrefix string `db:"path_prefix" yaml:"path_prefix" json:"path_prefix"`
	Access     string `db:"access"      yaml:"access"      json:"access"`
	RedirectTo string `db:"redirect_to" yaml:"redirect_to,omitempty" json:"redirect_to,omitempty"`
	Folder     string `db:"folder"      yaml:"folder"      json:"folder"`
	CreatedAt  string `db:"created_at"  yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

// WebRoutesEndpoints is the single owner of the web_routes REST endpoint set:
// routd's web_route tools (web_routes_resource.go) reference it. create is a PUT
// upsert on the collection and delete addresses the row by `path` in the body
// (no {pk} path), so the real faces diverge from the PK-CRUD convention.
var WebRoutesEndpoints = []resreg.Endpoint{
	{Verb: "PUT", Path: "/v1/web_routes", Action: resreg.ActionCreate},
	{Verb: "DELETE", Path: "/v1/web_routes", Action: resreg.ActionDelete},
	{Verb: "GET", Path: "/v1/web_routes", Action: resreg.ActionList},
}

// WebRoutesMCPNames maps each action to the flat tool name the live agent already
// calls; routd's web_routes_resource.go references it (agent socket derivation)
// and ipc.ListTools reads it via the registry walk. Spec 5/44.
var WebRoutesMCPNames = map[resreg.Action]string{
	resreg.ActionCreate: "set_web_route",
	resreg.ActionDelete: "del_web_route",
	resreg.ActionList:   "list_web_routes",
}

// WebRoutesMCPDoc is the single owner of the web_route tools' agent-facing
// one-liners. Copy verbatim — the agent wire contract.
var WebRoutesMCPDoc = map[resreg.Action]string{
	resreg.ActionCreate: "Upsert a web route: control whether a URL path is public, auth-gated, denied, or redirected. " +
		"`path` must start with `/`. `access` is one of public|auth|deny|redirect. " +
		"When access=redirect, `redirect_to` is required and must point into this folder's own " +
		"slot (/pub/<folder>/... or /priv/<folder>/...) — no external URLs or other folders. " +
		"Route is scoped to this folder.",
	resreg.ActionDelete: "Delete a web route by path. Only routes owned by this folder may be deleted (operators can delete any).",
	resreg.ActionList:   "List all web routes owned by this folder. Returns a JSON array of {path_prefix, access, redirect_to, folder, created_at}.",
}

// WebRoutesMCPArgs is the explicit per-action arg list. The agent face carries
// {path, access, redirect_to}, NOT the RowType-reflected columns, so this overrides
// RowType reflection for the derived agent/browser tools.
var WebRoutesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionCreate: {
		{Name: "path", Type: "string", Required: true},
		{Name: "access", Type: "string", Required: true},
		{Name: "redirect_to", Type: "string"},
	},
	resreg.ActionDelete: {{Name: "path", Type: "string", Required: true}},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          "web_routes",
		Table:         "web_routes",
		RowType:       reflect.TypeOf(WebRoutesRow{}),
		PKFields:      []string{"PathPrefix"},
		Endpoints:     WebRoutesEndpoints,
		MCPDoc:        WebRoutesMCPDoc,
		MCPArgs:       WebRoutesMCPArgs,
		MCPNames:      WebRoutesMCPNames,
		Scope:         resreg.ScopeSpec{Field: "Folder"},
		StampedFields: []string{"CreatedAt"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(ctx context.Context, tx *sql.Tx, row any) error {
				r := row.(*WebRoutesRow)
				if r.CreatedAt == "" {
					r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
				}
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				"RedirectTo": {
					Read:  "COALESCE(redirect_to, '')",
					Write: nilIfEmptyString,
				},
			},
		},
	})
}
