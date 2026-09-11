package store

// membership.go holds every read and write of `acl_membership`: one item per
// edge, addressed by (child, parent).
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// acl_membership is a registered resreg resource, and an entity-backed
// resource's items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd) — a second spelling puts this file's items where the
// engine's read does not look, and a query on an empty partition is a legal
// query that answers nothing.
//
// THE CHILD LEADS THE COORDINATE because that is the direction the graph is
// walked. Every ancestor walk asks "which nodes sit directly above this one",
// which is a begins_with on `child + KeySep` — exact, because no value may
// carry the separator. Members(parent) asks the other direction and is a
// partition read with a filter: 58 edges on krons, 13 and 12 on the other two.
//
// THE CYCLE GUARD IS objstore.AddEdge WHERE THIS PACKAGE OWNS THE WRITE. A
// Check binds one item and a cycle test is a condition on a graph frontier, so
// two inserts closing a ring from opposite ends each walk a graph without the
// other's edge and both commit; the marker's compare-and-swap is what
// serialises them (objstore/reach.go, BUGS L256). The one writer that cannot
// use it is the pairing redemption, which must consume its token in the SAME
// transaction as the edge: it walks and writes inside that transaction, where
// SQLite's single writer lock is the serialisation.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// ErrCycle: adding the edge would create a cycle in acl_membership.
var ErrCycle = errors.New("acl_membership: cycle")

// ErrSelfMembership: child == parent.
var ErrSelfMembership = errors.New("acl_membership: self")

// The attributes an edge item carries. `child` and `parent` are NOT among them:
// they are the coordinate.
//
// `added_by` is ALWAYS written, empty string included, because it is the
// resource's own row filter — acl_membership excludes the edges pairing
// consented to (`added_by <> 'pairing'`) from the manifest-visible projection,
// and an ABSENT attribute answers no comparison. An edge that left it off would
// write itself out of its own projection: invisible to export, and untouched by
// the apply that should replace it.
const (
	attrMemAddedBy = "added_by"
	attrMemAddedAt = "added_at"
)

// membershipKey is the coordinate of one edge. A value carrying the separator
// is refused rather than joined — two different edges would address one item,
// and the read that splits the key apart would answer an edge neither states.
func membershipKey(child, parent string) (objstore.Key, error) {
	if child == "" || parent == "" {
		return objstore.Key{}, fmt.Errorf("membership edge carries child=%q parent=%q; "+
			"an edge with an empty end joins nothing", child, parent)
	}
	for name, v := range map[string]string{"child": child, "parent": parent} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("membership edge %s=%q carries the byte that "+
				"separates the two fields of its coordinate", name, v)
		}
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: child + KeySep + parent}, nil
}

// membershipEdgeOf splits one item's key back into the edge it names. A key
// that does not split is a LOUD fault: half a coordinate names an edge nobody
// wrote, and an authorize walk reading it would confer authority nobody granted.
func membershipEdgeOf(it objstore.Item) (child, parent string, err error) {
	child, parent, ok := strings.Cut(it.Key.Sort, KeySep)
	if !ok {
		return "", "", fmt.Errorf("membership item %q does not split into (child, parent)",
			it.Key.Sort)
	}
	return child, parent, nil
}

// membershipPut renders the create-if-absent write every edge writer issues.
// Create-if-absent is what `INSERT OR IGNORE` said: re-adding an edge that is
// there must not restamp who added it or when.
func membershipPut(child, parent, addedBy string) (objstore.Write, error) {
	key, err := membershipKey(child, parent)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Put, Entity: ACLMembership, Key: key,
		Attrs: map[string]string{
			attrMemAddedBy: addedBy,
			attrMemAddedAt: core.NowTS(),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}, nil
}

// MembershipParents answers which principals sit directly above one — the
// callback objstore.Reachable walks (objstore/reach.go).
//
// It is EXPORTED because the same guard decides four writes, and it was four
// copies of one recursive CTE: this package's two edge writers, routd's
// agent-facing add, and the manifest apply's per-row validation. A walk written
// four times is a walk that can disagree with itself (BUGS L256).
//
// It reads through the store's own driver rather than taking a handle, because
// an item read needs no transaction: the guard that makes the walk and the
// write one decision is the marker, not a snapshot.
func MembershipParents(q EntityQuerier) objstore.ParentsFunc {
	return func(ctx context.Context, node string) ([]string, error) {
		items, err := q.Query(ctx, objstore.Query{
			Entity: ACLMembership, Partition: OwnerRoutd,
			Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: node + KeySep},
		})
		if err != nil {
			return nil, fmt.Errorf("read the parents of %q: %w", node, err)
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			_, parent, err := membershipEdgeOf(it)
			if err != nil {
				return nil, err
			}
			out = append(out, parent)
		}
		return out, nil
	}
}

