# DB bootstrap spec (v2)

## Problem

CLI `group add` on a fresh instance fails because the
database doesn't exist yet — only `initDatabase()` in the
gateway creates it. The bash workaround duplicates schema
DDL inline.

## Current pattern

`db.ts:initDatabase()`:

1. `fs.mkdirSync` for store dir
2. `new Database(dbPath)` — creates file
3. `createSchema(db)` — idempotent `CREATE TABLE IF NOT EXISTS`
4. `ALTER TABLE` migrations (try/catch, no version tracking)
5. `migrateJsonState()` — one-time JSON→SQLite migration

DB path: `store/messages.db` (relative to cwd).

## Proposed: extract schema into standalone module

### `src/schema.ts`

Export the schema DDL and migration functions so both the
gateway and CLI can create/migrate the database without
importing the full gateway config.

```typescript
export function createSchema(db: Database.Database): void;
export function runMigrations(db: Database.Database): void;
export function ensureDatabase(dbPath: string): Database.Database;
```

`ensureDatabase` = mkdir + open + createSchema + runMigrations.
Reusable by gateway (`initDatabase`) and CLI (`group add`).

### Migration tracking

Replace try/catch ALTER TABLE with a `schema_version` table:

```sql
CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER PRIMARY KEY
);
```

Migrations are numbered functions. `runMigrations` applies
unapplied versions in order. Existing DBs get version 0
(baseline).

### CLI usage

The nest-commander CLI (v1 spec) imports `ensureDatabase`
directly. No more inline DDL in bash.

## Bash interim

Until v1 CLI ships, the bash `group add` creates the DB
with inline DDL for `chats` and `registered_groups` only.
Gateway's `initDatabase` is idempotent and will add any
missing tables/columns on first start.
