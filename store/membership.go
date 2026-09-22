package store

// membership.go holds every read and write of `acl_membership`: one item per
// edge, addressed by (child, parent).
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). The child leads the
// coordinate because that is the direction the graph is walked: "which nodes
// sit directly above this one" is a begins_with on `child + KeySep`.
// Members(parent) is a partition read with a filter (58 edges on krons).
//
// THE CYCLE GUARD IS objstore.AddEdge, for every writer this package owns. A
// Check binds one item and a cycle test is a condition on a graph frontier, so
// two inserts closing a ring from opposite ends would both commit; the
// marker's compare-and-swap serialises them (objstore/reach.go, BUGS L256).
// The edge write is a Merge, so it refuses nothing and the companion writes (a
// pairing's token, a folder creation's row) ride the same batch.

import (
	"context"
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

// The attributes an edge item carries; `child` and `parent` are the
// coordinate. `added_by` is ALWAYS written, empty string included: it is the
// resource's own row filter (`added_by <> 'pairing'`), and an ABSENT attribute
// answers no comparison, so an edge that left it off would write itself out of
// its own projection.
const (
	attrMemAddedBy = "added_by"
	attrMemAddedAt = "added_at"
)

// membershipKey is the coordinate of one edge. A value carrying the separator
// is refused: two different edges would address one item.
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
// that does not split is a LOUD fault: an authorize walk reading it would
// confer authority nobody granted.
func membershipEdgeOf(it objstore.Item) (child, parent string, err error) {
	child, parent, ok := strings.Cut(it.Key.Sort, KeySep)
	if !ok {
		return "", "", fmt.Errorf("membership item %q does not split into (child, parent)",
			it.Key.Sort)
	}
	return child, parent, nil
}

// membershipPut renders the create-if-absent write every edge writer issues:
// re-adding an edge must not restamp who added it or when. Both attributes
// are Init, and it is a Merge rather than a guarded Put so the write has
// NOTHING TO REFUSE and can ride beside the token a pairing spends or the
// groups row a folder creation writes (BUGS L256).
func membershipPut(child, parent, addedBy string) (objstore.Write, error) {
	key, err := membershipKey(child, parent)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Merge, Entity: ACLMembership, Key: key,
		Init: map[string]string{
			attrMemAddedBy: addedBy,
			attrMemAddedAt: core.NowTS(),
		},
	}, nil
}

// MembershipParents answers which principals sit directly above one: the
// callback objstore.Reachable walks. EXPORTED because the same guard decides
// four writes, and four copies of one walk can disagree with each other (BUGS
// L256).
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
// child its own ancestor: the walk starts at parent and asks whether child is
// already at or above it. Reachable seeds with the node itself, so a self edge
// answers true too; callers check it first because ErrSelfMembership is a
// different answer for the operator.
func MembershipWouldCycle(ctx context.Context, q EntityQuerier, child, parent string) (bool, error) {
	return objstore.Reachable(ctx, MembershipParents(q), parent, child)
}

