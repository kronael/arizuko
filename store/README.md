# store

The access layer for the tables the split topology shares.

## Purpose

`store` is a library, not a daemon. It provides the `Open`/`Migrate` call
and typed accessors for the tables shared across daemons (`messages.db` in
the split is owned by routd; `onbod.db` by onbod; `auth.db` by authd —
each runs its own migrations). WAL mode + 5 s busy timeout tolerate
concurrent readers on the same file.

## Two tables are entities, and the rest are still SQL

`pane_sessions` and `secret_use_log` hold no statement: they are objstore
entities, declared in `entities.go` and read and written through objstore's
two primitives (spec [`6/37`](../specs/6/37-store-model.md) step 7). They are
the two whose whole surface this package owns — nothing outside it runs a
statement against either, and neither is a resreg resource. Every other table
here still reaches the database through SQL.

During the move a `*Store` holds BOTH a `*sql.DB` and, through `Store.Obj`,
the driver over the same pool. Each table has exactly one implementation at
every moment: a moved one reads through objstore, an unmoved one through SQL.
There is no fallback and no dual read.

An entity keeps the NAME of the table it replaces, so a database carrying rows
is moved by `arizuko migrate-store` and the driver refuses to serve the entity
until it has been. The two files say what their coordinates cost:
`pane_sessions.go` takes one constant partition and the (team, user, thread)
triple as its sort key; `secret_use_log.go` takes audit_log's shape, and says
plainly that no read decided it because it has no reader.

## Public API

- `Open(dir string) (*Store, error)` — opens `<dir>/messages.db`, runs migrations
- `OpenMem() (*Store, error)` — a private test database on the FROZEN pre-split chain
- `EnsureEntities(ctx, driver)` — the entity half, which every opener also asks for
- `Migrate(db *sql.DB) error` — migrations only (test fixtures)
- `New(db *sql.DB) *Store` — wrap an existing connection

Primary methods (by domain):

- Messages: `PutMessage`, `NewMessages`, `MessagesSince`, `EnrichMessage`, `MarkMessagesErrored`, `DeleteErroredMessages`, `LatestSource`
- Groups: `PutGroup`, `DeleteGroup`, `AllGroups`, `GroupByFolder`, `SetGroupModel`, `GroupUsageBulk`
- Sessions: `GetSession`, `SetSession`, `RecordSession`, `EndSession`, `RecentSessions`
- Sticky: `SetStickyGroup`, `GetStickyGroup`, `SetStickyTopic`, `GetStickyTopic`
- Auth: `CreateAuthUser` — one row per sub in `user_profiles`. Account linking lives in authd (`auth.db`'s `auth_users` + `oauth_identities`), not here.
- ACL (spec 6/9): `AddACLRow`, `RemoveACLRow`, `ListACL`, `ListACLByScope`, `ACLRowsFor`, `ACLWildcardRows` (scope listing is `auth.UserScopes` — a projection of these, not a second query)
- Membership: `AddMembership`, `RemoveMembership`, `Members`, `Ancestors` — roles + JID→sub claims (`acl_membership` table)
- Tasks: `CreateTask`, `GetTask`, `ListTasks`, `UpdateTask`, `DeleteTask`, `TaskRunLogs`
- Secrets: `SetSecret`, `GetSecret`, `ListSecrets`, `DeleteSecret`,
  `FolderSecretsResolved` (walk parents → root), `UserSecrets`
  (per-user overlay), `CountPlaintextSecrets` + `EncryptPlaintextSecrets`
  (seal legacy rows in place; driven by `arizuko secret <inst> seal-legacy`,
  never on boot — the boot-time `PurgeUnencryptedSecrets` they replaced wiped
  every secret on each startup).
  AES-256-GCM encrypted at rest under the `v2:` prefix; key derived via SHA-256
  from the `SECRETS_KEY` env var, which is REQUIRED — every write path refuses
  without it rather than storing plaintext (BUGS S2). A legacy un-prefixed row
  still reads correctly until it is sealed.
- Routes, onboarding, topics — see source

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
