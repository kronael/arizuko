package db_utils

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// RequireDBFile errors unless path already holds a SQLite file. Owner daemons
// call it before sql.Open: SQLite creates a missing file silently and Migrate
// then fills it with a complete empty schema, so a daemon pointed at the wrong
// path — a typo'd mount, an unfinished store/<owner>/ move — boots green as a
// fresh instance holding none of the operator's data (spec 5/16 step 7).
// Creation is an explicit act: CreateDBFile, called by `arizuko create`.
func RequireDBFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s does not exist: an owner daemon never creates its own "+
				"database (run `arizuko create`, or move the existing .db + -wal + -shm here)", path)
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return nil
}

// CreateDBFile makes path's parent directory and an empty file at path when
// absent, leaving an existing file untouched. A zero-byte file IS a valid empty
// SQLite database, so the owner daemon's own Migrate fills in the schema on
// first boot — this seeds the file without linking every migration set into the
// CLI. `arizuko create` and the split-cutover tool only.
func CreateDBFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// FileDSN is the on-disk DSN every owner daemon and adapter opens. foreign_keys
// must ride the DSN: modernc.org/sqlite defaults FK enforcement OFF per
// connection and database/sql pools many, so a one-shot PRAGMA covers one of
// them and ON DELETE CASCADE silently no-ops on the rest.
func FileDSN(path string) string {
	return path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
}

// maxOpenConns caps every Postgres pool. A Postgres connection is a share of one
// server-wide budget, so the number is arithmetic rather than taste.
//
// The floor is what one daemon holds at once. Migrate takes a dedicated
// connection and holds it for a whole migration file, a daemon migrates while it
// serves, a transaction holds a second connection while another goroutine reads,
// and 4 leaves one spare.
//
// The ceiling is the fleet. A full deployment holds 20 pools — routd, runed,
// authd, proxyd, webd and emaid one each; onbod, gitd and slakd two (their own
// database and routd's); dashd three; teled, discd, bskyd, linkd and reditd one
// each — and 24 leaves room for four more adapters without touching this number.
// So arizuko asks a shared server for at most 24*4 = 96 connections. Postgres
// ships max_connections=100 with superuser_reserved_connections=3
// (postgresql.org/docs/17/runtime-config-connection.html), which leaves 97: a
// server that also carries Dex's and LiteLLM's pools (spec 6/37) needs
// max_connections raised, and this is the number to raise it past.
const maxOpenConns = 4

// Open opens dsn and returns a pool. The DSN selects the engine: `postgres://`
// and `postgresql://` open Postgres through pgx, and every other DSN is a SQLite
// path or `file:` URI.
//
// SQLite is opened in WAL mode and the pragma failure is an error, not a
// warning: a store in rollback-journal mode looks identical to a healthy one.
// Postgres is pinged before the handle is returned, so a wrong DSN fails at
// boot rather than on the first query.
func Open(dsn string) (*sql.DB, error) {
	if isPostgresURL(dsn) {
		return openPostgres(dsn)
	}
	if isLibpqKeywordDSN(dsn) {
		return nil, fmt.Errorf("%q is a libpq keyword DSN; write it as a postgres:// URL "+
			"(SQLite would take the whole string as a file path and create a file with that name)", dsn)
	}
	return openSQLite(dsn)
}

func openSQLite(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openPostgres(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres DSN: %w", err)
	}
	// A transaction-mode pooler (PgBouncer, RDS Proxy, Azure's built-in
	// PgBouncer) hands each transaction whichever server connection is free, so
	// a statement prepared on one is missing on the next and PREPARE/DEALLOCATE
	// are refused outright (pgbouncer.org/features.html). The simple protocol
	// names no prepared statement, so a pooled connection carries no state a
	// later transaction depends on.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(maxOpenConns)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return db, nil
}

