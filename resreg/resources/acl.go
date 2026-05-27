package resources

import (
	"context"
	"database/sql"
	"reflect"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// ACLRow mirrors the acl table: full composite PK + nullable granted_by.
// `granted_by` is read via COALESCE so the Go field can stay `string`
// (empty = NULL); same shape as existing store/acl.go scanACLRow.
type ACLRow struct {
	Principal string `db:"principal" yaml:"principal" json:"principal"`
	Action    string `db:"action"    yaml:"action"    json:"action"`
	Scope     string `db:"scope"     yaml:"scope"     json:"scope"`
	Effect    string `db:"effect"    yaml:"effect"    json:"effect,omitempty"`
	Params    string `db:"params"    yaml:"params"    json:"params,omitempty"`
	Predicate string `db:"predicate" yaml:"predicate" json:"predicate,omitempty"`
	GrantedBy string `db:"granted_by" yaml:"granted_by,omitempty" json:"granted_by,omitempty"`
	GrantedAt string `db:"granted_at" yaml:"granted_at,omitempty" json:"granted_at,omitempty"`
}

func init() {
	resreg.Register(resreg.Resource{
		Name:     "acl",
		Table:    "acl",
		RowType:  reflect.TypeOf(ACLRow{}),
		PKFields: []string{"Principal", "Action", "Scope", "Params", "Predicate", "Effect"},
		Scope:    resreg.ScopeSpec{Field: "Scope"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(ctx context.Context, tx *sql.Tx, row any) error {
				r := row.(*ACLRow)
				if r.Effect == "" {
					r.Effect = "allow"
				}
				if r.GrantedAt == "" {
					r.GrantedAt = time.Now().UTC().Format(time.RFC3339)
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
