package objstore

// consistent.go is the consistent multi-read, the read twin of the versioned
// replace (spec 6/40 §"The consistent multi-read, and why it is a protocol").
//
// A read transaction CANNOT go on the interface: DynamoDB's TransactGetItems
// takes only GetItem BY KEY, and a Query is read-committed with no snapshot
// beside it. So the protocol is: read the scope's marker, run the N queries,
// read the marker again, retry if it moved. It is not a BLOCKING snapshot; a
// scope busy enough retries until this gives up and says so.

import (
	"context"
	"fmt"
)

// ReadConsistent runs read between two point reads of the scope's marker, and
// runs it again if the scope moved in between. It detects exactly the writers
// that carry Bump (marker.go). A replace holding the scope answers
// ErrScopeHeld and the caller retries at its own pace.
//
// read may run more than once and must DISCARD what it accumulated at the top
// of each run.
func ReadConsistent(ctx context.Context, s Store, sc Scope, read func(context.Context) error) error {
	for range maxCASAttempts {
		before, err := settledMarker(ctx, s, sc)
		if err != nil {
			return err
		}
		if err := read(ctx); err != nil {
			return err
		}
		after, err := settledMarker(ctx, s, sc)
		if err != nil {
			return err
		}
		if before.version == after.version {
			return nil
		}
	}
	return fmt.Errorf("read %s/%s consistently: the scope moved under %d reads in a row",
		sc.Entity, sc.Key.Sort, maxCASAttempts)
}

// settledMarker refuses a missing marker (no writer bumps a marker that is not
// there, so "unchanged" would be a guarantee nobody made) and a HELD one (a
// replace bumps at claim and at finish, so equal versions can bracket every
// one of its row writes).
func settledMarker(ctx context.Context, s Store, sc Scope) (marker, error) {
	m, err := readMarker(ctx, s, sc)
	if err != nil {
		return marker{}, err
	}
	if !m.present {
		return marker{}, fmt.Errorf("read %s/%s consistently: %w, so nothing would report a "+
			"writer that committed during the read", sc.Entity, sc.Key.Sort, ErrNoMarker)
	}
	if !m.settled() {
		return marker{}, fmt.Errorf("read %s/%s consistently: %w since %s, so its rows are "+
			"part-replaced", sc.Entity, sc.Key.Sort, ErrScopeHeld, m.since)
	}
	return m, nil
}
