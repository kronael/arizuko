package objstore

// consistent.go is the consistent multi-read, the read twin of the versioned
// replace (spec 6/40 §"The consistent multi-read, and why it is a protocol").
//
// resreg.ExportSnapshot wraps N reads in ONE read transaction so that a
// subsystem document is one MVCC snapshot rather than thirteen unrelated ones
// (resreg/archive.go:69). The obvious move is to put a snapshot or a read
// transaction on the interface. IT CANNOT GO THERE.
//
// DynamoDB's only serializable read across items is TransactGetItems, and every
// slot in it is a GetItem BY KEY: "Get — Initiates a GetItem operation to
// retrieve a set of attributes for the item with the given primary key", up to
// "100 TransactGetItem objects" and 4 MB.
// https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactGetItems.html
// A Query — a sort-key range or a begins_with, which is what every scoped read
// in arizuko is — cannot go in one, and the isolation table on
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/transaction-apis.html
// gives Query and Scan READ-COMMITTED with no snapshot mechanism beside it. So
// an engine the interface is measured against has no snapshot across N table
// reads, and an operation one driver cannot express does not go on the
// interface.
//
// The protocol needs no new shape: read the scope's version marker, run the N
// queries, read the marker again. If it moved, a writer committed during the
// read and the caller retries. That costs one extra point read per export and
// it detects exactly the interleaving a transaction would have prevented.
//
// WHAT IT DOES NOT GIVE is a BLOCKING snapshot. A busy scope retries, and a
// scope busy enough retries until this gives up and says so. resreg already
// carries the ingredient: ExportSnapshot computes a checksum per subsystem, so
// a moved marker and a changed checksum are the same signal, and the export is
// already a read the operator repeats when it fails.

import (
	"context"
	"fmt"
)

// ReadConsistent runs read between two point reads of the scope's marker, and
// runs it again if the scope moved in between.
//
// It detects exactly the writers that carry Bump, and no others (marker.go). A
// replace holding the scope is not an interleave to retry past — the scope is
// part-written by definition — so it answers ErrScopeHeld and the caller
// retries at its own pace.
//
// read may run more than once. Anything it accumulates it must DISCARD at the
// top of each run, or a retry returns the first attempt's rows appended to the
// second's.
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

// settledMarker reads the marker and refuses the two states that make a
// consistent read impossible rather than reporting one that is not.
//
// A missing marker is refused because no writer bumps a marker that is not
// there, so "unchanged" over an absent one would be a guarantee nobody made.
//
// A HELD marker is refused because equal versions do not mean an unchanged
// scope while a replace is running: the claim bumps the version once at the
// start and once at the finish, so a replace that claimed before the first read
// and settles after the second shows the same version at both — with every one
// of its row writes in between. The holder is what makes that case visible.
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
