package resources

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// ACLMembershipRow mirrors acl_membership: composite (child, parent)
// PK + added_by, which every writer sets. ValidateRow runs the recursive cycle check
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
		Name:          ACLMembershipName,
		Table:         "acl_membership",
		DB:            resreg.SubsystemRoutd,
		RowType:       reflect.TypeFor[ACLMembershipRow](),
		PKFields:      []string{"Child", "Parent"},
		StampedFields: []string{"AddedAt"},
		Endpoints:     ACLMembershipEndpoints,
		MCPDoc:        ACLMembershipMCPDoc,
		MCPArgs:       ACLMembershipMCPArgs,
		MCPNames:      ACLMembershipMCPNames,
		// CRITICAL (spec 5/8 §"Cross-subsystem apply"): pairing edges are
		// consented runtime state made and undone through
		// issue_pairing_link/unpair (spec 5/31), never declarative operator
		// intent — they must stay OUT of every manifest-visible read AND
		// write: the content-hash checksum, the CAS-scoped DELETE+INSERT
		// rebuild, and (once built) the pre-image/rollback set. Without this,
		// rollback re-applying a pre-image taken before a caller unpaired
		// would silently resurrect authority they deliberately revoked.
		//
		// The predicate is `added_by <> 'pairing'` and not `IS NULL OR <>`,
		// because the same declaration has to narrow the read AND both deletes on
		// both shapes, and an object-path filter is a conjunction of attribute
		// comparisons with no OR (objstore.Query.Where). The two forms answer
		// alike for every row that carries a value; `added_by` is NOT NULL from
		// routd migration 0047, so every row does.
		RowFilter: []objstore.Cmp{{Attr: "added_by", Op: objstore.Ne, Value: "pairing"}},
		Hooks: resreg.Hooks{
			ValidateRow: func(ctx context.Context, tx *sql.Tx, row any) error {
				r := row.(*ACLMembershipRow)
				if r.Child == r.Parent {
					return ErrMembershipSelf
				}
				cycles, err := store.MembershipWouldCycle(ctx, tx, r.Child, r.Parent)
				if err != nil {
					return err
				}
				if cycles {
					return ErrMembershipCycle
				}
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				// Read only. The write half stored an empty actor as NULL, and
				// NULL is the one value the row filter above cannot compare —
				// a row the writer left unattributed would fall out of its own
				// resource's projection. routd.db has no such row (migration
				// 0047); the COALESCE stays for the frozen pre-split chain,
				// which store's tests still build.
				"AddedBy": {Read: "COALESCE(added_by, '')"},
			},
		},
	})
}
