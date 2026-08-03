package resources

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

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

// The resource carries exactly ONE action (spec 5/31 § Unpair): delete, scoped
// to added_by='pairing' so it can never reach role membership. Adding an edge
// stays a consented redemption or a manifest apply — there is no add face here.
// The DELETE is body-addressed because the PK is composite (child, parent).
var ACLMembershipEndpoints = []resreg.Endpoint{
	{Verb: "DELETE", Path: "/v1/acl_membership", Action: resreg.ActionDelete},
}

// ACLMembershipMCPNames keeps the agent-facing verb flat and honest: `unpair` is
// what it does, and it is the exact inverse of issue_pairing_link.
var ACLMembershipMCPNames = map[resreg.Action]string{
	resreg.ActionDelete: "unpair",
}

var ACLMembershipMCPDoc = map[resreg.Action]string{
	resreg.ActionDelete: "Undo a pairing: stop a channel identity from acting as " +
		"the account it was linked to. Both endpoints of the link may call this — " +
		"the agent handling that chat, or the account holder over REST. It only " +
		"touches links made by pairing, never role membership, and it cannot add " +
		"authority (there is no inverse verb). Takes effect on the identity's next " +
		"tool call. Spec 5/31.",
}

var ACLMembershipMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionDelete: {
		{Name: "child", Type: "string", Required: true,
			Description: "The channel identity to unlink (e.g. telegram:user/123). It must route to your folder."},
		{Name: "parent", Type: "string", Required: true,
			Description: "The account it currently acts as (e.g. google:alice)."},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          "acl_membership",
		Table:         "acl_membership",
		RowType:       reflect.TypeFor[ACLMembershipRow](),
		PKFields:      []string{"Child", "Parent"},
		StampedFields: []string{"AddedAt"},
		Endpoints:     ACLMembershipEndpoints,
		MCPDoc:        ACLMembershipMCPDoc,
		MCPArgs:       ACLMembershipMCPArgs,
		MCPNames:      ACLMembershipMCPNames,
		Hooks: resreg.Hooks{
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
