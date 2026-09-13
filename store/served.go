package store

// served.go is the door a daemon opens when its instance keeps its owners on a
// DynamoDB server instead of in files. One place dials, one place makes an
// owner's entities: the same three steps were copied into a test and would have
// been copied into every daemon's boot, which is how a set drifts (F29).

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore/dynstore"
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
