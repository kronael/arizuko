package objstore

// reach.go is the reachability guard: a graph read decides a write, and the
// write is guarded on the graph not having moved since the read (spec 6/40
// §"Three is not enough").
//
// It is not a Check: a cycle test is a condition on a graph frontier, not an
// item. Two concurrent inserts closing a cycle from opposite ends each walk a
// graph without the other's edge and both commit (BUGS L256). The version
// marker serialises them: both compare-and-swap it in the batch that writes
// the edge, one wins, the loser walks again.
//
// LATENT, NOT LIVE: on every live instance no parent is itself a child
// (specs/15/h). It stops being latent the moment a folder is made a member of
// something.

import (
	"context"
	"errors"
	"fmt"
)

// ErrCycle is the answer for an edge that would make a node its own ancestor.
var ErrCycle = errors.New("the edge would close a cycle")

// maxWalkNodes bounds SIZE, not looping (a visited set makes a cyclic graph
// finite): the same walk is on the authorize path, where an unbounded read is
// a request that never answers. Live graphs are 58 edges (specs/15/h).
const maxWalkNodes = 10000

// ParentsFunc answers "which nodes sit directly above this one". It is a
// callback because the edge layout belongs to the caller; the callback IS a
// Query the caller shapes.
type ParentsFunc func(ctx context.Context, node string) ([]string, error)

// Reachable reports whether `to` sits at or above `from`. It INCLUDES from
// itself, so a self edge is a cycle of length one; a caller that answers a
// self edge in its own terms keeps its check ahead of this one. This is the
// half a caller adopts when it does not own the write (a ValidateRow hook).
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

// AddEdge writes an edge, and the companions that must land with it, unless
// the edge would close a cycle. parent sits above child; the walk starts at
// parent and asks whether child is already at or above it.
//
// The marker's compare-and-swap rides in the caller's batch, so an edge cannot
// land on a graph that moved after it was walked, nor slip past a replace's
// claim. committed=false is the CALLER'S OWN condition refusing; a lost race
// on the marker is retried here and never returned.
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
		// A refusal is one bit: re-read the marker to tell a lost race (walk
		// again) from the caller's own condition (the caller's answer).
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
