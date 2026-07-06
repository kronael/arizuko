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

// ACLEndpoints is the single owner of the acl REST endpoint set: routd's acl
// tools (acl_resource.go) reference it. add is a POST and remove a body-addressed
// DELETE on the collection (principal/scope in the body, no {pk} path), so the
// real faces diverge from the composite-PK-CRUD convention. list_acl has no REST
// twin — mountACL trims Endpoints to add/remove for the operator face.
var ACLEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/acl", Action: resreg.Action("add")},
	{Verb: "DELETE", Path: "/v1/acl", Action: resreg.Action("remove")},
	{Verb: "GET", Path: "/v1/acl", Action: resreg.ActionList},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "acl",
		Table:     "acl",
		RowType:   reflect.TypeOf(ACLRow{}),
		PKFields:  []string{"Principal", "Action", "Scope", "Params", "Predicate", "Effect"},
		Endpoints: ACLEndpoints,
		// No folder scope: acl.scope is a glob (`atlas/`, `**`), not column-
		// equal to a folder (spec 5/36 §"Schema-driven CRUD"/"FK posture").
		// Apply rebuilds acl wholesale; per-glob scoped delete is not v1.
		StampedFields: []string{"GrantedAt"},
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
