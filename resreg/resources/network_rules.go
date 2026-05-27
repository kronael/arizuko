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

func init() {
	resreg.Register(resreg.Resource{
		Name:     "network_rules",
		Table:    "network_rules",
		RowType:  reflect.TypeOf(NetworkRulesRow{}),
		PKFields: []string{"Folder", "Target"},
		Scope:    resreg.ScopeSpec{Field: "Folder"},
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