// MembershipWouldCycle reports whether the edge (child → parent) would make
// child its own ancestor.
//
// The walk starts at parent and asks whether child is already at or above it,
// which is what the recursive CTE asked. Reachable seeds with the node itself,
// so a self edge answers true here too — the callers still check it first,
// because ErrSelfMembership is a different answer for the operator.
func MembershipWouldCycle(ctx context.Context, q EntityQuerier, child, parent string) (bool, error) {
	return objstore.Reachable(ctx, MembershipParents(q), parent, child)
}

// AddMembership writes (child → parent) idempotently, unless the edge would
// close a cycle, and records it.
//
// The walk and the write are ONE decision: objstore.AddEdge reads the scope's
// version, walks, and compare-and-swaps that version in the batch that carries
// the edge, so an edge cannot land on a graph that moved after it was walked.
// The audit row rides in the same batch, which is what keeps it from outliving
// a refused write.
func (s *Store) AddMembership(child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	edge, err := membershipPut(child, parent, addedBy)
	if err != nil {
		return err
	}
	// addedBy is the grantor recorded in the row itself; it stands in as the
	// actor for callers that never set AsUser/AsCLI — the fallback AddACLRow
	// already uses, so the two acl writers render one identity.
	actor, actorSub, surface := s.auditIdentity()
	if s.auditSub == "" && addedBy != "" {
		actor, actorSub = addedBy, addedBy
	}
	_, w := audit.WriteFor(audit.Event{
		Category: audit.CategoryAuthZ,
		Action:   "membership.add",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: "acl_membership/" + child + "/" + parent,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"child":  child,
			"parent": parent,
		},
	})
	return s.addEdge(child, parent, edge, w)
}

// addEdge is the guarded write both audited and unaudited edge writers issue.
//
// It does NOT go through writeEntities, and that is not a way round the marker:
// objstore.AddEdge writes the marker itself, as a compare-and-swap rather than
// a bump, because the walk that decided this write has to be invalidated by any
// graph move — which a plain bump would not catch (objstore/reach.go).
func (s *Store) addEdge(child, parent string, writes ...objstore.Write) error {
	ctx := context.Background()
	_, err := objstore.AddEdge(ctx, s.Obj(), OwnerScope(OwnerRoutd),
		MembershipParents(s.Obj()), child, parent, writes)
	if errors.Is(err, objstore.ErrCycle) {
		return ErrCycle
	}
	return err
}

func (s *Store) RemoveMembership(child, parent string) error {
	key, err := membershipKey(child, parent)
	if err != nil {
		return err
	}
	actor, actorSub, surface := s.auditIdentity()
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		if err := s.WriteEntitiesInTx(context.Background(), tx, nil, objstore.Write{
			Op: objstore.Delete, Entity: ACLMembership, Key: key,
		}); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{
			Category: audit.CategoryAuthZ,
			Action:   "membership.remove",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "acl_membership/" + child + "/" + parent,
			Outcome:  audit.OutcomeOK,
		}, nil
	})
}

// RoleMember is the seeded floor role — the 12 messaging verbs, seeded by
// store.SeedRoutdFloor. Every group is born a member of it (5/33): power above
// the floor is explicit delegation, never derived from path depth.
const RoleMember = "role:member"

// SeedMemberRoleTx binds a new folder to RoleMember on a caller-owned
// transaction. It is the ONE writer of the create-time edge, because the
// invariant is "a group row implies the member edge" and it used to live in
// only one of four creating writers: the CLI, dashd and onbod each inserted a
// groups row without it, and a folder with no edge expands to nothing, matches
// zero allow rows and comes up with no messaging tools at all (BUGS L119).
// Idempotent — the underlying write is create-if-absent.
func SeedMemberRoleTx(ctx context.Context, s *Store, tx *sql.Tx, folder string) error {
	return s.PutMembershipTx(ctx, tx, "folder:"+folder, RoleMember, "system:4r-member")
}

// PutMembership writes (child → parent) idempotently WITHOUT emitting an
// audit_log row — the audit-free twin of AddMembership for an edge no operator
// asked for: the role:member seed every group creation writes. Same self/cycle
// rejection, and the same marker compare-and-swap.
func (s *Store) PutMembership(child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	edge, err := membershipPut(child, parent, addedBy)
	if err != nil {
		return err
	}
	return s.addEdge(child, parent, edge)
}

