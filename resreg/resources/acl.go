package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// ACLRow mirrors the acl entity: the six-field grant is its coordinate and the
// other three are its attributes. EVERY column: apply rebuilds acl wholesale
// and a Put replaces the item's WHOLE attribute set, so a field this does not
// name is a value gone. `grant_option` (spec 5/33-paths-roles) missing from the row type
// dropped every holder's right to re-delegate on rebuild.
type ACLRow struct {
	Principal   string `db:"principal" yaml:"principal" json:"principal"`
	Action      string `db:"action"    yaml:"action"    json:"action"`
	Scope       string `db:"scope"     yaml:"scope"     json:"scope"`
	Effect      string `db:"effect"    yaml:"effect"    json:"effect,omitempty"`
	Params      string `db:"params"    yaml:"params"    json:"params,omitempty"`
	Predicate   string `db:"predicate" yaml:"predicate" json:"predicate,omitempty"`
	GrantOption bool   `db:"grant_option,bool" yaml:"grant_option,omitempty" json:"grant_option,omitempty"`
	GrantedBy   string `db:"granted_by" yaml:"granted_by,omitempty" json:"granted_by,omitempty"`
	GrantedAt   string `db:"granted_at" yaml:"granted_at,omitempty" json:"granted_at,omitempty"`
}

// ACLEndpoints is the single owner of the acl endpoint set. remove is a
// body-addressed DELETE (principal/scope in the body). list is MCPOnly:
// nothing mounts a REST twin, and declaring a Verb+Path for it advertised a
// GET /v1/acl that 404s (BUGS F27).
var ACLEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/acl", Action: resreg.Action("add")},
	{Verb: "DELETE", Path: "/v1/acl", Action: resreg.Action("remove")},
	{Action: resreg.ActionList, MCPOnly: true},
}

// ACLMCPNames maps each action to the flat tool name the live agent calls
// (spec 5/16).
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

// ACLMCPArgs is the agent face's wire shape, overriding RowType reflection.
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
		Name:     ACLName,
		Table:    "acl",
		DB:       resreg.SubsystemRoutd,
		RowType:  reflect.TypeFor[ACLRow](),
		PKFields: []string{"Principal", "Action", "Scope", "Params", "Predicate", "Effect"},
		// All six are needed: two rows differing only in `effect` are a grant
		// and a denial of the same call, and `params` and `predicate` each
		// narrow a row to a subset of it.
		EntitySortKey: []string{"Principal", "Action", "Scope", "Params", "Predicate", "Effect"},
		EntityKeySep:  store.KeySep,
		Endpoints:     ACLEndpoints,
		MCPDoc:        ACLMCPDoc,
		MCPArgs:       ACLMCPArgs,
		MCPNames:      ACLMCPNames,
		// No folder scope: acl.scope is a glob, not column-equal to a folder
		// (spec 5/8 §"FK posture"), so apply rebuilds acl wholesale.
		StampedFields: []string{"GrantedAt"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(row any) error {
				r := row.(*ACLRow)
				if r.Effect == "" {
					r.Effect = "allow"
				}
				return nil
			},
		},
	})
}
