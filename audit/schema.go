package audit

// schema.go creates the table this package writes.
//
// audit_log's shape is audit's, so a daemon that has no migrations of its own
// still gets it: authd holds no DDL since its storage moved behind objstore
// (spec 6/37 step 5), and without a table here every Emit would warn "no such
// table" and drop the row while the daemon reported healthy (BUGS F29 is what
// that costs). The daemons that still run their own migration sets create the
// same table from those, and this is the shape they converge on as each one
// moves.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kronael/arizuko/db_utils"
)

// ErrNotPortable is what EnsureTable answers on an engine this package cannot
// serve. audit is SQLite-only in three places at once — `AUTOINCREMENT` in the
// DDL below, `?` placeholders in insertSQL, and LastInsertId on the result —
// so a Postgres database gets a refusal at boot rather than a table nothing can
// write into. Emit warns and DROPS a row it cannot insert, so the portable-DDL
// half on its own would turn this failure into silence.
//
// It is the ownership half of the storage seam, tracked as BUGS L251: audit
// runs its own statements against a daemon's *sql.DB instead of going through
// objstore, and moving audit_log to an entity is what makes it portable.
var ErrNotPortable = errors.New("audit_log is SQLite-only: its DDL, its placeholders and " +
	"its LastInsertId all are, so audit cannot serve Postgres until audit_log is an " +
	"objstore entity (BUGS L251, spec 6/40)")

// createSQL is audit_log and its indexes, idempotent so a database that already
// has them is untouched.
const createSQL = `CREATE TABLE IF NOT EXISTS audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  category        TEXT    NOT NULL,
  action          TEXT    NOT NULL,
  actor           TEXT    NOT NULL,
  actor_sub       TEXT,
  resource        TEXT,
  scope           TEXT,
  surface         TEXT,
  params_summary  TEXT,
  outcome         TEXT    NOT NULL,
  error_msg       TEXT,
  duration_ms     INTEGER,
  turn_id         TEXT,
  folder          TEXT,
  instance        TEXT,
  request_id      TEXT,
  source_ip       TEXT
)`

// createIndexSQL are audit_log's read paths: the time cursor, the two nullable
// filters, and the category/action pair.
var createIndexSQL = []string{
	`CREATE INDEX IF NOT EXISTS audit_log_created_at ON audit_log(created_at)`,
	`CREATE INDEX IF NOT EXISTS audit_log_actor_sub  ON audit_log(actor_sub) WHERE actor_sub IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS audit_log_folder     ON audit_log(folder)    WHERE folder    IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS audit_log_cat_act    ON audit_log(category, action)`,
}

// EnsureTable creates audit_log in db when it is not already there.
func EnsureTable(ctx context.Context, db *sql.DB) error {
	if db_utils.IsPostgres(db) {
		return ErrNotPortable
	}
	if _, err := db.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create audit_log: %w", err)
	}
	for _, stmt := range createIndexSQL {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("index audit_log: %w", err)
		}
	}
	return nil
}
