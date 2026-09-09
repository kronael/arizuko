# store

The access layer for the tables the split topology shares.

## Purpose

`store` is a library, not a daemon. It provides the `Open`/`Migrate` call
and typed accessors for the tables shared across daemons (`messages.db` in
the split is owned by routd; `onbod.db` by onbod; `auth.db` by authd —
each runs its own migrations). WAL mode + 5 s busy timeout tolerate
concurrent readers on the same file.

## Which tables are entities, and which are still SQL

`entities.go` names every table this package holds as an objstore entity,
read and written through objstore's two primitives (spec
[`6/37`](../specs/6/37-store-model.md) step 7): `pane_sessions`,
`secret_use_log` and `counters`, routd's thirteen registered resources,
onbod's three, and the hot tier's five — `chats`, `sessions`,
`chat_reply_state`, `chat_proactive` and `system_messages`. Every other table
here still reaches the database through SQL, `messages` and its FTS index
foremost: ranked search leaves the store and becomes its own service, so that
one waits for the decision rather than for a port.

During the move a `*Store` holds BOTH a `*sql.DB` and, through `Store.Obj`,
the driver over the same pool. Each table has exactly one implementation at
every moment: a moved one reads through objstore, an unmoved one through SQL.
There is no fallback and no dual read.

An entity keeps the NAME of the table it replaces, so a database carrying rows
is moved by `arizuko migrate-store`, `migrate-routd` or `migrate-hot`, and the
driver refuses to serve the entity until it has been. Each file argues its own
coordinate in its header: what the partition costs, which reads become the
reader's work, and which attribute is ordered and why.

The two cursors are the reason the hot tier moved together.
`chats.agent_cursor` and `sessions.observed_cursor` are copies of a
`messages.timestamp` compared back against that column, so both are declared
ordered at `core.TS`'s width and `cursor.go` moves each one forward and only
forward — a guarded transition rather than an update
([`6/40`](../specs/6/40-store-surface.md) §2). `arizuko migrate-hot` refuses to
carry a cursor until the message rows it compares against are padded, because
padding a cursor first is what re-feeds consumed messages (BUGS `L97`, routd
migration `0044`).

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

`sessions` carries topic lineage as attributes: `parent_topic`,
`forked_at`, `observed_cursor`. `EnsureTopicLineage` seeds an item
idempotently on first turn, inheriting the parent's cursor rather than
stamping now; `ForkTopic` branches explicitly. Parent context arrives via a
`cp` of the parent's Claude Code session jsonl (`container.CopySession`) — no
inline injection. `ObservedSince` feeds the `<observed>` block, and
`AdvanceObservedCursor` moves each topic's cursor independently and forward
only. Spec: `specs/5/F-topic-lineage.md`.

## Dependencies

- `core`, `db_utils`, `groupfolder`, `modernc.org/sqlite`

## Files

- `store.go` — `Open`, `OpenMem`, `Migrate`, connection setup
- `messages.go`, `groups.go`, `tasks.go`, `auth.go`, `acl.go`, `membership.go`, `routes.go`, `onboarding.go`, `invites.go`, `secrets.go`, `inspect.go`
- `chats.go`, `sessions.go`, `chat_reply_state.go`, `chat_proactive.go`, `system_messages.go` — the hot tier, one file per entity
- `cursor.go` — the forward-only guarded transition the two cursors share
- `session_log.go` — the run history beside `sessions`, still a table of columns
- `import_routd.go`, `import_hot.go` — the target halves of the two moves
- `migrations/NNNN-*.sql` — numbered migrations

## Related docs

- `ARCHITECTURE.md` (SQLite Schema)
