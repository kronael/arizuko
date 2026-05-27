package resources

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/kronael/arizuko/resreg"
)

// ACLMembershipRow mirrors acl_membership: composite (child, parent)
// PK + nullable added_by. ValidateRow runs the recursive cycle check
// in-tx; the same query the imperative AddMembership uses, but the
// engine wires it on the apply path too so manifests can't introduce
// cycles silently.
type ACLMembershipRow struct {
	Child   string `db:"child"    yaml:"child"    json:"child"`
	Parent  string `db:"parent"   yaml:"parent"   json:"parent"`
	AddedBy string `db:"added_by" yaml:"added_by,omitempty" json:"added_by,omitempty"`
	AddedAt string `db:"added_at" yaml:"added_at,omitempty" json:"added_at,omitempty"`
}

var ErrMembershipCycle = errors.New("acl_membership: cycle")
var ErrMembershipSelf = errors.New("acl_membership: self")

func init() {
	resreg.Register(resreg.Resource{
		Name:     "acl_membership",
		Table:    "acl_membership",
		RowType:  reflect.TypeOf(ACLMembershipRow{}),
		PKFields: []string{"Child", "Parent"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(ctx context.Context, tx *sql.Tx, row any) error {
				r := row.(*ACLMembershipRow)
				if r.AddedAt == "" {
					r.AddedAt = time.Now().UTC().Format(time.RFC3339)
				}
				return nil
			},
			ValidateRow: func(ctx context.Context, tx *sql.Tx, row any) error {
				r := row.(*ACLMembershipRow)
				if r.Child == r.Parent {
					return ErrMembershipSelf
				}
				var hits int
				err := tx.QueryRowContext(ctx,
					`WITH RECURSIVE up(p) AS (
					   SELECT ? UNION
					   SELECT acl_membership.parent FROM acl_membership
					     JOIN up ON acl_membership.child = up.p
					 )
					 SELECT COUNT(*) FROM up WHERE p = ?`,
					r.Parent, r.Child,
				).Scan(&hits)
				if err != nil {
					return err
				}
				if hits > 0 {
					return ErrMembershipCycle
				}
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				"AddedBy": {
					Read:  "COALESCE(added_by, '')",
					Write: nilIfEmptyString,
				},
			},
		},
	})
}