// AddMembership writes (child → parent) idempotently, unless the edge would
// close a cycle, and records it. The walk and the write are ONE decision
// (objstore.AddEdge), and the audit row rides the same batch so it cannot
// outlive a refused write.
func (s *Store) AddMembership(child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	edge, err := membershipPut(child, parent, addedBy)
	if err != nil {
		return err
	}
	// addedBy stands in as the actor for callers that never set AsUser/AsCLI,
	// the fallback AddACLRow uses too.
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
// It does NOT go through WriteEntities, and that is not a way round the
// marker: objstore.AddEdge writes the marker itself as a compare-and-swap,
// because the walk that decided this write has to be invalidated by any graph
// move, which a plain bump would not catch (objstore/reach.go). Every entity
// these batches touch is routd-scoped, so that one marker is the one a bump
// would have moved.
func (s *Store) addEdge(child, parent string, writes ...objstore.Write) error {
	_, err := s.AddEdgeWith(context.Background(), child, parent, "", writes...)
	return err
}

// AddEdgeWith draws one edge under the cycle guard with the caller's OWN writes
// in the same batch, and answers whether the batch committed. The companions
// are the token a pairing spends, the groups row a member edge implies (BUGS
// L119), the creator's admin grant, the audit row: one guarded batch carrying
// the marker's compare-and-swap, so the walk and everything it decides are one
// decision (BUGS L256).
//
// FALSE IS A COMPANION'S CONDITION REFUSING, never this edge's: the edge write
// is a Merge and has nothing to refuse, so a caller reads a false as its own
// answer. addedBy empty means the caller rendered the edge write itself.
func (s *Store) AddEdgeWith(ctx context.Context, child, parent, addedBy string,
	writes ...objstore.Write) (bool, error) {
	if child == parent {
		return false, ErrSelfMembership
	}
	if addedBy != "" {
		edge, err := membershipPut(child, parent, addedBy)
		if err != nil {
			return false, err
		}
		writes = append([]objstore.Write{edge}, writes...)
	}
	ok, err := objstore.AddEdge(ctx, s.Obj(), OwnerScope(OwnerRoutd),
		MembershipParents(s.Obj()), child, parent, writes)
	if errors.Is(err, objstore.ErrCycle) {
		return false, ErrCycle
	}
	return ok, err
}

func (s *Store) RemoveMembership(child, parent string) error {
	key, err := membershipKey(child, parent)
	if err != nil {
		return err
	}
	actor, actorSub, surface := s.auditIdentity()
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w := objstore.Write{Op: objstore.Delete, Entity: ACLMembership, Key: key}
		return []objstore.Write{w}, audit.Event{
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

// RoleMember is the seeded floor role (the messaging verbs, store.SeedRoutdFloor).
// Every group is born a member of it (5/33): power above the floor is explicit
// delegation, never derived from path depth.
const RoleMember = "role:member"

// SeedMemberRole binds a new folder to RoleMember, with the creation's own
// writes in the SAME batch. It is the ONE writer of the create-time edge:
// three of four creating writers once wrote a groups row without it, and a
// folder with no edge expands to nothing and has no messaging tools (BUGS
// L119). Idempotent. A false is the CREATION's own condition: the folder is
// already someone's (AddEdgeWith).
func SeedMemberRole(ctx context.Context, s *Store, folder string,
	also ...objstore.Write) (bool, error) {
	return s.AddEdgeWith(ctx, "folder:"+folder, RoleMember, "system:4r-member", also...)
}

// PutMembership is the audit-free twin of AddMembership, for an edge no
// operator asked for. Same self/cycle rejection, same marker compare-and-swap.
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

// PutMembershipInto buffers the edge write on the caller's write set, for
// routd's `/v1/acl` face, where the edge rides the batch that carries
// resreg.invoke's audit row.
//
// Its cycle guard is the same walk WITHOUT the marker's compare-and-swap, so
// two edges closing a ring from opposite ends are serialised by the engine's
// write lock, which Postgres at READ COMMITTED does not give (BUGS L256). That
// is why it has one caller and not four. The walk reads the LIVE table; a
// caller buffers one edge per call, so there is no edge of its own to miss.
func (s *Store) PutMembershipInto(ctx context.Context, ws *WriteSet, child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	cycles, err := MembershipWouldCycle(ctx, s.Obj(), child, parent)
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
	// A Merge REFUSES NOTHING, so it can ride beside a write whose own refusal
	// is an answer.
	ws.Add(w)
	return nil
}

// MembershipEdge is one edge with the two attributes the item carries, for the
// readers that need MORE than the two ends: onbod's account page and its
// admission tick, which asks which parent PAIRING bound a jid to.
type MembershipEdge struct {
	Child   string
	Parent  string
	AddedBy string
	AddedAt string
}

// edgesFrom turns items into edges. One unreadable key fails the whole read: a
// caller reading a half-split coordinate would confer authority nobody
// granted.
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

// MemberEdges returns the edges directly under `parent`, in child order. The
// parent is the SECOND field of the coordinate, so this reads the partition
// and filters.
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

// ParentEdges returns the edges directly above `child`, with their attributes,
// in one begins_with. MembershipParents answers the same direction with names
// only; this is for the caller that reads `added_by`, where a role membership
// and a pairing are two different facts about one principal.
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
// excluding `child` itself. Cycle-safe by the visited set.
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

// RemoveMembershipInto buffers the removal of ONE edge on the caller's write
// set, for routd's `/v1/acl` face: a revoke that removed nothing must not
// answer OK, so the delete is conditioned on the item being there and refused
// is the not-found answer. Unlike UnpairInto it carries no `added_by`
// condition: this reaches role membership, which is what the operator
// overload writes.
func (s *Store) RemoveMembershipInto(ws *WriteSet, child, parent string, refused error) error {
	key, err := membershipKey(child, parent)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Delete, Entity: ACLMembership, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}
