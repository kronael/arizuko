# store

SQLite access layer. Shared schema library for the split topology.

## Purpose

`store` is a library, not a daemon. It provides the `Open`/`Migrate` call
and typed accessors for the tables shared across daemons (`messages.db` in
the split is owned by routd; `onbod.db` by onbod; `auth.db` by authd —
each runs its own migrations). WAL mode + 5 s busy timeout tolerate
concurrent readers on the same file.

## Public API

- `Open(dir string) (*Store, error)` — opens `<dir>/messages.db`, runs migrations
- `OpenMem() (*Store, error)` — in-memory (tests)
- `Migrate(db *sql.DB) error` — migrations only (test fixtures)
- `New(db *sql.DB) *Store` — wrap an existing connection

Primary methods (by domain):

- Messages: `PutMessage`, `NewMessages`, `MessagesSince`, `EnrichMessage`, `MarkMessagesErrored`, `DeleteErroredMessages`, `LatestSource`
- Groups: `PutGroup`, `DeleteGroup`, `AllGroups`, `GroupByFolder`, `SetGroupModel`, `GroupUsageBulk`
- Sessions: `GetSession`, `SetSession`, `RecordSession`, `EndSession`, `RecentSessions`
- Sticky: `SetStickyGroup`, `GetStickyGroup`, `SetStickyTopic`, `GetStickyTopic`
- Auth: `CreateAuthUser`, `AuthUserBySub`, `AuthUserByUsername`, `CanonicalSub`, `LinkSubToCanonical`, `LinkedSubs`, `CreateAuthSession`, `AuthSession`, `DeleteAuthSession`
- ACL (spec 6/9): `AddACLRow`, `RemoveACLRow`, `ListACL`, `ListACLByScope`, `ACLRowsFor`, `ACLWildcardRows`, `UserScopes`
- Membership: `AddMembership`, `RemoveMembership`, `Members`, `Ancestors` — roles + JID→sub claims (`acl_membership` table)
- Tasks: `CreateTask`, `GetTask`, `ListTasks`, `UpdateTask`, `DeleteTask`, `TaskRunLogs`
- Secrets: `SetSecret`, `GetSecret`, `ListSecrets`, `DeleteSecret`,
  `FolderSecretsResolved` (walk parents → root), `UserSecrets`
  (per-user overlay), `PurgeUnencryptedSecrets` (called on startup
  when key set; removes rows without `v1:` prefix).
  AES-256-GCM encrypted at rest; key derived via SHA-256 from
  `SECRETS_KEY` env var (falls back to `AUTH_SECRET`). Plaintext
  rows are rejected on read and purged on startup.
- Routes, system messages, onboarding, topics — see source

`messages.is_observed` (migration 0054) marks rows stored under a
folder via a `#observe` route target — they do not fire a turn but are
surfaced to the next trigger turn's `<observed>` block. `routes.target`
is `<folder>[#<mode>]`; per-route caps `observe_window_messages` and
`observe_window_chars` override the env defaults. Spec:
`specs/5/B-route-mode-ingestion.md`.

`sessions` (migration 0055) carries topic lineage: `parent_topic`,
`forked_at`, `observed_cursor`. `EnsureTopicLineage` seeds a row
idempotently on first turn; `ForkTopic` branches explicitly. Parent
context arrives via a `cp` of the parent's Claude Code session jsonl
(`container.CopySession`) — no inline injection. `ObservedSince` feeds
the `<observed>` block, advancing each topic's cursor independently.
Spec: `specs/5/F-topic-lineage.md`.

## Dependencies

- `core`, `db_utils`, `groupfolder`, `modernc.org/sqlite`

## Files

- `store.go` — `Open`, `OpenMem`, `Migrate`, connection setup
- `messages.go`, `groups.go`, `sessions.go`, `tasks.go`, `auth.go`, `acl.go`, `membership.go`, `routes.go`, `onboarding.go`, `invites.go`, `secrets.go`, `inspect.go`
- `migrations/NNNN-*.sql` — numbered migrations

## Related docs

- `ARCHITECTURE.md` (SQLite Schema)