func isPostgresURL(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// isLibpqKeywordDSN reports whether dsn is Postgres in libpq keyword/value form
// (`host=db user=routd dbname=routd`). Open refuses that form rather than
// guessing: a SQLite path never begins `word=value`, and opening one as a file
// would create a file named after the whole connection string and boot green on
// an empty schema.
func isLibpqKeywordDSN(dsn string) bool {
	head, _, _ := strings.Cut(dsn, " ")
	key, value, ok := strings.Cut(head, "=")
	return ok && key != "" && value != "" && !strings.ContainsAny(key, `/\.:?`)
}

// IsTransient reports whether err is a database fault a retry can clear. It is
// the whole class: everything else — a misconfiguration, missing data, a
// programming error — throws on the first attempt.
//
// SQLite contributes SQLITE_BUSY and SQLITE_LOCKED, which reach a caller only
// after FileDSN's busy_timeout has already waited. Postgres contributes 40001
// serialization_failure and 40P01 deadlock_detected
// (postgresql.org/docs/17/errcodes-appendix.html), plus the connection faults a
// managed failover produces when it re-points the endpoint. Deadlock has no
// SQLite counterpart: one writer per file means two transactions can never wait
// on each other.
//
// A dropped connection counts only when pgconn can prove the statement never
// reached the server. Retrying a statement that may already have run is a
// second write, not a retry.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	var liteErr *sqlite.Error
	if errors.As(err, &liteErr) {
		switch liteErr.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			return true
		}
		return false
	}
	return errors.Is(err, driver.ErrBadConn) || pgconn.SafeToRetry(err)
}

// checkVersions rejects a migration set whose 4-digit prefixes are not exactly
// 1..N. Migrate skips any file whose version is <= the version it just applied,
// so two files sharing a number apply only the first in sort order and DROP the
// second — with no error, on every instance. That cost the target_folder column
// (BUGS L4): store/migrations grew a second 0084 and the column silently never
// existed. A gap is the same class of mistake caught at the same moment.
func checkVersions(files []string) error {
	for i, f := range files {
		if len(f) < 4 {
			return fmt.Errorf("migration %q: filename too short for 4-digit version", f)
		}
		ver, err := strconv.Atoi(f[:4])
		if err != nil {
			return fmt.Errorf("migration %q: non-numeric version prefix: %w", f, err)
		}
		if ver != i+1 {
			if i > 0 && f[:4] == files[i-1][:4] {
				return fmt.Errorf("migration %q duplicates version %d of %q; renumber it",
					f, ver, files[i-1])
			}
			return fmt.Errorf("migration %q is version %d but must be %04d; versions run 1..N with no gaps",
				f, ver, i+1)
		}
	}
	return nil
}

// migrateLockKey scopes every migrator on one Postgres database to a single
// advisory lock. The value is arbitrary; only its uniqueness inside arizuko
// matters. pg_advisory_xact_lock releases at commit, which is what a
// transaction-mode pooler allows and a session lock is not.
const migrateLockKey = 6103702026090601

// dialect is the SQL Migrate's own bookkeeping differs in between engines. The
// migration runner is the one place in arizuko that speaks both: it applies the
// SQLite sets an instance already carries and the Postgres baselines an instance
// converts to (spec 6/37 step 7). Every other statement in the tree has one
// dialect.
type dialect struct {
	// begin opens the critical section — one migrator at a time, so the version
	// recheck inside it decides which one applies the file.
	begin []string
	// selectVersion reads the highest applied version for one service.
	selectVersion string
	// insertVersion records an applied migration.
	insertVersion string
}

var sqliteDialect = dialect{
	begin:         []string{"BEGIN EXCLUSIVE"},
	selectVersion: "SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?",
	insertVersion: "INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
}

var postgresDialect = dialect{
	begin: []string{
		"BEGIN",
		fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", migrateLockKey),
	},
	selectVersion: "SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=$1",
	insertVersion: "INSERT INTO migrations (service, version, applied_at) VALUES ($1,$2,$3)",
}

func dialectFor(db *sql.DB) dialect {
	if IsPostgres(db) {
		return postgresDialect
	}
	return sqliteDialect
}

// IsPostgres reports whether db is a Postgres pool. The DSN chose the engine at
// Open, and a caller that must EMIT different SQL — the migration runner below,
// objstore's SQL driver — reads the choice back from the pool rather than
// re-parsing a DSN it no longer holds. One reader of the driver type, here,
// beside the opener that set it.
func IsPostgres(db *sql.DB) bool {
	_, ok := db.Driver().(*stdlib.Driver)
	return ok
}

