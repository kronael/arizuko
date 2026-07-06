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

func init() {
	resreg.Register(resreg.Resource{
		Name:          "network_rules",
		Table:         "network_rules",
		RowType:       reflect.TypeOf(NetworkRulesRow{}),
		PKFields:      []string{"Folder", "Target"},
		Endpoints:     NetworkRulesEndpoints,
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
