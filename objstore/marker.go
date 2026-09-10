package objstore

// marker.go is the version marker the unbounded protocols share.
//
// A BOUNDED write needs no marker. It is one GuardedBatch, so its intermediate
// state is never observable and its audit row commits with it. The three
// protocols below are UNBOUNDED — the versioned replace over a subsystem, the
// consistent multi-read that is its read twin, and the reachability guard whose
// decision comes from a graph read — so each needs one item that says whether
// the scope moved and whether somebody is part-way through changing it. Spec
// 6/40 §"The audited write" names that item and says it is ONE mechanism
// serving those callers rather than a scheme imposed on the other forty writes.
//
// The marker carries at most four attributes:
//
//	version  a value that CHANGES on every write to the scope
//	holder   present only while a Replace holds the scope, and its value is the
//	         fencing token that replace writes under
//	intent   present with holder, and its value names the WORK that holder is
//	         doing, so a later replace can prove it would write the same rows
//	         and finish what this one left (replace.go)
//	since    when that holder claimed the scope, so an operator reading a stuck
//	         scope can tell an abandoned claim from one still in flight
//
// Absent is the only null (objstore.go), so "settled" is `holder` absent and
// needs no second value to say it.
//
// HOLDER AND INTENT ARE TWO VALUES BECAUSE THEY ANSWER TWO QUESTIONS, and one
// value cannot answer both. The fence wants a value UNIQUE to one attempt: every
// batch a replace writes carries a check that the holder is still its own, so a
// second attempt of the same work must not be able to pass the first one's fence
// (replace.go Write). The roll-forward wants a value STABLE across attempts:
// that is the whole proof that finishing another replace's work is safe rather
// than merely available. A single attribute would have to be both, and a marker
// that made two attempts indistinguishable would let a dead replace's last
// in-flight batch land inside the one recovering from it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// Scope names the ITEM that carries one scope's version marker.
//
// It is an ordinary item at an ordinary coordinate, which means every read that
// SELECTS that coordinate returns it. Give the marker an entity of its own, or
// at least a partition the scope's rows do not share: a marker sitting inside
// the rows it versions arrives in the caller's own scan as a row with no
// columns it recognises. An entity that declares an ordered sort key
// (OrderedSort) constrains the marker's key to that width as well, which is the
// second reason the marker's own entity is the easy answer.
type Scope struct {
	Entity string
	Key    Key
}

// The marker's attributes. They are JSON keys inside the item's document, so
// they obey the same identifier rule every attribute does.
const (
	markerVersion = "version"
	markerHolder  = "holder"
	markerIntent  = "intent"
	markerSince   = "since"
)

var (
	// ErrNoMarker is the answer for a scope whose marker item is not there.
	//
	// It is loud rather than lenient. A missing marker means no writer has ever
	// bumped it and none ever will, so a protocol that treated absence as
	// "settled and unchanged" would report a consistent read over a scope with
	// no consistency at all — the guard that ends the error and starts the
	// silence. EnsureScope is what a daemon calls beside CreateEntity.
	ErrNoMarker = errors.New("the scope carries no version marker")

	// ErrScopeHeld is the answer when a Replace holds the scope: another one
	// cannot start, and a consistent read cannot be taken across a scope that is
	// part-way through being replaced.
	//
	// It is NOT retried inside the protocols. A lost race is transient and worth
	// a retry; a replace in flight is another caller's work in progress, and
	// hammering it eight times in a row costs eight round trips to reach the
	// same answer. The caller retries at its own pace.
	ErrScopeHeld = errors.New("another replace holds the scope")
)

// EnsureScope creates a scope's marker if it is not there, and leaves an
// existing one exactly as it is.
//
// A daemon calls it where it already calls CreateEntity. It has to exist
// because Bump is an Update: an Update writes nothing when the item is missing
// (batch.go), so a bounded write carrying a Bump against a scope nobody
// created would have its WHOLE batch refused — the mutation lost with it.
func EnsureScope(ctx context.Context, s Store, sc Scope) error {
	if err := sc.validate(); err != nil {
		return err
	}
	// A refusal here is the item already being there, which is the outcome this
	// call wants. Nothing else can refuse a create-if-absent.
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
// consistent multi-read over the scope can see it.
//
// ReadConsistent detects exactly the writers that carry this, and no others.
// That is the protocol's real bound and it is a discipline, not a guarantee the
// engine gives: a writer that forgets the Bump is invisible to every export,
// the same silent way a missing audit row is. It costs one action out of the
// hundred a batch holds and it rides in the caller's own transaction, so the
// mutation and the version move together or neither does.
//
// The write is unconditional and it changes `version` to a value no other call
// produces, which is all a reader comparing two point reads needs. It does NOT
// compare-and-swap: two bounded writers that bump at the same time both commit
// and either value differs from what the reader saw, so a CAS here would buy a
// retry loop on every ordinary write and nothing else. It leaves `holder`
// alone, so a bump during a replace does not make the scope look settled.
func Bump(sc Scope) Write {
	return Write{
		Op: Update, Entity: sc.Entity, Key: sc.Key,
		Attrs: map[string]string{markerVersion: freshVersion()},
	}
}

// marker is one point read of a scope's marker. version is what a reader
// compares; holder is empty when the scope is settled, and intent says what the
// holder is writing.
type marker struct {
	version string
	holder  string
	intent  string
	since   string
	present bool
}

// readMarker reads the marker item. A missing item is not an error here — the
// versioned replace creates one and the other two protocols refuse one — so the
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

// freshVersion returns a value no other call returns.
//
// It is random rather than a counter or a clock. A counter is a read-then-write
// on the item this value is meant to make comparable, and a clock repeats: two
// writers inside one tick would render the same version, and a reader comparing
// two point reads would call a scope unchanged that changed twice. Sixteen
// random bytes cost one syscall and answer only the question asked of them,
// which is "is this the same value I saw".
func freshVersion() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never returns an error on any platform arizuko runs
		// on, and a version that repeated would make a moved scope read as
		// unchanged — a wrong answer, not a slow one.
		panic("objstore: the system random source failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
