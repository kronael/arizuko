package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// GroupsRow mirrors groups. Per-group behavioral settings (model,
// thread_replies, observe_window_*, open) live inside the `config` JSON
// blob; `container_config` carries spawn shape. Both are modeled as raw
// JSON strings rather than typed structs because they are operator-opaque
// and the engine's job is shape, not semantics — decoding happens in the
// imperative store path, not on the manifest round-trip.
type GroupsRow struct {
	Folder             string `db:"folder"           yaml:"folder"           json:"folder"`
	AddedAt            string `db:"added_at"         yaml:"added_at,omitempty"     json:"added_at,omitempty"`
	ContainerConfig    string `db:"container_config" yaml:"container_config,omitempty" json:"container_config,omitempty"`
	Product            string `db:"product"          yaml:"product"          json:"product"`
	Config             string `db:"config"           yaml:"config,omitempty" json:"config,omitempty"`
	UpdatedAt          string `db:"updated_at"       yaml:"updated_at,omitempty"   json:"updated_at,omitempty"`
	CostCapCentsPerDay int    `db:"cost_cap_cents_per_day" yaml:"cost_cap_cents_per_day,omitempty" json:"cost_cap_cents_per_day,omitempty"`
}

// GroupsAgentEndpoints drives the agent's group-management tools (register_group +
// refresh_groups) via routd's groups_resource.go — the spec 5/44 agent-face fold.
// register is a custom verb with FS side-effects (a group row, a room route, and a
// git-init'd group dir), so routd mounts it as a FORWARDER (no engine tx); these
// paths only drive deriveMCPTools + the facade browser. groups is dashd-FS-managed,
// never a routd REST resource (routd/cmd/routd/main.go's OpenAPI list omits it), so
// no daemon advertises these paths in /openapi.json.
var GroupsAgentEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/groups", Action: resreg.Action("register")},
	{Verb: "GET", Path: "/v1/groups", Action: resreg.ActionList},
}

// GroupsMCPNames maps each agent action to the flat tool name the live agent already
// calls; routd's groups_resource.go references it (agent socket derivation) and
// ipc.ListTools reads it via the registry walk (MCPNames is the facade-tool
// discriminator, so setting it here is what surfaces groups in the browser). Spec 5/44.
var GroupsMCPNames = map[resreg.Action]string{
	resreg.Action("register"): "register_group",
	resreg.ActionList:         "refresh_groups",
}

// GroupsMCPDoc is the single owner of the group tools' agent-facing one-liners.
// Copy verbatim — the agent wire contract.
var GroupsMCPDoc = map[resreg.Action]string{
	resreg.Action("register"): "Create a child agent group and route a jid to it. Use when onboarding a new chat into its own isolated workspace/session, or when spinning up a sub-agent from this group's prototype/ (fromPrototype=true). Not for promoting work up (escalate_group) or handing a task to an existing child (delegate_group).",
	resreg.ActionList:         "Return folder for every registered group. Use to discover delegation targets or audit the group tree. Not for routing details (inspect_routing) or per-group tasks (inspect_tasks).",
}

// GroupsMCPArgs is the explicit per-action arg list for the agent face — register's
// {jid, fromPrototype, folder} shape, NOT the RowType columns, so it overrides
// RowType reflection for the derived tool. refresh_groups (list) takes no args.
var GroupsMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("register"): {
		{Name: "jid", Type: "string", Required: true},
		{Name: "fromPrototype", Type: "bool"},
		{Name: "folder", Type: "string"},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:     "groups",
		Table:    "groups",
		RowType:  reflect.TypeOf(GroupsRow{}),
		PKFields:      []string{"Folder"},
		Scope:         resreg.ScopeSpec{Field: "Folder"},
		StampedFields: []string{"AddedAt", "UpdatedAt"},
		// Agent-face metadata (spec 5/44): drives deriveMCPTools for register_group +
		// refresh_groups. The manifest CRUD path uses RowType/Table only; these are
		// inert to it (Apply/Export ignore Endpoints/MCP*).
		Endpoints: GroupsAgentEndpoints,
		MCPDoc:    GroupsMCPDoc,
		MCPArgs:   GroupsMCPArgs,
		MCPNames:  GroupsMCPNames,
		Hooks: resreg.Hooks{
			BeforeInsert: func(_ context.Context, _ *sql.Tx, row any) error {
				r := row.(*GroupsRow)
				if r.Product == "" {
					r.Product = "assistant"
				}
				now := time.Now().UTC().Format(time.RFC3339)
				if r.AddedAt == "" {
					r.AddedAt = now
				}
				if r.UpdatedAt == "" {
					r.UpdatedAt = now
				}
				// Normalize the JSON blobs for deterministic emit: parse →
				// re-marshal so key order is canonical. Cheap; runs only on apply.
				r.ContainerConfig = canonJSON(r.ContainerConfig)
				r.Config = canonJSON(r.Config)
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				"Config": {
					Read:  "COALESCE(config, '')",
					Write: nilIfEmptyString,
				},
				"ContainerConfig": {
					Read:  "COALESCE(container_config, '')",
					Write: nilIfEmptyString,
				},
				"UpdatedAt": {
					Read:  "COALESCE(updated_at, '')",
					Write: nilIfEmptyString,
				},
			},
		},
	})
}

// canonJSON re-marshals a JSON object string so its key order is canonical,
// for deterministic manifest emit. Empty or unparseable input passes through
// unchanged (legacy/empty rows stay as-is).
func canonJSON(s string) string {
	if s == "" {
		return s
	}
	var v map[string]any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(b)
}
