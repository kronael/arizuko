// Package runed is the execution plane carved out of gated (spec 5/P): the work queue
// and the per-spawn container lifecycle. It is a pure container-spawner — routd hosts
// the agent MCP socket in-process and is the sole message appender; authd is the sole
// token signer. runed mounts the ipc dir into each spawn and manages spawn/steer/kill.
package runed

// db.go is where runed picks its storage driver and makes sure the store can
// answer. It is the composition root and the only file in the daemon that names
// a driver: runed/runstore holds every read and write, so a second engine is a
// second driver and one line here (spec 6/37 step 5).
//
// There is no migration set and no DDL. An entity is a name the driver resolves
// however it must — a table on SQL, nothing at all on an engine with no schema —
// so creating the storage is asking the driver for the entities runstore and
// audit declare, and asking twice is a no-op.

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// ErrNotFound signals an absent item to the HTTP layer (404). It is runstore's,
// named here so a handler comparing against it needs no second import.
var ErrNotFound = runstore.ErrNotFound

// The shapes runed's handlers pass around are runstore's, so the daemon and its
// storage layer name one type each rather than two that have to be copied
// between.
type (
	// Spawn is one container spawn (the execution-session envelope).
	Spawn = runstore.Spawn
	// ActiveSpawn is a live spawn's identifying info for the steer path.
	ActiveSpawn = runstore.ActiveSpawn
	// Session is one session_log entry, as both the dashboard page and routd's
	// federated read project it.
	Session = runstore.Session
)

// DB owns runed.db: execution runtime state with no home in routd. Every read
// and write is runstore's, embedded so a handler calls it directly and this file
// holds no pass-through method to drift.
type DB struct {
	*runstore.Store

	db *sql.DB

	// release closes the pool and drops the database OpenMem reserved. A DB
	// opened against a file leaves the file alone, and Close closes the pool.
	release func() error
}

// Open attaches to an EXISTING runed.db at dir/runed.db. It never creates the
// file — see Create.
func Open(dir string) (*DB, error) {
	return OpenAt(store.OwnerDBPath(dir, store.OwnerRuned))
}

// OpenAt is Open against an explicitly configured target — runed's DATABASE env
// — which may name either engine, because db_utils.Open reads the engine off the
// DSN and nothing here names one (spec 6/37 step 6).
func OpenAt(target string) (*DB, error) {
	if err := db_utils.RequireDB(target); err != nil {
		return nil, err
	}
	return open(target)
}

// Create seeds dir/runed.db when absent. `arizuko create` and the split-cutover
// tool only — every other caller Opens, so a wrong path fails loud instead of
// yielding a prepared DB with no spawn history.
func Create(dir string) (*DB, error) {
	path := store.OwnerDBPath(dir, store.OwnerRuned)
	if err := db_utils.CreateDBFile(path); err != nil {
		return nil, err
	}
	return open(path)
}

// OpenMem opens a fresh, private, empty runed.db for one test.
// db_utils.NewTestDB owns the engine and the DSN, so ARIZUKO_TEST_PG_DSN moves
// this and every other test database together (spec 6/37 step 6). Close releases
// the database.
func OpenMem() (*DB, error) {
	dsn, release, err := db_utils.NewTestDB("runed")
	if err != nil {
		return nil, err
	}
	d, err := open(dsn)
	if err != nil {
		// the caller gets no handle, so the failed open releases the database
		if rerr := release(); rerr != nil {
			return nil, fmt.Errorf("%w (and releasing the database: %v)", err, rerr)
		}
		return nil, err
	}
	d.release = release
	return d, nil
}

func open(dsn string) (*DB, error) {
	sqldb, err := db_utils.Open(dsn)
	if err != nil {
		return nil, err
	}
	if err := ensureStorage(sqldb); err != nil {
		sqldb.Close() //nolint:errcheck // the open already failed
		return nil, err
	}
	return &DB{Store: runstore.New(newObjectStore(sqldb)), db: sqldb}, nil
}

// ensureStorage prepares db to serve runed: every entity runstore reads, and the
// one audit records into.
//
// audit declares its entity beside the code that writes it and every owner
// applies the same list, so a daemon with no migrations of its own still has
// somewhere to record — without which every Emit would warn and drop the row
// while the daemon reported healthy (BUGS F29 is what that costs).
func ensureStorage(db *sql.DB) error {
	ctx := context.Background()
	s := newObjectStore(db)
	if err := runstore.EnsureEntities(ctx, s); err != nil {
		return err
	}
	return audit.EnsureEntities(ctx, s)
}

// newObjectStore is the driver choice, in one place. sqlstore serves both SQLite
// and Postgres off the DSN db_utils.Open already read, so this line does not
// change when the engine does.
func newObjectStore(db *sql.DB) *sqlstore.Store { return sqlstore.New(db) }

func (d *DB) Close() error {
	if d.release != nil {
		return d.release()
	}
	return d.db.Close()
}

// Obj is this database through objstore's two primitives, which is how audit
// reaches it.
func (d *DB) Obj() *sqlstore.Store { return newObjectStore(d.db) }

// SQL returns the raw pool. Its ONE production caller is audit_resource.go,
// which needs a non-nil *store.Store because resreg reads a nil Store as a
// FORWARDER and skips authorization entirely — the same marker authd's
// auditStore passes for the same reason. Nothing here runs a statement on it.
//
// `arizuko migrate-split` and `arizuko migrate-runed` take it too: both are the
// source half of a data move, and the source half is the only half that knows
// the old schema.
func (d *DB) SQL() *sql.DB { return d.db }
