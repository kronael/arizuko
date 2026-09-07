package objstore

// reach.go is the reachability guard: a graph read decides a write, and the
// write is guarded on the graph not having moved since the read (spec 6/40
// §"Three is not enough" and §"The seam is TWO primitives").
//
// WHY IT IS NOT A Check. store.AddMembership walks the ancestor closure with
// `WITH RECURSIVE up(p)` and refuses the edge if the child is already in it,
// INSIDE the transaction that adds the edge (store/membership.go:36). A Check
// binds an ITEM — this key, this condition — and a cycle test is a condition on
// a graph frontier, which is not an item. Two concurrent inserts closing a
// cycle from opposite ends each walk a graph without the other's edge, both
// pass, and both commit. SQLite's single writer hides it; Postgres at READ
// COMMITTED does not, and neither does a key-value engine. That is BUGS L256.
//
// The version marker is what serialises them. Both walkers read the same
// version, both compare-and-swap it in the batch that writes the edge, exactly
// one wins, and the loser walks again — over the graph the winner left, where
// the cycle is now visible. It is the Increment shape (protocol.go) with a
// graph read where the counter's point read is.
//
// LATENT, NOT LIVE. specs/15/h measured the graph on all three live instances:
// 58 edges on krons pointing at exactly two parents, `role:operator` and
// `role:member`, and no parent is ever itself a child, so the recursion never
// recurses. It stops being latent the moment a folder is made a member of
// something.

import (
	"context"
	"errors"
	"fmt"
)

// ErrCycle is the answer for an edge that would make a node its own ancestor.
// Both copies of the walk already answer with a sentinel of their own
// (store.ErrCycle, resources.ErrMembershipCycle), so an adopter maps rather
// than restructures.
var ErrCycle = errors.New("the edge would close a cycle")

// maxWalkNodes bounds one walk.
//
// The walk terminates on its own — a visited set makes a cyclic graph finite —
// so this bounds SIZE, not looping. It is here because the same walk is on the
// authorize path (auth/authorize.go:134, :308) where an unbounded read is a
// request that never answers. The live graphs measured in specs/15/h are 58
// edges, so any real graph is four orders of magnitude inside it and a walk
// that hits this bound has found a graph nobody designed.
const maxWalkNodes = 10000

// ParentsFunc answers "which nodes sit directly above this one".
//
// It is a callback and not a coordinate convention because the edge layout
// belongs to the caller: acl_membership's item is keyed by the pair, and a
// second graph would key its own. The callback IS a Query the caller shapes, so
// this protocol still stands on the two primitives and knows nothing about how
// an edge is stored.
type ParentsFunc func(ctx context.Context, node string) ([]string, error)

// Reachable reports whether `to` sits at or above `from`.
//
// It INCLUDES from itself, which is the `SELECT ?` seed of the recursive CTE
// both copies run, so an edge from a node to itself is a cycle of length one
// and needs no separate test. A caller that answers a self edge in its own
// terms — store.AddMembership returns ErrSelfMembership — keeps its own check
// ahead of this one, because the answer here is ErrCycle.
//
// This is the half a caller adopts when it does not own the write:
// resreg/resources/membership.go:88 is a ValidateRow hook and resreg's engine
// issues the insert, so the hook can take the walk and the engine takes the
// guard.
func Reachable(ctx context.Context, parents ParentsFunc, from, to string) (bool, error) {
	seen := map[string]bool{from: true}
	frontier := []string{from}
	for len(frontier) > 0 {
		node := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		if node == to {
			return true, nil
		}
		up, err := parents(ctx, node)
		if err != nil {
			return false, fmt.Errorf("walk above %q: %w", node, err)
		}
		for _, p := range up {
			if seen[p] {
				continue
			}
			if len(seen) >= maxWalkNodes {
				return false, fmt.Errorf("walk above %q reached %d nodes, which is more than "+
					"any membership graph arizuko has measured", from, len(seen))
			}
			seen[p] = true
			frontier = append(frontier, p)
		}
	}
	return false, nil
}

// AddEdge writes an edge, and the companions that must land with it, unless the
// edge would close a cycle.
//
// child and parent are the edge: parent sits above child. The walk starts at
// parent and asks whether child is already at or above it, which is what
// `WITH RECURSIVE up(p) … SELECT COUNT(*) FROM up WHERE p = child` asks.
//
// writes are the caller's own, so the edge item's shape stays the caller's. The
// marker's compare-and-swap rides in the same batch, which is what makes the
// walk and the write one decision: a batch is all-or-nothing, so an edge cannot
// land on a graph that moved after it was walked. The version is the whole
// condition — a replace moves it in the same write that takes the scope, so an
// edge cannot slip past a claim either.
//
// committed=false is the caller's own condition refusing — an edge already
// there, under a Put with ItemAbsent — reported the way GuardedBatch reports
// it. A lost race is retried here and never returned, because the answer to a
// lost race is another walk.
func AddEdge(ctx context.Context, s Store, sc Scope, parents ParentsFunc,
	child, parent string, writes []Write) (bool, error) {
	if len(writes) == 0 {
		return false, fmt.Errorf("add the edge %s -> %s: no writes, so the guard would "+
			"decide nothing", child, parent)
	}
	for range maxCASAttempts {
		was, err := readMarker(ctx, s, sc)
		if err != nil {
			return false, err
		}
		if !was.present {
			return false, fmt.Errorf("add the edge %s -> %s: %w, so two concurrent inserts "+
				"would have nothing to serialise on", child, parent, ErrNoMarker)
		}
		if !was.settled() {
			return false, fmt.Errorf("add the edge %s -> %s: %w since %s",
				child, parent, ErrScopeHeld, was.since)
		}
		hit, err := Reachable(ctx, parents, parent, child)
		if err != nil {
			return false, err
		}
		if hit {
			return false, fmt.Errorf("add the edge %s -> %s: %w, because %s is already at or "+
				"above %s", child, parent, ErrCycle, child, parent)
		}
		guarded := append([]Write{{
			Op: Update, Entity: sc.Entity, Key: sc.Key,
			Attrs: map[string]string{markerVersion: freshVersion()},
			Cond:  Cond{Where: []Cmp{{Attr: markerVersion, Op: Eq, Value: was.version}}},
		}}, writes...)
		committed, err := s.GuardedBatch(ctx, nil, guarded)
		if err != nil {
			return false, err
		}
		if committed {
			return true, nil
		}
		// A refusal is one bit, so this re-reads the marker to tell a lost race
		// — which is answered by walking again — from the caller's own condition,
		// which is the caller's answer to give.
		now, err := readMarker(ctx, s, sc)
		if err != nil {
			return false, err
		}
		if now.version == was.version && now.settled() {
			return false, nil
		}
	}
	return false, fmt.Errorf("add the edge %s -> %s: the graph moved under %d walks in a row",
		child, parent, maxCASAttempts)
}
