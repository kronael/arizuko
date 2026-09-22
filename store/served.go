package store

// served.go is the door a daemon opens onto its owners: one dial, one place
// that makes an owner's entities (F29).

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
)

// Handle dials the server for one owner's tables. It MAKES nothing: the owner
// makes its own entities through PrepareOwner at boot.
func Handle(owner string) (*dynstore.Store, error) {
	return dynstore.FromEnv(dynstore.Prefix(mustOwner(owner)))
}

// PrepareOwner makes what one owner serves: this package's entities for it,
// its own package's (extra), its counters, its version marker and its audit
// record. Asking twice is a no-op.
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

// ownerProbes names the table only that owner's own boot makes. It is what
// makes a wrong target loud: an endpoint whose owner never booted otherwise
// reads as zero rows and the daemon comes up healthy.
var ownerProbes = map[string]string{OwnerRoutd: ACL, OwnerOnbod: Invites}

// OpenOwner answers the store one owner's tables live in, for a daemon that
// only READS another owner's tables. It makes nothing: a reader that created
// them would be a second declaration of the owner's schema.
func OpenOwner(owner string) (*Store, error) {
	probe, ok := ownerProbes[mustOwner(owner)]
	if !ok {
		return nil, fmt.Errorf("store: %q holds none of this package's tables, so there is nothing to read", owner)
	}
	h, err := Handle(owner)
	if err != nil {
		return nil, err
	}
	// One read at boot instead of a missing-table error on every request.
	if _, err := h.Query(context.Background(), objstore.Query{
		Entity: probe, Partition: owner, Limit: 1,
	}); err != nil {
		return nil, fmt.Errorf("%s at %s serves no %s (%s must boot to make it first): %w",
			owner, dynstore.URL(), probe, owner, err)
	}
	return NewOver(h), nil
}

// OwnerDriver dials one owner's tables and answers the driver plus a close. A
// served driver holds no pool, so its close ends nothing.
func OwnerDriver(owner string) (Driver, func() error, error) {
	h, err := Handle(owner)
	if err != nil {
		return nil, nil, err
	}
	return h, func() error { return nil }, nil
}

// TestNamespace dials a private namespace and answers the release that drops
// every table it holds. It makes nothing. It lives in a production file because
// the fixture doors that call it (store.OpenMem, routd.OpenMem, runed.OpenMem)
// are exported from production files, so none may import `testing`.
func TestNamespace() (*dynstore.Store, func() error, error) {
	h, err := dynstore.FromEnv(dynstore.TestPrefix())
	if err != nil {
		return nil, nil, err
	}
	release := func() error {
		ctx := context.Background()
		// Everything under the namespace, not only what an owner declares: an
		// entity a test made would otherwise outlive the run.
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
