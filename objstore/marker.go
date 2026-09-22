package objstore

// marker.go is the version marker the three UNBOUNDED protocols share: the
// versioned replace, the consistent multi-read and the reachability guard. A
// bounded write is one GuardedBatch and needs none (spec 6/40 §"The audited
// write").
//
// The marker carries at most four attributes:
//
//	version  a value that CHANGES on every write to the scope
//	holder   present only while a Replace holds the scope: the fencing token
//	         that replace writes under
//	intent   present with holder: names the WORK, so a later replace can prove
//	         it would write the same rows and finish it (replace.go)
//	since    when that holder claimed the scope
//
// "settled" is `holder` absent. holder and intent are two values because the
// fence must be UNIQUE to one attempt and the roll-forward proof must be
// STABLE across attempts; one value cannot be both.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// Scope names the ITEM that carries one scope's version marker. Give it an
// entity of its own: a marker inside the rows it versions arrives in the
// caller's own scan as a row with no columns it recognises, and an OrderedSort
// entity constrains the marker's key to that width too.
type Scope struct {
	Entity string
	Key    Key
}

// The marker's attributes; JSON keys inside the item's document.
const (
	markerVersion = "version"
	markerHolder  = "holder"
	markerIntent  = "intent"
	markerSince   = "since"
)

var (
	// ErrNoMarker is the answer for a scope whose marker item is not there. It
	// is loud: treating absence as "settled and unchanged" would report a
	// consistent read over a scope with no consistency at all. EnsureScope is
	// what a daemon calls beside CreateEntity.
	ErrNoMarker = errors.New("the scope carries no version marker")

	// ErrScopeHeld is the answer when a Replace holds the scope. The protocols
	// do NOT retry it: a replace in flight is another caller's work, not a
	// lost race.
	ErrScopeHeld = errors.New("another replace holds the scope")
)

// EnsureScope creates a scope's marker if it is not there, and leaves an
// existing one exactly as it is. It must run before Bump: Bump is an Update,
// which writes nothing when the item is missing, so the WHOLE batch carrying
// it would be refused.
func EnsureScope(ctx context.Context, s Store, sc Scope) error {
	if err := sc.validate(); err != nil {
		return err
	}
	// A refusal here is the item already being there, the outcome this call
	// wants.
	if _, err := s.GuardedBatch(ctx, nil, []Write{{
		Op: Put, Entity: sc.Entity, Key: sc.Key,
		Attrs: map[string]string{markerVersion: freshVersion()},
		Cond:  Cond{Item: ItemAbsent},
	}}); err != nil {
		return fmt.Errorf("ensure the marker for %s/%s/%s: %w",
			sc.Entity, sc.Key.Partition, sc.Key.Sort, err)
	}
	return nil
}

// Bump is the write a BOUNDED mutation adds to its own batch so that a
// consistent multi-read over the scope can see it. ReadConsistent detects
// exactly the writers that carry this: a writer that forgets the Bump is
// invisible to every export, as silently as a missing audit row.
//
// It is unconditional, not a compare-and-swap: two bounded writers bumping at
// once both commit and either value differs from what the reader saw. It
// leaves `holder` alone.
func Bump(sc Scope) Write {
	return Write{
		Op: Update, Entity: sc.Entity, Key: sc.Key,
		Attrs: map[string]string{markerVersion: freshVersion()},
	}
}

// marker is one point read of a scope's marker.
type marker struct {
	version string
	holder  string
	intent  string
	since   string
	present bool
}

// readMarker reads the marker item. A missing item is not an error here; the
// answer carries `present` and each caller says what absence means to it.
func readMarker(ctx context.Context, s Store, sc Scope) (marker, error) {
	if err := sc.validate(); err != nil {
		return marker{}, err
	}
	items, err := s.Query(ctx, Query{
		Entity: sc.Entity, Partition: sc.Key.Partition,
		Key:     KeyPredicate{Op: ExactKey, Value: sc.Key.Sort},
		Project: []string{markerVersion, markerHolder, markerIntent, markerSince},
	})
	if err != nil {
		return marker{}, fmt.Errorf("read the marker for %s/%s/%s: %w",
			sc.Entity, sc.Key.Partition, sc.Key.Sort, err)
	}
	if len(items) == 0 {
		return marker{}, nil
	}
	return marker{
		version: items[0].Attrs[markerVersion],
		holder:  items[0].Attrs[markerHolder],
		intent:  items[0].Attrs[markerIntent],
		since:   items[0].Attrs[markerSince],
		present: true,
	}, nil
}

// settled reports a marker nobody is part-way through replacing.
func (m marker) settled() bool { return m.present && m.holder == "" }

// validate refuses a scope that does not name one item.
func (sc Scope) validate() error {
	if sc.Entity == "" || sc.Key.Partition == "" || sc.Key.Sort == "" {
		return fmt.Errorf("scope %q/%q/%q does not name one item; a marker is an item and "+
			"needs all three parts of a coordinate",
			sc.Entity, sc.Key.Partition, sc.Key.Sort)
	}
	return nil
}

// freshVersion returns a value no other call returns. Random rather than a
// counter (a read-then-write on the item itself) or a clock (two writers in
// one tick would render the same version and a moved scope would read
// unchanged).
func freshVersion() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on a supported platform, and a repeated
		// version is a wrong answer, not a slow one.
		panic("objstore: the system random source failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
