# store

The access layer for the tables the split topology shares.

## Purpose

`store` is a library, not a daemon: the door onto one owner's tables plus the
typed accessors over them. `served.go` is the only place that dials the
instance's store server, and every other file here reads and writes through the
handle it answers.

## Every table is an entity

`entities.go` names every table this package holds as an objstore entity,
read and written through objstore's two primitives (spec
[`6/37`](../specs/6/37-store-model.md) step 7): `pane_sessions`,
`secret_use_log` and `counters`, routd's thirteen registered resources,
onbod's four, the hot tier — `chats`, `sessions`, `chat_reply_state`,
`chat_proactive`, `system_messages`, `user_profiles`, `turn_results`,
`group_watchers`, `task_run_logs`, `turn_context` — and `messages` with the
four read-shaped indexes one append fans out into. So the package issues no
statement of its own: the same code runs over a pool and over a served
endpoint. Ranked search is an in-memory BM25 index in routd over the
`messages` entity, derived and rebuilt from it (spec
[`6/42`](../specs/6/42-dynamo-over-postgres.md) §8, `routd/msgsearch`).

A `*Store` built over a pool holds it and reaches the entities through the
driver over it (`Store.Obj`); one handed a handle holds no pool at all, so
`Close` ends nothing and every daemon's `defer Close` stays correct either
way.

Each entity file argues its own coordinate in its header: what the partition
costs, which reads become the reader's work, and which attribute is ordered
and why.

The two cursors are the reason the hot tier moved together.
`chats.agent_cursor` and `sessions.observed_cursor` are copies of a
`messages.timestamp` compared back against that column, so both are declared
ordered at `core.TS`'s width and `cursor.go` moves each one forward and only
forward — a guarded transition rather than an update
([`6/40`](../specs/6/40-store-surface.md) §2). A cursor that moves ahead of the
rows it is compared against re-feeds consumed messages (BUGS `L97`).

## Public API

The doors are in `served.go`, and they are the only place the served/file
branch is written:

- `Handle(owner) (*dynstore.Store, error)` — dials one owner's tables; makes
  nothing
- `OpenOwner(owner) (*Store, error)` — the store one owner's tables live in, for
  a daemon that only READS them; probes for that owner's own entity so an owner
  that has never booted is loud instead of empty
- `OwnerDriver(owner) (Driver, func() error, error)` — the driver plus the close
- `PrepareOwner(ctx, d, owner, extra…)` — makes what one owner serves; the
  OWNER daemon calls it at boot, a reader never does
- `EnsureEntities(ctx, d, owner)` — the entity set that owner holds
- `TestNamespace()`, `TestOwner(owner, extra…)` — one test's own private table
  namespace, and the release that drops it
- `NewOver(obj objstore.Store) *Store` — wrap a handle

Primary methods (by domain):

- Messages: `PutMessage`, `NewMessagesForChat`, `MessagesSince`, `EnrichMessage`, `MarkMessagesErrored`, `DeleteErroredMessages`, `LatestSource`
- Groups: `PutGroup`, `DeleteGroup`, `AllGroups`, `GroupByFolder`, `SetGroupModel`
- Sessions: `GetSession`, `SetSession`, `EnsureTopicLineage`, `ForkTopic`, `TopicLineage` — the run history is runed's, over its own `/v1`
- Sticky: `StickyPins` (both at once), `SetStickyGroup`, `SetStickyTopic`, `GetStickyTopic`
- People: `CreateUserProfile`, `Username`, `SetUsername`, `ProfileNames`, `UserCap`, `SetUserCap` — one item per sub in `user_profiles`. Account linking lives in authd (`auth_users` + `oauth_identities`), not here.
- ACL (spec 5/32-acl-unified): `AddACLRow`, `RemoveACLRow`, `ListACL`, `ListACLByScope`, `ACLRowsFor`, `ACLWildcardRows` (scope listing is `auth.UserScopes` — a projection of these, not a second query)
- Membership: `AddMembership`, `RemoveMembership`, `Members`, `Ancestors` — roles + JID→sub claims (`acl_membership` table)
- Tasks: `CreateTask`, `GetTask`, `ListTasks`, `UpdateTask`, `DeleteTask`
- Task runs: `RecordTaskRun`, `TaskRunLogs`, `AllRunLogs`, `FailedRunsSince`
- Turns: `OpenTurnContext`, `TurnContextOf`, `RecordRetryAttempt`, `SweepExpiredRunning`, `RunningTurns`, `TurnGatewayKey`, `RecordTurnResult`, `GetTurnResult`
- Secrets: `SetSecret`, `GetSecret`, `ListSecrets`, `SecretKeysIn`,
  `DeleteSecret`, `FolderSecretsResolved` (walk parents → root),
  `CountPlaintextSecrets` + `EncryptPlaintextSecrets`
  (seal legacy rows in place; driven by `arizuko secret <inst> seal-legacy`,
  never on boot — the boot-time `PurgeUnencryptedSecrets` they replaced wiped
  every secret on each startup).
  AES-256-GCM encrypted at rest under the `v2:` prefix; key derived via SHA-256
  from the `SECRETS_KEY` env var, which is REQUIRED — every write path refuses
  without it rather than storing plaintext (BUGS S2). A legacy un-prefixed row
  still reads correctly until it is sealed.
- Routes, onboarding, topics — see source

`messages.is_observed` marks rows stored under a
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

## Coverage

`go test ./store/ -short -cover` reports 64.9% (2026-09-13), and the gap is
concentrated rather than spread: `import_routd.go` and `import_hot.go` are the
target halves of the one-way copy into the entity shape and have no caller in
the tree — tests here and in `cmd/arizuko` are what exercise them.

The rest of the shortfall is error returns on paths the engine decides: a
driver refusal every caller reports and no test provokes.

## Dependencies

- `core`, `objstore` (+ `dynstore`), `audit`, `groupfolder`

## Files

- `served.go` — the doors onto an owner: `Handle`, `OpenOwner`, `OwnerDriver`, `PrepareOwner`, `TestNamespace`
- `store.go` — the `*Store` itself, the secret keyring, `OpenMem`
- `messages.go`, `groups.go`, `tasks.go`, `acl.go`, `membership.go`, `routes.go`, `onboarding.go`, `invites.go`, `secrets.go`
- `chats.go`, `sessions.go`, `chat_reply_state.go`, `chat_proactive.go`, `system_messages.go`, `user_profiles.go`, `turn_results.go`, `group_watchers.go`, `task_run_logs.go`, `turn_context.go`, `invite_redemptions.go` — the hot tier, one file per entity
- `cursor.go` — the forward-only guarded transition the two cursors share
- `inspect.go`, `turn_frames.go` — the two `messages` reads an operator page and a browser stream ask for

## Related docs

- `ARCHITECTURE.md` (Schema)
