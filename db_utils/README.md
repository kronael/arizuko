# db_utils

The one opener, and the migration runner for embedded `.sql` files.

## Purpose

Tiny shared helper: open a database, and apply pending `NNNN-*.sql`
migrations from an `embed.FS` at startup. Two owners still run a chain —
`routd/migrations/` and `store/migrations/` — and they run it only on a
non-served instance. runed, authd and onbod carry no chain at all: their
tables are objstore entities the driver creates, so there is nothing numbered
to apply.

Separate from `store/` because the runner has zero knowledge of any
specific schema — it just applies numbered files and records what it
applied. `store/` owns the schema _content_ (the SQL files,
table accessors, query helpers); `db_utils` owns the _mechanism_
(numbered apply, gap detection, per-service bookkeeping). Splitting
keeps `store/`'s public surface focused on data access and lets a
hypothetical second schema-owning daemon reuse the runner without
importing `store/`.

## The opener

`Open(dsn)` is the only place in the tree that opens a database, and
`opener_test.go` walks the source and fails the build on any other
`sql.Open`. It selects the engine from the DSN — `postgres://` and
`postgresql://` reach Postgres through `pgx`, everything else is a
SQLite path or `file:` URI — and it owns the three things a call site
must not each decide for itself:

- **The SQLite pragma set.** `Open` puts `busy_timeout(5000)` and
  `foreign_keys(on)` in the DSN, because modernc.org/sqlite defaults
  foreign-key enforcement OFF per connection and `database/sql` pools
  many, so a one-shot `PRAGMA` covers one of them. A caller passes the
  target it was configured with and builds no DSN, which is what lets
  `DATABASE=postgres://…` move a daemon without editing a call site. A
  target that already names a pragma keeps its own value: the driver
  sorts the set before applying it, so naming one twice is not an
  override. WAL is a hard error, not a warning: a store in
  rollback-journal mode looks identical to a healthy one.
- **The Postgres connection cap.** A Postgres connection is a share of
  one server-wide budget; the arithmetic behind the number is at
  `maxOpenConns`. pgx runs on the simple protocol so a transaction-mode
  pooler never hands a transaction a connection missing a statement an
  earlier one prepared.
- **The transient class.** `IsTransient(err)` is what may be retried:
  SQLITE_BUSY, SQLITE_LOCKED, Postgres `40001` and `40P01`, and a
  connection fault the driver can prove reached no server. Everything
  else throws on the first attempt.

## The door every daemon has

A daemon is configured with a TARGET, never with an engine: `DATABASE`
names its own database and `DB_PATH` / `<OWNER>_DB_PATH` a sibling's,
and any of them may be a `postgres://` URL or a SQLite path. `Open`
reads the engine off it, so nothing in a daemon changes when the engine
does (spec `6/37` step 6).

`RequireDB(target)` is the guard that runs before `Open`. A SQLite
target must be a file that is already there, because SQLite creates a
missing one silently and the owner's schema step then fills it with a
complete empty schema — a daemon pointed at a typo'd mount boots green
holding none of the operator's data (spec `5/16` step 7). A Postgres
server refuses a database that is not there and `Open` pings before it
returns a handle, so on that engine the guard is the engine's own and
`RequireDB` asks nothing further.

`TableExists(db, name)` is the one catalog probe in the tree: SQLite
answers from `sqlite_master` and Postgres from `pg_catalog`, so a caller
that wrote either query by hand would boot on one engine only.

## The database a test opens

A test asks this package for a database the same way a daemon does, by
DSN, so one environment variable moves the whole suite between engines
(spec `6/37` step 6). `ARIZUKO_TEST_PG_DSN` is that variable: unset, a
test database is a private SQLite one held in memory; set to a
`postgres://` URL, each is a fresh database created on that server and
dropped when the test finishes.

`dbtest.Open(t)` is how a `_test.go` reaches it — `opener_test.go`
fails the build on a direct `sql.Open` in a test file too, so a new
test cannot quietly add a twenty-seventh place to edit. The three
`OpenMem` builders (`store`, `routd`, `runed`) call `NewTestDB` from
their own package, because a production package must not link
`testing`.

## Public API

- `Open(target string) (*sql.DB, error)`
- `RequireDB(target string) error` / `CreateDBFile(path string) error`
- `TableExists(db *sql.DB, name string) (bool, error)`
- `IsTransient(err error) bool`
- `Migrate(db *sql.DB, fsys embed.FS, dir, service string) error`
- `NewTestDB(name string) (dsn string, release func() error, err error)`
  and `OpenTestDB(name string)`, which is `NewTestDB` plus `Open`
- `TestPGDSNEnv` — the name of the environment variable above

`service` is the bookkeeping key written into the `migrations` table
so multiple schemas can share the same DB without colliding on
version numbers. `store/` passes `"store"`; a future daemon would
pass its own name.

## Migration file convention

- Files live under `dir` inside `fsys` and are named
  `NNNN-<summary>.sql` (4-digit zero-padded version, then a hyphen,
  then a free-form slug). Non-`.sql` siblings are ignored.
- Versions must be strictly sequential — `Migrate` errors on the
  first gap (`expected N, got N+1`). No "skip a number to reserve a
  slot" pattern; renumber instead.
- Each file is applied inside one transaction together with the
  `INSERT INTO migrations` bookkeeping row. Partial application is
  impossible — either the file ran and was recorded, or neither.
- Already-applied versions (recorded `<= max(version)` for this
  `service`) are silently skipped.

Example layout (from `store/migrations/`):

```
0001-initial-schema.sql
0002-jid-format.sql
0003-reply-to-id.sql
...
```

## Bookkeeping table

Created on first call if absent:

```sql
CREATE TABLE migrations (
  service    TEXT NOT NULL,
  version    INTEGER NOT NULL,
  applied_at TEXT NOT NULL,
  PRIMARY KEY (service, version)
);
```

## Dependencies

`database/sql` and `embed`, plus the two drivers: `modernc.org/sqlite`
and `github.com/jackc/pgx/v5`.

## Files

- `db_utils.go` — the opener, the transient class and the migration runner
- `testdb.go` — the database a test opens, on either engine
- `dbtest/` — the `*testing.T` wrapper over it

## Related

- `../store/README.md` — the schema this runner currently applies
- `../store/migrations/` — actual `.sql` files
