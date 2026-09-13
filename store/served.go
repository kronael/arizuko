package store

// served.go is the door a daemon opens when its instance keeps its owners on a
// DynamoDB server instead of in files. One place dials, one place makes an
// owner's entities: the same three steps were copied into a test and would have
// been copied into every daemon's boot, which is how a set drifts (F29).

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// Served reports whether this instance keeps its owners on a server. It is the
// branch every daemon's boot takes: served, or the file it has always opened.
func Served() bool { return dynstore.URL() != "" }

// Handle dials the server for one owner's tables. It MAKES nothing: a daemon
// that reads another owner's tables must not create them, and the owner makes
// its own through PrepareOwner at boot.
func Handle(owner string) (*dynstore.Store, error) {
	return dynstore.FromEnv(dynstore.Prefix(mustOwner(owner)))
}

// PrepareOwner makes what one owner serves: the entities this package declares
// for it, the ones its own package declares (extra), its counters, its version
// marker and its audit record. It is the served twin of the migration chain a
// file runs, the OWNER daemon calls it at boot, and asking twice is a no-op.
func PrepareOwner(ctx context.Context, d Driver, owner string, extra ...string) error {
	entities := append([]string{}, EntitiesByOwner[owner]...)
	entities = append(entities, extra...)
	if len(entities) == 0 {
		return fmt.Errorf("store: %q serves no entities, so there is nothing to prepare", owner)
	}
	if err := EnsureEntitySet(ctx, d, owner, entities...); err != nil {
		return err
	}
	return audit.EnsureEntities(ctx, d)
}

// ownerProbes names the entity a reader proves is there before it answers for
// one owner: the table only that owner's own boot makes. It is what makes a
// wrong target loud — an empty file and an endpoint whose owner has never
// booted both read as zero rows otherwise, and the daemon comes up healthy.
var ownerProbes = map[string]string{OwnerRoutd: ACL, OwnerOnbod: Invites}

// OpenOwner answers the store one owner's tables live in: the endpoint this
// instance is served from, or the file at path. It is the door a daemon that
// only READS another owner's tables opens — webd, proxyd, gitd, slakd, onbod
// and the CLI all take it — so the branch is written here once instead of in
// each of their boots.
//
// It makes nothing. The owner daemon makes its own entities at boot through
// PrepareOwner, and a reader that created them would be a second declaration of
// the owner's schema, reached first by whichever daemon happened to start.
func OpenOwner(owner, path string) (*Store, error) {
	probe, ok := ownerProbes[mustOwner(owner)]
	if !ok {
		return nil, fmt.Errorf("store: %q holds none of this package's tables, so there is nothing to read", owner)
	}
	if !Served() {
		return openOwned(path, OwnerDBs[owner], probe, owner)
	}
	h, err := Handle(owner)
	if err != nil {
		return nil, err
	}
	// A Query against an entity the server does not hold is an error, so this
	// is the served twin of the file probe: one read at boot instead of a
	// missing-table error on every request the daemon later serves.
	if _, err := h.Query(context.Background(), objstore.Query{
		Entity: probe, Partition: owner, Limit: 1,
	}); err != nil {
		return nil, fmt.Errorf("%s at %s serves no %s (%s must boot to make it first): %w",
			owner, dynstore.URL(), probe, owner, err)
	}
	return NewOver(h), nil
}

// OwnerDriver answers the driver one owner's tables live on and the close that
// ends what the open holds. A served driver holds no pool, so its close ends
// nothing.
//
// It is the door for an owner whose boot runs no migration chain — authd, whose
// every table is an entity — and for the CLI reads that reach the same tables.
// target is what the daemon was configured with: an endpoint makes it
// irrelevant, and a file instance opens exactly it.
func OwnerDriver(owner, target string) (Driver, func() error, error) {
	if Served() {
		h, err := Handle(owner)
		if err != nil {
			return nil, nil, err
		}
		return h, func() error { return nil }, nil
	}
	// Strict, the same way every owner opener is strict: the file is seeded by
	// `arizuko create`, and a missing one would otherwise be created empty —
	// for authd, a fresh keyring that invalidates every live session.
	if err := db_utils.RequireDB(target); err != nil {
		return nil, nil, err
	}
	db, err := db_utils.Open(target)
	if err != nil {
		return nil, nil, err
	}
	return sqlstore.New(db), db.Close, nil
}