// PutMembershipTx is the edge write on a transaction the CALLER owns, for the
// writers that have to commit something else with it: pairing redemption spends
// its token in the same commit, a group creation seeds the member edge with the
// groups row, and routd's `/v1/acl` face lands the operator edge with
// resreg.invoke's audit row. AddMembership opens no transaction to join.
//
// Its cycle guard is the same walk, run through the store's own driver. What it
// gives up against AddMembership is the marker's compare-and-swap, so two edges
// closing a ring from opposite ends are serialised by the engine's write lock
// rather than by the version — which is what SQLite gives and Postgres at READ
// COMMITTED does not (BUGS L256).
func (s *Store) PutMembershipTx(ctx context.Context, tx *sql.Tx, child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	// The walk reads through the CALLER'S transaction: this body has already
	// written into it in some callers, and a read on another connection would both
	// miss those writes and wait on the write lock they hold (scopes.go reads).
	cycles, err := MembershipWouldCycle(ctx, s.reads(tx), child, parent)
	if err != nil {
		return err
	}
	if cycles {
		return ErrCycle
	}
	w, err := membershipPut(child, parent, addedBy)
	if err != nil {
		return err
	}
	// A refusal is the edge already being there (membershipPut is create-if-absent),
	// which must not restamp who added it or when.
	_, err = s.TryWriteEntitiesInTx(ctx, tx, nil, w)
	return err
}

// MembershipEdge is one edge with the two attributes the item carries: who
// wrote it, and when. It is the shape the readers that need MORE than the two
// ends take — onbod's account page prints the instant a chat was claimed, and
// its admission tick asks which parent PAIRING bound a jid to.
type MembershipEdge struct {
	Child   string
	Parent  string
	AddedBy string
	AddedAt string
}

// edgesFrom turns items into edges. One unreadable key fails the whole read: a
// half-split coordinate names an edge nobody wrote, and a caller reading it
// would confer authority nobody granted.
func edgesFrom(items []objstore.Item) ([]MembershipEdge, error) {
	out := make([]MembershipEdge, 0, len(items))
	for _, it := range items {
		child, parent, err := membershipEdgeOf(it)
		if err != nil {
			return nil, err
		}
		out = append(out, MembershipEdge{
			Child:   child,
			Parent:  parent,
			AddedBy: it.Attrs[attrMemAddedBy],
			AddedAt: it.Attrs[attrMemAddedAt],
		})
	}
	return out, nil
}

// MemberEdges returns the edges directly under `parent`, in child order.
//
// The parent is the SECOND field of the coordinate, so this reads the partition
// and filters: a key predicate reaches a leading fragment and nothing else. The
// whole graph is 58 edges on the largest live instance.
func (s *Store) MemberEdges(parent string) ([]MembershipEdge, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: ACLMembership, Partition: OwnerRoutd,
	})
	if err != nil {
		return nil, fmt.Errorf("read the members of %q: %w", parent, err)
	}
	all, err := edgesFrom(items)
	if err != nil {
		return nil, err
	}
	out := make([]MembershipEdge, 0, len(all))
	for _, e := range all {
		if e.Parent == parent {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b MembershipEdge) int { return strings.Compare(a.Child, b.Child) })
	return out, nil
}

// ParentEdges returns the edges directly above `child`, with their attributes —
// one begins_with, because the child leads the coordinate.
//
// MembershipParents answers the same direction for the cycle walk and carries
// only the names; this is for the caller that has to read `added_by` off the
// edge, where a role membership and a pairing are two different facts about one
// principal.
func (s *Store) ParentEdges(child string) ([]MembershipEdge, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: ACLMembership, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: child + KeySep},
	})
	if err != nil {
		return nil, fmt.Errorf("read the parents of %q: %w", child, err)
	}
	return edgesFrom(items)
}

// Members returns direct children of `parent`.
func (s *Store) Members(parent string) []string {
	edges, err := s.MemberEdges(parent)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.Child)
	}
	return out
}

// Ancestors returns the transitive closure of parents reachable from `child`,
// excluding `child` itself. Cycle-safe by the visited set, which is what the
// recursive CTE's UNION gave.
func (s *Store) Ancestors(child string) []string {
	parents := MembershipParents(s.Obj())
	ctx := context.Background()
	seen := map[string]bool{child: true}
	frontier := []string{child}
	var out []string
	for len(frontier) > 0 {
		node := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		up, err := parents(ctx, node)
		if err != nil {
			return nil
		}
		for _, p := range up {
			if seen[p] {
				continue
			}
			seen[p] = true
			frontier = append(frontier, p)
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// RemoveMembershipTx drops ONE edge on a transaction the caller owns and reports
// whether an edge went, for routd's `/v1/acl` face: revoking the operator-role
// overload is a revoke, and a revoke that removed nothing must not answer OK.
// The delete is conditioned on the item being there, so the refusal IS the
// not-found answer.
//
// Unlike UnpairTx it carries no `added_by` condition: this reaches role
// membership, which is exactly the edge the operator overload writes.
func (s *Store) RemoveMembershipTx(ctx context.Context, tx *sql.Tx, child, parent string) (bool, error) {
	key, err := membershipKey(child, parent)
	if err != nil {
		return false, err
	}
	return s.TryWriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: ACLMembership, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}
