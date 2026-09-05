package resources

import (
	"context"
	"database/sql"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// ACLRow mirrors the acl table: full composite PK + nullable granted_by.
// `granted_by` is read via COALESCE so the Go field can stay `string`
// (empty = NULL); same shape as existing store/acl.go scanACLRow.
//
// EVERY column, not just the interesting ones. acl has no folder scope, so
// apply rebuilds it wholesale (DELETE + INSERT of the manifest's rows) — a
// column missing here is a column silently reset to its DEFAULT by any apply
// that mentions acl. `grant_option` (spec 5/33, WITH GRANT OPTION) was that
// column: absent from the row type, so a rebuild dropped every holder's right
// to re-delegate.
type ACLRow struct {
	Principal   string `db:"principal" yaml:"principal" json:"principal"`
	Action      string `db:"action"    yaml:"action"    json:"action"`
	Scope       string `db:"scope"     yaml:"scope"     json:"scope"`
	Effect      string `db:"effect"    yaml:"effect"    json:"effect,omitempty"`
	Params      string `db:"params"    yaml:"params"    json:"params,omitempty"`
	Predicate   string `db:"predicate" yaml:"predicate" json:"predicate,omitempty"`
	GrantOption bool   `db:"grant_option" yaml:"grant_option,omitempty" json:"grant_option,omitempty"`
	GrantedBy   string `db:"granted_by" yaml:"granted_by,omitempty" json:"granted_by,omitempty"`
	GrantedAt   string `db:"granted_at" yaml:"granted_at,omitempty" json:"granted_at,omitempty"`
}

// ACLEndpoints is the single owner of the acl endpoint set: routd's mountACL
// mounts it verbatim, deriveMCPTools reads it for the agent's acl tools, and
// openapi.go emits one operation per non-MCPOnly entry. add is a POST and remove
// a body-addressed DELETE on the collection (principal/scope in the body, no
// {pk} path), so the real faces diverge from the composite-PK-CRUD convention.
//
// list is MCPOnly: list_acl is agent-only and nothing mounts a REST twin. It
// carried Verb+Path while mountACL trimmed Endpoints to add/remove, so routd's
// /openapi.json advertised a GET /v1/acl that 404s (BUGS F27, the F21 class).
var ACLEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/acl", Action: resreg.Action("add")},
	{Verb: "DELETE", Path: "/v1/acl", Action: resreg.Action("remove")},
	{Action: resreg.ActionList, MCPOnly: true},
}

// ACLMCPNames maps each action to the flat tool name the live agent already calls;
// routd's acl_resource.go references it (agent socket derivation) and ipc.ListTools
// reads it via the registry walk. Spec 5/16.
var ACLMCPNames = map[resreg.Action]string{
	resreg.Action("add"):    "add_acl",
	resreg.Action("remove"): "remove_acl",
	resreg.ActionList:       "list_acl",
}

// ACLMCPDoc is the single owner of the acl tools' agent-facing one-liners. Copy
// verbatim — the agent wire contract.
var ACLMCPDoc = map[resreg.Action]string{
	resreg.Action("add"): "Grant a principal access to a folder scope (an acl row); scope '**' grants the operator role. " +
		"You can only grant within your own authority. Defaults action=admin, effect=allow. " +
		"`params` narrows the row to calls whose arguments match, as 'name=glob[,name=glob]' — " +
		"it constrains ONLY the arguments it names, and a rule cannot name a non-scalar argument at all. " +
		"`grant_option` lets the grantee re-grant the row; you can only pass it on where you hold it yourself.",
	resreg.Action("remove"): "Revoke a principal's access to a folder scope (drop an acl row); scope '**' revokes the operator role. " +
		"You can only revoke within your own authority. `params` must match the row's narrowing EXACTLY — " +
		"omit it to drop an un-narrowed row, and pass the same string to drop a narrowed one.",
	resreg.ActionList: "List acl rows for a folder (scope matches the folder). Audit what's permitted before changing.",
}

// ACLMCPArgs is the explicit per-action arg list. The agent face carries the exact
// wire shapes (principal/scope/action/effect; folder for list), NOT the RowType-
// reflected columns, so this overrides RowType reflection for the derived tools.
var ACLMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("add"): {
		{Name: "principal", Type: "string", Required: true},
		{Name: "scope", Type: "string", Required: true},
		{Name: "action", Type: "string"},
		{Name: "effect", Type: "string"},
		{Name: "params", Type: "string",
			Description: "narrow the row to matching call arguments: name=glob[,name=glob]"},
		{Name: "grant_option", Type: "bool",
			Description: "let the grantee re-grant this row to others; only grantable by a holder who has it"},
	},
	resreg.Action("remove"): {
		{Name: "principal", Type: "string", Required: true},
		{Name: "scope", Type: "string", Required: true},
		{Name: "action", Type: "string"},
		{Name: "effect", Type: "string"},
		{Name: "params", Type: "string",
			Description: "the row's exact narrowing; omit for an un-narrowed row"},
	},
	resreg.ActionList: {
		{Name: "folder", Type: "string", Required: true},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      ACLName,
		Table:     "acl",
		DB:        resreg.SubsystemRoutd,
		RowType:   reflect.TypeFor[ACLRow](),
		PKFields:  []string{"Principal", "Action", "Scope", "Params", "Predicate", "Effect"},
		Endpoints: ACLEndpoints,
		MCPDoc:    ACLMCPDoc,
		MCPArgs:   ACLMCPArgs,
		MCPNames:  ACLMCPNames,
		// No folder scope: acl.scope is a glob (`atlas/`, `**`), not column-
		// equal to a folder (spec 5/8 §"Schema-driven CRUD"/"FK posture").
		// Apply rebuilds acl wholesale; per-glob scoped delete is not v1.
		StampedFields: []string{"GrantedAt"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(ctx context.Context, tx *sql.Tx, row any) error {
				r := row.(*ACLRow)
				if r.Effect == "" {
					r.Effect = "allow"
				}
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				"GrantedBy": {
					Read:  "COALESCE(granted_by, '')",
					Write: nilIfEmptyString,
				},
			},
		},
	})
}
