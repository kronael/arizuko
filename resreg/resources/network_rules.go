package resources

import (
	"context"
	"database/sql"
	"reflect"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// NetworkRulesRow mirrors network_rules: composite (folder, target) PK.
// Global rules use folder="" (seeded by migration 0037).
type NetworkRulesRow struct {
	Folder    string `db:"folder"     yaml:"folder"     json:"folder"`
	Target    string `db:"target"     yaml:"target"     json:"target"`
	CreatedAt string `db:"created_at" yaml:"created_at,omitempty" json:"created_at,omitempty"`
	CreatedBy string `db:"created_by" yaml:"created_by,omitempty" json:"created_by,omitempty"`
}

// NetworkRulesEndpoints is the single owner of the network_rules endpoint set
// that drives the agent's egress tools (routd network_rules_resource.go
// references it). allow is a POST and deny a body-addressed DELETE (host/folder
// in the body, no {pk} path), so the real faces diverge from the composite-PK-
// CRUD convention. network_rules is agent-MCP-only (no daemon advertises it).
var NetworkRulesEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/network_rules", Action: resreg.Action("allow")},
	{Verb: "DELETE", Path: "/v1/network_rules", Action: resreg.Action("deny")},
	{Verb: "GET", Path: "/v1/network_rules", Action: resreg.ActionList},
}

// NetworkRulesMCPNames maps each action to the flat tool name the live agent
// already calls; routd's network_rules_resource.go references it (agent socket
// derivation) and ipc.ListTools reads it via the registry walk. Spec 5/44.
var NetworkRulesMCPNames = map[resreg.Action]string{
	resreg.Action("allow"): "network_allow",
	resreg.Action("deny"):  "network_deny",
	resreg.ActionList:      "network_list",
}

// NetworkRulesMCPDoc is the single owner of the egress tools' agent-facing
// one-liners. Copy verbatim — the agent wire contract.
var NetworkRulesMCPDoc = map[resreg.Action]string{
	resreg.Action("allow"): "Open egress to `host` for `folder` and every descendant by appending an allowlist rule. " +
		"Use when an agent needs to reach a host the default-deny proxy blocks (e.g. a vendor API). " +
		"`host` is a bare domain (no scheme/port), e.g. 'example.com', or a `*.example.com` subdomain glob. " +
		"`folder` is the target folder — your own folder (the default when omitted) or any folder in your subtree, " +
		"e.g. network_allow(folder='atlas/search', host='krons.fiu.wtf'). A rule at a parent folder cascades to all children.",
	resreg.Action("deny"): "Remove an egress allowlist rule: close access to `host` for `folder`. " +
		"`folder` defaults to your own folder and may be any folder in your subtree. " +
		"Only drops a rule set on `folder` itself; a host inherited from an ancestor must be removed at that ancestor.",
	resreg.ActionList: "List the egress allowlist for THIS folder: `resolved` is every host the folder can reach " +
		"(its own rules plus those inherited from ancestors and the instance base); `own` is only the rules set on this folder itself.",
}

// NetworkRulesMCPArgs is the explicit per-action arg list. The agent face carries
// {host, folder}, NOT the RowType-reflected columns, so this overrides RowType
// reflection for the derived agent/browser tools.
var NetworkRulesMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("allow"): {{Name: "host", Type: "string", Required: true}, {Name: "folder", Type: "string"}},
	resreg.Action("deny"):  {{Name: "host", Type: "string", Required: true}, {Name: "folder", Type: "string"}},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          "network_rules",
		Table:         "network_rules",
		RowType:       reflect.TypeOf(NetworkRulesRow{}),
		PKFields:      []string{"Folder", "Target"},
		Endpoints:     NetworkRulesEndpoints,
		MCPDoc:        NetworkRulesMCPDoc,
		MCPArgs:       NetworkRulesMCPArgs,
		MCPNames:      NetworkRulesMCPNames,
		Scope:         resreg.ScopeSpec{Field: "Folder"},
		StampedFields: []string{"CreatedAt"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(ctx context.Context, tx *sql.Tx, row any) error {
				r := row.(*NetworkRulesRow)
				if r.CreatedAt == "" {
					r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
				}
				return nil
			},
		},
	})
}
