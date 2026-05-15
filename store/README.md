# store

SQLite access layer. Owns `messages.db` schema and all migrations.

## Purpose

Single writer of the shared database. `Migrate` runs every migration in
`migrations/` on `Open`. Other daemons connect to the same DB but must
wait for `gated` to finish migrating on startup (WAL mode + 5s busy
timeout tolerate the handoff).

## Public API

- `Open(dir string) (*Store, error)` — opens `<dir>/messages.db`, runs migrations
- `OpenMem() (*Store, error)` — in-memory (tests)
- `Migrate(db *sql.DB) error` — migrations only (test fixtures)
- `New(db *sql.DB) *Store` — wrap an existing connection

Primary methods (by domain):

- Messages: `PutMessage`, `NewMessages`, `MessagesSince`, `EnrichMessage`, `MarkMessagesErrored`, `DeleteErroredMessages`, `LatestSource`
- Groups: `PutGroup`, `DeleteGroup`, `AllGroups`, `GroupByFolder`, `GroupBySlinkToken`
- Sessions: `GetSession`, `SetSession`, `RecordSession`, `EndSession`, `RecentSessions`
- Sticky: `SetStickyGroup`, `GetStickyGroup`, `SetStickyTopic`, `GetStickyTopic`
- Auth: `CreateAuthUser`, `AuthUserBySub`, `AuthUserByUsername`, `CanonicalSub`, `LinkSubToCanonical`, `LinkedSubs`, `CreateAuthSession`, `AuthSession`, `DeleteAuthSession`
- ACL (spec 6/9): `AddACLRow`, `RemoveACLRow`, `ListACL`, `ACLRowsFor`, `ACLWildcardRows`, `UserScopes`
- Membership: `AddMembership`, `RemoveMembership`, `Members`, `Ancestors` — roles + JID→sub claims (`acl_membership` table)
- Tasks: `CreateTask`, `GetTask`, `ListTasks`, `UpdateTask`, `DeleteTask`, `TaskRunLogs`
- Secrets: `SetSecret`, `GetSecret`, `ListSecrets`, `DeleteSecret`,
  `FolderSecretsResolved` (walk parents → root), `UserSecrets`
  (per-user overlay). v1 stores plaintext (operator trusts disk +
  FS perms; encryption at rest deferred per spec 9/11).
- Routes, system messages, onboarding, topics — see source

`messages.is_observed` (migration 0054) marks rows stored under a
folder via a `#observe` route target — they do not fire a turn but are
surfaced to the next trigger turn's `<observed>` block. `routes.target`
is `<folder>[#<mode>]`; per-route caps `observe_window_messages` and
`observe_window_chars` override the env defaults. Spec:
`specs/6/B-route-mode-ingestion.md`.

## Dependencies

- `core`, `db_utils`, `groupfolder`, `modernc.org/sqlite`

## Files

- `store.go` — `Open`, `OpenMem`, `Migrate`, connection setup
- `messages.go`, `groups.go`, `sessions.go`, `tasks.go`, `auth.go`, `acl.go`, `membership.go`, `routes.go`, `onboarding.go`, `invites.go`, `secrets.go`, `inspect.go`
- `migrations/NNNN-*.sql` — numbered migrations

## Related docs

- `ARCHITECTURE.md` (SQLite Schema)
