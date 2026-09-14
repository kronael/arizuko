package store

// served.go is the door a daemon opens onto its owners. One place dials, one
// place makes an owner's entities: the same three steps were copied into a test
// and would have been copied into every daemon's boot, which is how a set
// drifts (F29).

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// Handle dials the server for one owner's tables. It MAKES nothing: a daemon
// that reads another owner's tables must not create them, and the owner makes
// its own through PrepareOwner at boot.
func Handle(owner string) (*dynstore.Store, error) {
	return dynstore.FromEnv(dynstore.Prefix(mustOwner(owner)))
}

// PrepareOwner makes what one owner serves: the entities this package declares
// for it, the ones its own package declares (extra), its counters, its version
// marker and its audit record. The OWNER daemon calls it at boot, and asking
// twice is a no-op.
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
// wrong target loud — an endpoint whose owner has never booted reads as zero
// rows otherwise, and the daemon comes up healthy.
var ownerProbes = map[string]string{OwnerRoutd: ACL, OwnerOnbod: Invites}

// OpenOwner answers the store one owner's tables live in. It is the door a
// daemon that only READS another owner's tables opens — webd, proxyd, gitd,
// slakd, onbod and the CLI all take it — so the dial is written here once
// instead of in each of their boots.
//
// It makes nothing. The owner daemon makes its own entities at boot through
// PrepareOwner, and a reader that created them would be a second declaration of
// the owner's schema, reached first by whichever daemon happened to start.
func OpenOwner(owner string) (*Store, error) {
	probe, ok := ownerProbes[mustOwner(owner)]
	if !ok {
		return nil, fmt.Errorf("store: %q holds none of this package's tables, so there is nothing to read", owner)
	}
	h, err := Handle(owner)
	if err != nil {
		return nil, err
	}
	// A Query against an entity the server does not hold is an error, so this is
	// the probe: one read at boot instead of a missing-table error on every
	// request the daemon later serves.
	if _, err := h.Query(context.Background(), objstore.Query{
		Entity: probe, Partition: owner, Limit: 1,
	}); err != nil {
		return nil, fmt.Errorf("%s at %s serves no %s (%s must boot to make it first): %w",
			owner, dynstore.URL(), probe, owner, err)
	}
	return NewOver(h), nil
}

// OwnerDriver dials one owner's tables and answers the driver plus the close
// that ends what the open holds. A served driver holds no pool, so its close
// ends nothing.
//
// It is the door for an owner whose every table is an entity — authd — and for
// the CLI reads that reach the same tables.
func OwnerDriver(owner string) (Driver, func() error, error) {
	h, err := Handle(owner)
	if err != nil {
		return nil, nil, err
	}
	return h, func() error { return nil }, nil
}

// TestNamespace dials a private namespace on the endpoint this process dials,
// and answers the release that drops every table it holds. It makes nothing:
// the caller says which owners it needs prepared.
//
// It lives beside Handle because the fixture doors that call it — store.OpenMem,
// routd.OpenMem and runed.OpenMem — are exported from production files that
// other packages' tests call, so none of them may import a package that pulls
// `testing` into a daemon binary.
func TestNamespace() (*dynstore.Store, func() error, error) {
	h, err := dynstore.FromEnv(dynstore.TestPrefix())
	if err != nil {
		return nil, nil, err
	}
	release := func() error {
		ctx := context.Background()
		// Everything under this namespace, not just what an owner declares: a
		// test that makes an entity of its own would otherwise leave it on a
		// server that outlives the run.
		left, err := h.Entities(ctx)
		if err != nil {
			return err
		}
		for _, e := range left {
			if err := h.DropEntity(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}
	return h, release, nil
}

// TestOwner is TestNamespace with ONE owner's entities already made in it.
func TestOwner(owner string, extra ...string) (*dynstore.Store, func() error, error) {
	h, release, err := TestNamespace()
	if err != nil {
		return nil, nil, err
	}
	if err := PrepareOwner(context.Background(), h, owner, extra...); err != nil {
		if rerr := release(); rerr != nil {
			return nil, nil, fmt.Errorf("%w (and dropping the namespace: %v)", err, rerr)
		}
		return nil, nil, err
	}
	return h, release, nil
}
