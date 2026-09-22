package resources

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// ACLMembershipRow mirrors acl_membership: composite (child, parent) PK.
// ValidateRows runs the same cycle walk the imperative AddMembership runs, so
// a manifest cannot introduce a cycle silently.
type ACLMembershipRow struct {
	Child   string `db:"child"    yaml:"child"    json:"child"`
	Parent  string `db:"parent"   yaml:"parent"   json:"parent"`
	AddedBy string `db:"added_by" yaml:"added_by,omitempty" json:"added_by,omitempty"`
	AddedAt string `db:"added_at" yaml:"added_at,omitempty" json:"added_at,omitempty"`
}

var ErrMembershipCycle = errors.New("acl_membership: cycle")
var ErrMembershipSelf = errors.New("acl_membership: self")

// The resource carries exactly ONE action (spec 5/31 § Unpair): delete,
// scoped to added_by='pairing' so it can never reach role membership. There
// is no add face. The DELETE is body-addressed because the PK is composite.
var ACLMembershipEndpoints = []resreg.Endpoint{
	{Verb: "DELETE", Path: "/v1/acl_membership", Action: resreg.ActionDelete},
}

// ACLMembershipMCPNames: `unpair` is the exact inverse of issue_pairing_link.
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
		EntitySortKey: []string{"Child", "Parent"},
		EntityKeySep:  store.KeySep,
		StampedFields: []string{"AddedAt"},
		Endpoints:     ACLMembershipEndpoints,
		MCPDoc:        ACLMembershipMCPDoc,
		MCPArgs:       ACLMembershipMCPArgs,
		MCPNames:      ACLMembershipMCPNames,
		// CRITICAL (spec 5/8 §"Cross-subsystem apply"): pairing edges are
		// consented runtime state (spec 5/31), never operator intent, and must
		// stay OUT of every manifest-visible read AND write, or a rollback
		// would silently resurrect authority a caller deliberately revoked.
		// Ne, not `IS NULL OR <>`: a filter has no OR, and every row carries
		// added_by.
		RowFilter: []objstore.Cmp{{Attr: "added_by", Op: objstore.Ne, Value: "pairing"}},
		Hooks:     resreg.Hooks{ValidateRows: noMembershipCycle},
	})
}

// noMembershipCycle refuses a manifest whose edges make a principal its own
// ancestor: the same walk store.AddMembership runs (objstore.Reachable), over
// the MANIFEST's graph alone, since an apply wipes the projection before it
// writes. The pairing edges the RowFilter excludes are outside this graph by
// the same declaration (spec 5/31). It runs BEFORE the wipe; an apply has no
// rollback.
func noMembershipCycle(ctx context.Context, rows any) error {
	edges, ok := rows.([]ACLMembershipRow)
	if !ok {
		return fmt.Errorf("acl_membership rows are %T, not %T", rows, []ACLMembershipRow{})
	}
	up := make(map[string][]string, len(edges))
	for _, e := range edges {
		if e.Child == e.Parent {
			return fmt.Errorf("%w: %s", ErrMembershipSelf, e.Child)
		}
		up[e.Child] = append(up[e.Child], e.Parent)
	}
	parents := func(_ context.Context, node string) ([]string, error) { return up[node], nil }
	for _, e := range edges {
		hit, err := objstore.Reachable(ctx, parents, e.Parent, e.Child)
		if err != nil {
			return err
		}
		if hit {
			return fmt.Errorf("%w: %s is already at or above %s",
				ErrMembershipCycle, e.Child, e.Parent)
		}
	}
	return nil
}