// beginLocked opens the critical section on conn. The caller owns the ROLLBACK
// or COMMIT that ends it.
func (d dialect) beginLocked(ctx context.Context, conn *sql.Conn) error {
	for _, stmt := range d.begin {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// rowQuerier is the one statement Migrate issues from both the pool and a
// pinned connection.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readVersion(ctx context.Context, q rowQuerier, d dialect, service string) (int, error) {
	var max int
	err := q.QueryRowContext(ctx, d.selectVersion, service).Scan(&max)
	return max, err
}

// createVersionTable creates the bookkeeping table inside the critical section.
// Two daemons boot together and both reach here, and Postgres's CREATE TABLE IF
// NOT EXISTS is not race-free — concurrent creators collide on the catalog's
// unique index rather than one of them seeing the other's table.
func createVersionTable(ctx context.Context, db *sql.DB, d dialect) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := d.beginLocked(ctx, conn); err != nil {
		return fmt.Errorf("create migrations table: lock: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return fmt.Errorf("create migrations table: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("create migrations table: commit: %w", err)
	}
	return nil
}

// migrateReport records what one migrate call did with the files it considered.
// Its two lists are the two ways a pending file stops being pending: this call
// ran it, or another opener recorded it while this call waited on the critical
// section. A file whose version was already recorded when this call read the
// version is in neither list — that is the ordinary nothing-to-do case a
// restart takes.
type migrateReport struct {
	applied []string
	yielded []string
}

// Migrate applies pending `NNNN-*.sql` migrations from fsys/dir. Versions
// must be sequential with no gaps. Tracked in the `migrations` table
// keyed on (service, version).
func Migrate(db *sql.DB, fsys embed.FS, dir, service string) error {
	_, err := migrate(db, fsys, dir, service)
	return err
}

// migrate is Migrate, reporting which files it ran and which it yielded to a
// concurrent opener. The report is what tells the concurrency test that the
// recheck inside the critical section ran at all: the end state is identical
// whether six openers raced for a file or one applied every file before the
// other five read the version.
func migrate(db *sql.DB, fsys embed.FS, dir, service string) (migrateReport, error) {
	var rep migrateReport
	ctx := context.Background()
	d := dialectFor(db)

	// The version read is the probe for the bookkeeping table, so a database
	// that already carries it takes no write lock at boot. Reaching for the lock
	// unconditionally queues every daemon behind whichever one is inside a slow
	// migration, and the rest fail on busy_timeout.
	max, err := readVersion(ctx, db, d, service)
	if err != nil {
		if cerr := createVersionTable(ctx, db, d); cerr != nil {
			return rep, cerr
		}
		if max, err = readVersion(ctx, db, d, service); err != nil {
			return rep, fmt.Errorf("read migration version: %w", err)
		}
	}

	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return rep, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if err := checkVersions(files); err != nil {
		return rep, err
	}

	for _, f := range files {
		ver, err := strconv.Atoi(f[:4])
		if err != nil {
			return rep, fmt.Errorf("migration %q: non-numeric version prefix: %w", f, err)
		}
		if ver <= max {
			continue
		}
		raw, err := fsys.ReadFile(dir + "/" + f)
		if err != nil {
			return rep, fmt.Errorf("%s: read: %w", f, err)
		}
		// Serialise concurrent openers of the same DB (e.g. webd + proxyd
		// both racing to apply the same migration). The critical section holds a
		// write lock for the duration, so the second opener blocks until the
		// first commits, then sees cur >= ver and skips.
		conn, err := db.Conn(ctx)
		if err != nil {
			return rep, err
		}
		if err := d.beginLocked(ctx, conn); err != nil {
			conn.Close()
			return rep, fmt.Errorf("%s: exclusive lock: %w", f, err)
		}
		cur, err := readVersion(ctx, conn, d, service)
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return rep, fmt.Errorf("%s: recheck version: %w", f, err)
		}
		if cur >= ver {
			// another process applied this migration while we were waiting
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			max = cur
			rep.yielded = append(rep.yielded, f)
			continue
		}
		if ver != cur+1 {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return rep, fmt.Errorf("migration gap: expected %d, got %d", cur+1, ver)
		}
		if _, err := conn.ExecContext(ctx, string(raw)); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return rep, fmt.Errorf("%s: %w", f, err)
		}
		if _, err := conn.ExecContext(ctx, d.insertVersion,
			service, ver, time.Now().Format(time.RFC3339)); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return rep, fmt.Errorf("%s: record: %w", f, err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			conn.Close()
			return rep, fmt.Errorf("%s: commit: %w", f, err)
		}
		conn.Close()
		max = ver
		rep.applied = append(rep.applied, f)
	}
	return rep, nil
}
