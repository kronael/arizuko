package resources

import (
	"encoding/json"
	"reflect"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
)

// GroupsRow mirrors groups. `config` and `container_config` are raw JSON
// strings, not typed structs: they are operator-opaque, and decoding happens
// in the imperative store path, not on the manifest round-trip.
type GroupsRow struct {
	Folder             string `db:"folder"           yaml:"folder"           json:"folder"`
	AddedAt            string `db:"added_at"         yaml:"added_at,omitempty"     json:"added_at,omitempty"`
	ContainerConfig    string `db:"container_config" yaml:"container_config,omitempty" json:"container_config,omitempty"`
	Product            string `db:"product"          yaml:"product"          json:"product"`
	Config             string `db:"config"           yaml:"config,omitempty" json:"config,omitempty"`
	UpdatedAt          string `db:"updated_at"       yaml:"updated_at,omitempty"   json:"updated_at,omitempty"`
	CostCapCentsPerDay int    `db:"cost_cap_cents_per_day" yaml:"cost_cap_cents_per_day,omitempty" json:"cost_cap_cents_per_day,omitempty"`
}

// GroupsAgentEndpoints is the single owner of the groups endpoint set.
// register is MCPOnly: a custom verb with FS side-effects that routd mounts
// as a FORWARDER for the agent only; group CREATION stays dashd's SetupGroup,
// so no daemon serves POST /v1/groups (BUGS F27).
var GroupsAgentEndpoints = []resreg.Endpoint{
	{Action: resreg.Action("register"), MCPOnly: true},
	{Verb: "GET", Path: "/v1/groups", Action: resreg.ActionList},
	{Verb: "GET", Path: "/v1/groups/{folder}", Action: resreg.ActionGet},
}

// GroupsFolderRef is what the list answers with: the folder and nothing else.
// Documenting the list off the row promised columns no caller receives (BUGS
// L120).
type GroupsFolderRef struct {
	Folder string `json:"folder"`
}

// GroupsResponses declares the list projection and the group detail.
var GroupsResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]GroupsFolderRef](),
	resreg.ActionGet:  reflect.TypeFor[core.Group](),
}

// GroupsMCPNames maps each agent action to the flat tool name the live agent
// calls (spec 5/16).
var GroupsMCPNames = map[resreg.Action]string{
	resreg.Action("register"): "register_group",
	resreg.ActionList:         "refresh_groups",
	resreg.ActionGet:          "get_group",
}

// GroupsMCPDoc is the single owner of the group tools' agent-facing one-liners.
// Copy verbatim — the agent wire contract.
var GroupsMCPDoc = map[resreg.Action]string{
	resreg.Action("register"): "Create a child agent group at `folder` and route `jid` to it — this is the ONLY way a group comes into existence; nothing auto-creates one. Pass the full path (e.g. folder=\"acme/support\") and the chat jid that should land there. Use when onboarding a new chat into its own isolated workspace/session, or before delegating to a child that does not exist yet. Not for promoting work up (escalate_group) or handing a task to an existing child (delegate_group).",
	resreg.ActionList:         "Return folder for every registered group. Use to discover delegation targets or audit the group tree. Not for routing details (inspect_routing) or per-group tasks (inspect_tasks).",
	resreg.ActionGet:          "Read one group's settings by folder.",
}

// GroupsMCPArgs is the agent face's wire shape, overriding RowType reflection.
var GroupsMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionGet: {{Name: "folder", Type: "string", Required: true}},
	resreg.Action("register"): {
		{Name: "folder", Type: "string", Required: true},
		{Name: "jid", Type: "string", Required: true},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          GroupsName,
		Table:         "groups",
		DB:            resreg.SubsystemRoutd,
		RowType:       reflect.TypeFor[GroupsRow](),
		PKFields:      []string{"Folder"},
		EntitySortKey: []string{"Folder"},
		Scope:         resreg.ScopeSpec{Field: "Folder"},
		StampedFields: []string{"AddedAt", "UpdatedAt"},
		// `updated_at` is server-OWNED and never server-INVENTED: a group nobody
		// has changed carries none, and stamping one moves a checksum the
		// operator did not cause (BUGS F49).
		AbsentStamps: []string{"UpdatedAt"},
		// Agent-face metadata (spec 5/16); Apply/Export ignore it.
		Endpoints: GroupsAgentEndpoints,
		Responses: GroupsResponses,
		MCPDoc:    GroupsMCPDoc,
		MCPArgs:   GroupsMCPArgs,
		MCPNames:  GroupsMCPNames,
		Hooks: resreg.Hooks{
			BeforeInsert: func(row any) error {
				r := row.(*GroupsRow)
				if r.Product == "" {
					r.Product = "assistant"
				}
				// Canonical key order, for deterministic emit.
				r.ContainerConfig = canonJSON(r.ContainerConfig)
				r.Config = canonJSON(r.Config)
				return nil
			},
		},
	})
}

// canonJSON re-marshals a JSON object string so its key order is canonical.
// Empty or unparseable input passes through unchanged.
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
