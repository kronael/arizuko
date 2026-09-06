# db_utils

The one opener, and the migration runner for embedded `.sql` files.

## Purpose

Tiny shared helper used by daemons that own a schema and want
to apply pending `NNNN-*.sql` migrations from an `embed.FS` at
startup. Consumers are the split daemons that each own a DB — `routd`
(`routd.db`), `runed` (`runed.db`), `authd` (`auth.db`), `onbod`
(`onbod.db`) — plus the shared `store/` schema library. Any future
daemon that owns its own DB can call it without dragging in a
migrations framework.

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

- **The SQLite pragma set.** `FileDSN` puts `busy_timeout(5000)` and
  `foreign_keys(on)` in the DSN, because modernc.org/sqlite defaults
  foreign-key enforcement OFF per connection and `database/sql` pools
  many, so a one-shot `PRAGMA` covers one of them. WAL is a hard error,
  not a warning: a store in rollback-journal mode looks identical to a
  healthy one.
- **The Postgres connection cap.** A Postgres connection is a share of
  one server-wide budget; the arithmetic behind the number is at
  `maxOpenConns`. pgx runs on the simple protocol so a transaction-mode
  pooler never hands a transaction a connection missing a statement an
  earlier one prepared.
- **The transient class.** `IsTransient(err)` is what may be retried:
  SQLITE_BUSY, SQLITE_LOCKED, Postgres `40001` and `40P01`, and a
  connection fault the driver can prove reached no server. Everything
  else throws on the first attempt.

## Public API

- `Open(dsn string) (*sql.DB, error)`
- `FileDSN(path string) string`
- `RequireDBFile(path string) error` / `CreateDBFile(path string) error`
- `IsTransient(err error) bool`
- `Migrate(db *sql.DB, fsys embed.FS, dir, service string) error`

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

- `db_utils.go` — the whole public surface

## Related

- `../store/README.md` — the schema this runner currently applies
- `../store/migrations/` — actual `.sql` files
