# DB bootstrap spec (v2) — open

## Problem

CLI `group add` on a fresh instance fails because the database doesn't exist
yet — only `initDatabase()` in the gateway creates it. The bash workaround
duplicates schema DDL inline.

The existing `try/catch ALTER TABLE` pattern has no version tracking: applied
migrations are not recorded, failures are silently swallowed.

## Current pattern

`db.ts:initDatabase()`:

1. `fs.mkdirSync` for store dir
2. `new Database(dbPath)` — creates file
3. `createSchema(db)` — idempotent `CREATE TABLE IF NOT EXISTS`
4. `ALTER TABLE` migrations (try/catch, no version tracking)
5. `migrateJsonState()` — one-time JSON→SQLite migration

DB path: `store/messages.db` (relative to cwd).

## Proposed

### Migrations table

```sql
CREATE TABLE IF NOT EXISTS migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
```

Matches the core/news connector model. `applied_at` is ISO-8601 UTC string
(SQLite has no timestamp type).

### Migration runner

SQLite has no stored procedures, so the condition logic lives in TypeScript:

```typescript
type Migration = { version: number; up: (db: Database.Database) => void };

function runMigrations(db: Database.Database, migrations: Migration[]): void {
  db.exec(`CREATE TABLE IF NOT EXISTS migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
  )`);
  const maxVersion =
    (db.prepare('SELECT max(version) AS v FROM migrations').get() as any)?.v ??
    0;
  for (const m of migrations) {
    if (m.version === maxVersion + 1) {
      db.transaction(() => {
        m.up(db);
        db.prepare(
          'INSERT INTO migrations (version, applied_at) VALUES (?, ?)',
        ).run(m.version, new Date().toISOString());
      })();
    }
  }
}
```

Each migration runs only if its version is exactly `max(version) + 1` —
identical logic to the Python core pattern, no procedures needed. Runs on
every gateway start; no-op when already at latest version.

### `src/schema.ts`

Single module shared by gateway and CLI:

```typescript
export function ensureDatabase(dbPath: string): Database.Database;
```

Internally: mkdir → open → `CREATE TABLE IF NOT EXISTS` baseline schema →
`runMigrations(db, migrations)`. Replaces `initDatabase()` in `db.ts` and
eliminates inline DDL from bash `group add`.

### Migration list example

```typescript
const migrations: Migration[] = [
  {
    version: 1,
    up(db) {
      db.exec(`ALTER TABLE messages ADD COLUMN sender_channel TEXT`);
    },
  },
  // next migration goes here as version: 2, gated on max = 1
];
```

Existing databases start at version 0 (no row in `migrations`). First
migration applies unconditionally (0 + 1 = 1).

## Bash interim

Until `src/schema.ts` ships, bash `group add` continues with inline DDL for
`chats` and `registered_groups` only. Gateway `initDatabase` is idempotent
and adds missing tables/columns on first start.
