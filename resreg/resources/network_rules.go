package resources

import (
	"reflect"

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
// references it).
//
// All three are MCPOnly: network_rules is reached ONLY through the agent's
// network_allow/network_deny/network_list tools — no daemon mounts a REST face
// for it and none advertises it. They carried Verb+Path that nothing served,
// which is the F21/F27 class one step further along (declared, unmounted,
// unadvertised); MCPOnly is how spec 5/17 says to spell "no REST twin".
var NetworkRulesEndpoints = []resreg.Endpoint{
	{Action: resreg.Action("allow"), MCPOnly: true},
	{Action: resreg.Action("deny"), MCPOnly: true},
	{Action: resreg.ActionList, MCPOnly: true},
}

// NetworkRulesMCPNames maps each action to the flat tool name the live agent
// already calls; routd's network_rules_resource.go references it (agent socket
// derivation) and ipc.ListTools reads it via the registry walk. Spec 5/16.
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
		DB:            resreg.SubsystemRoutd,
		RowType:       reflect.TypeFor[NetworkRulesRow](),
		PKFields:      []string{"Folder", "Target"},
		Endpoints:     NetworkRulesEndpoints,
		MCPDoc:        NetworkRulesMCPDoc,
		MCPArgs:       NetworkRulesMCPArgs,
		MCPNames:      NetworkRulesMCPNames,
		Scope:         resreg.ScopeSpec{Field: "Folder"},
		StampedFields: []string{"CreatedAt"},
	})
}
