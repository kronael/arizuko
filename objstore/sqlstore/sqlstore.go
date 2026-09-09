// Package sqlstore is objstore's SQL driver: it generates its own statements
// and emits them in two dialects, SQLite and Postgres (spec 6/37 step 8).
//
// There is no SQLite-specific driver and no Postgres-specific driver. Serving
// both is a dialect switch inside this one — placeholders, the row lock a check
// takes, the document merge, the DDL type of the document column — because the
// statements are generated rather than written. That is what turns the dialect
// distance from 156 hand-written DDL files into one generator with two
// emitters.
//
// An entity is one table of three columns: the partition, the sort key, and the
// document. A driver for a key-value engine stores the same three and needs no
// DDL at all, which is why the schema lives here and not on the interface —
// schema is driver-local (spec 6/40).
package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// Store is objstore.Store over a database/sql pool.
type Store struct {
	db      *sql.DB
	dialect dialect
}

// compile-time proof that this driver is the interface and nothing more.
var _ objstore.Store = (*Store)(nil)

// New wraps a pool db_utils.Open returned. The DSN already chose the engine;
// the emitter follows it.
func New(db *sql.DB) *Store {
	return &Store{db: db, dialect: dialectFor(db)}
}

// CreateEntity creates the table one entity lives in. It is driver-local and
// deliberately off objstore.Store: an engine with no DDL has nothing to
// implement, and a caller that reached for this through the interface would be
// writing code only one driver can run.
func (s *Store) CreateEntity(ctx context.Context, entity string) error {
	if err := checkIdent("entity", entity); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+entity+" ("+
		"part TEXT NOT NULL, "+
		"sort TEXT NOT NULL, "+
		"attrs "+s.dialect.attrsColumn+", "+
		"PRIMARY KEY (part, sort))")
	if err != nil {
		return fmt.Errorf("create entity %s: %w", entity, err)
	}
	// IF NOT EXISTS is silent about a table of ANOTHER shape under the same
	// name, and an entity keeps the name of the table it replaces — so a
	// database whose data has not been moved yet would boot healthy here and
	// fail on every write instead, which for audit_log means a dropped row and
	// a warning nobody reads (BUGS F29). The probe selects the three columns and
	// no rows: it costs one statement at boot and it names the file.
	if _, err := s.db.ExecContext(ctx,
		"SELECT part, sort, attrs FROM "+entity+" WHERE 1 = 0"); err != nil {
		empty, cerr := s.emptyTable(ctx, entity)
		if cerr != nil || !empty {
			return fmt.Errorf("%s is a table of another shape, so the entity cannot be served "+
				"from it; move its rows first (arizuko migrate-*): %w", entity, err)
		}
		// AN EMPTY TABLE OF THE OTHER SHAPE IS A SCHEMA CHAIN'S LEFTOVER, not
		// data. A service's migration files are frozen once applied, so a chain
		// that created a table before it became an entity goes on creating it in
		// every database born after — routd's 0001 still creates turn_context,
		// turn_results, group_watchers, task_run_logs and user_profiles. Refusing
		// there would mean no new instance could open at all, and the refusal
		// above exists to protect ROWS. There are none, so the table goes and the
		// entity takes the name.
		//
		// The count and the drop are two statements, so a writer could insert
		// between them. Nothing does: this runs while the daemon that owns the
		// table is still opening it, before it serves anything.
		if _, derr := s.db.ExecContext(ctx, "DROP TABLE "+entity); derr != nil {
			return fmt.Errorf("%s is an empty table of another shape and the entity has to "+
				"take its name: %w", entity, derr)
		}
		return s.CreateEntity(ctx, entity)
	}
	return nil
}

// emptyTable reports whether a table holds no rows, which is what decides
// whether the shape refusal above is protecting anything.
func (s *Store) emptyTable(ctx context.Context, entity string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+entity).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// checkIdent refuses a name this package would write into a statement.
//
// An entity is a table name and an attribute is a key inside a JSON path, and
// neither can be a bind parameter: a table name is not a value, and Postgres
// cannot infer the type of an untyped literal on the right of ->>. So both are
// interpolated, and this is the guard that makes that safe — one validator for
// both, because there is one hazard.
func checkIdent(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if len(name) > 63 { // Postgres truncates an identifier past 63 bytes
		return fmt.Errorf("%s name %q is longer than 63 bytes", kind, name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '_':
		case (r >= '0' && r <= '9') && i > 0:
		default:
			return fmt.Errorf("%s name %q is not lower-case letters, digits and underscore "+
				"starting with a letter or underscore; it is written into a statement, "+
				"so anything else is an injection", kind, name)
		}
	}
	return nil
}

// stmt accumulates one generated statement and the values it binds, so the
// placeholder and its argument are produced at the same place and can never
// drift out of order.
type stmt struct {
	d    dialect
	sql  []byte
	args []any
}

// write appends literal SQL.
func (b *stmt) write(text string) { b.sql = append(b.sql, text...) }

// bind appends the placeholder for v and records v.
func (b *stmt) bind(v any) { b.write(b.mark(v)) }

// mark records v and RETURNS its placeholder instead of appending it, for the
// one caller that has to wrap the placeholder in an expression.
func (b *stmt) mark(v any) string {
	b.args = append(b.args, v)
	return b.d.placeholder(len(b.args))
}

func (b *stmt) String() string { return string(b.sql) }
