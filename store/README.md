# store

SQLite access layer. Owns `messages.db` schema and all migrations.

## Purpose

Single writer of the shared database. `Migrate` runs every migration in
`migrations/` on `Open`. Other daemons connect to the same DB but must
wait for `gated` to finish migrating on startup (WAL mode + 5s busy
timeout tolerate the handoff).

## Public API

- `Open(dir string) (*Store, error)` — opens `<dir>/messages.db`, runs migrations
- `OpenWithSecret(dir, authSecret string) (*Store, error)` — `Open` + AES-GCM cipher for the secrets API
- `OpenMem() (*Store, error)` / `OpenMemWithSecret(authSecret)` — in-memory (tests)
- `Migrate(db *sql.DB) error` — migrations only (test fixtures)
- `New(db *sql.DB) *Store` — wrap an existing connection

Primary methods (by domain):

- Messages: `PutMessage`, `NewMessages`, `MessagesSince`, `EnrichMessage`, `MarkMessagesErrored`, `DeleteErroredMessages`, `LatestSource`
- Groups: `PutGroup`, `DeleteGroup`, `AllGroups`, `GroupByFolder`, `GroupBySlinkToken`
- Sessions: `GetSession`, `SetSession`, `RecordSession`, `EndSession`, `RecentSessions`
- Sticky: `SetStickyGroup`, `GetStickyGroup`, `SetStickyTopic`, `GetStickyTopic`
- Auth: `CreateAuthUser`, `AuthUserBySub`, `CreateAuthSession`, `UserGroups`, `Grant`, `Ungrant`, `Grants`
- Tasks: `CreateTask`, `GetTask`, `ListTasks`, `UpdateTask`, `DeleteTask`, `TaskRunLogs`
- Grants/rules: `GetGrants(folder)`, `SetGrants(folder, rules)`
- Secrets: `SetSecret`, `GetSecret`, `ListSecrets`, `DeleteSecret`,
  `FolderSecretsResolved` (walk parents → root), `UserSecrets`
  (per-user overlay). Plaintext is AES-GCM-encrypted with a key
  derived from `AUTH_SECRET`; methods return
  `ErrSecretCipherNotConfigured` on a `Open`-only Store.
- Routes, system messages, onboarding, topics — see source

## Dependencies

- `core`, `db_utils`, `groupfolder`, `modernc.org/sqlite`

## Files

- `store.go` — `Open`, `OpenWithSecret`, `Migrate`, connection setup
- `messages.go`, `groups.go`, `sessions.go`, `tasks.go`, `auth.go`, `grants.go`, `routes.go`, `onboarding.go`, `invites.go`, `secrets.go`, `inspect.go`
- `migrations/NNNN-*.sql` — numbered migrations

## Related docs

- `ARCHITECTURE.md` (SQLite Schema)
