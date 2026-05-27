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

func init() {
	resreg.Register(resreg.Resource{
		Name:     "web_routes",
		Table:    "web_routes",
		RowType:  reflect.TypeOf(WebRoutesRow{}),
		PKFields: []string{"PathPrefix"},
		Scope:    resreg.ScopeSpec{Field: "Folder"},
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
