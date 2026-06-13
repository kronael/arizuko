# db_utils

SQL migration runner for embedded `.sql` files.

## Purpose

Tiny shared helper used by daemons that own a SQLite schema and want
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

## Public API

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

Standard library only (`database/sql`, `embed`).

## Files

- `db_utils.go` — `Migrate` (the entire public surface)

## Related

- `../store/README.md` — the schema this runner currently applies
- `../store/migrations/` — actual `.sql` files
