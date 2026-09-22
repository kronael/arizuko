// Package runed is the execution plane (spec 5/P): the per-spawn container
// lifecycle. It is a pure container-spawner: routd hosts the agent MCP socket
// and is the sole message appender; authd is the sole token signer. runed
// mounts the ipc dir into each spawn and manages spawn/steer/kill.
package runed

// db.go is runed's composition root: the only file in the daemon that names a
// storage driver (spec 6/37 step 5). There is no migration set and no DDL;
// asking the driver for the entities twice is a no-op.

import (
	"context"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

// ErrNotFound signals an absent item to the HTTP layer (404).
var ErrNotFound = runstore.ErrNotFound

// The shapes runed's handlers pass around are runstore's.
type (
	// Spawn is one container spawn (the execution-session envelope).
	Spawn = runstore.Spawn
	// ActiveSpawn is a live spawn's identifying info for the steer path.
	ActiveSpawn = runstore.ActiveSpawn
	// Session is one session_log entry, as both the dashboard page and routd's
	// federated read project it.
	Session = runstore.Session
)

// DB owns runed's tables. Every read and write is runstore's, embedded so this
// file holds no pass-through method to drift.
type DB struct {
	*runstore.Store

	// obj is the handle this database is served from; audit records through
	// it.
	obj objstore.Store

	// release drops the private namespace OpenMem reserved. A runed opened
	// against the instance's own tables leaves them alone.
	release func() error
}

// Open dials the endpoint runed's tables live on and makes them: runed is
// their OWNER, so it is the daemon that makes them; a reader dials and makes
// nothing (store.OpenOwner).
func Open() (*DB, error) {
	h, err := store.Handle(store.OwnerRuned)
	if err != nil {
		return nil, err
	}
	if err := store.PrepareOwner(context.Background(), h, store.OwnerRuned, runstore.Entities...); err != nil {
		return nil, err
	}
	return OpenOver(h), nil
}

// OpenOver is runed over a handle the caller already holds. It makes nothing:
// whoever dialled the handle decides whether the tables are there.
func OpenOver(h objstore.Store) *DB {
	return &DB{Store: runstore.New(h), obj: h}
}

// OpenMem hands ONE test its own private namespace on the endpoint this process
// dials, with runed's entities made in it. Close drops the namespace.
func OpenMem() (*DB, error) {
	h, release, err := store.TestOwner(store.OwnerRuned, runstore.Entities...)
	if err != nil {
		return nil, err
	}
	d := OpenOver(h)
	d.release = release
	return d, nil
}

// Close ends what the open holds. A runed opened against the instance's own
// tables holds no pool and no namespace, so there is nothing to end.
func (d *DB) Close() error {
	if d.release != nil {
		return d.release()
	}
	return nil
}

// Obj is this database through objstore's two primitives, which is how audit
// reaches it.
func (d *DB) Obj() objstore.Store { return d.obj }
