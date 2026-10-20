# store

The access layer for the tables the split topology shares.

## Purpose

`store` is a library, not a daemon: the door onto one owner's tables plus the
typed accessors over them. `served.go` is the only place that dials the
instance's store server, and every other file here reads and writes through the
handle it answers.

## Every table is an entity

`entities.go` names every table this package holds as an objstore entity,
read and written through objstore's two primitives, so the package issues no
statement of its own. Ranked search is not a table: it is an in-memory BM25
index in routd over `messages`, derived and rebuilt from it
(`routd/README.md` § Ranked message search).

Each entity file argues its own coordinate in its header: what the partition
costs, which reads become the reader's work, and which attribute is ordered
and why. The table is the summary; the file is the authority.

| Entity                  | Coordinate                                                       | Attributes                                                                                                                                                                                                                                                                                          |
| ----------------------- | ---------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `messages`              | the chat, then `core.TS` of the instant plus the message id      | sender, content, verb, source, attachments, topic, `routed_to`, errored, `is_observed`, turn id. Two messages can share an instant in one chat, so the id is in the key                                                                                                                             |
| `message_folders`       | the folder, then the message's own position                      | a pointer to the message, written when `routed_to` is stamped                                                                                                                                                                                                                                       |
| `message_folder_counts` | the folder                                                       | the folder's message count                                                                                                                                                                                                                                                                          |
| `message_activity`      | one constant partition, then the message's position              | the newest messages across every chat, swept at `ActivityRetention`                                                                                                                                                                                                                                 |
| `message_delivery`      | the delivery status, then the message's position                 | attempts. `sent` is the absence of an item                                                                                                                                                                                                                                                          |
| `chats`                 | the chat JID                                                     | `agent_cursor`, sticky group and topic, errored, `is_group`                                                                                                                                                                                                                                         |
| `sessions`              | folder and topic                                                 | `session_id`, `parent_topic`, `forked_at`, `observed_cursor`                                                                                                                                                                                                                                        |
| `chat_reply_state`      | JID and topic                                                    | `last_reply_id`, engagement window and folder, `disengaged_at`                                                                                                                                                                                                                                      |
| `chat_proactive`        | the chat JID                                                     | the instant the chat last spoke unprompted                                                                                                                                                                                                                                                          |
| `system_messages`       | the folder, then the queue instant                               | source, kind, body. The drain deletes exactly the items it read, so an event that arrives after the read is never deleted unrendered                                                                                                                                                                |
| `turn_context`          | the turn id                                                      | folder, topic, chat, trigger sender and message, return address, run id, state, `run_returned`, `retry_count`, `addressed`, `gateway_key_hash`                                                                                                                                                      |
| `turn_results`          | folder and turn id                                               | session, status, recorded instant. Create-if-absent makes one report one delivery                                                                                                                                                                                                                   |
| `groups`                | the folder                                                       | product, config, `container_config`, `cost_cap_cents_per_day`, added and updated instants                                                                                                                                                                                                           |
| `routes`                | `seq`, padded so text order is precedence, then target and match | `id` minted through `counters`, observe-window caps, `added_by` and `added_via`. `added_via` names the act, `picker` or `sole_world` for onbod, so a reader learns who wrote a rule without knowing which daemon did                                                                                |
| `route_tokens`          | the sha256 of the token                                          | JID, owner folder, context, `kind`: `route` for a delivery bearer, `pair` for an identity link                                                                                                                                                                                                      |
| `web_routes`            | the path prefix                                                  | `access`, redirect target, owning folder                                                                                                                                                                                                                                                            |
| `proxyd_routes`         | the path                                                         | backend, auth. proxyd owns every write                                                                                                                                                                                                                                                              |
| `acl`                   | principal, action, scope, params, predicate, effect              | `granted_by`, `granted_at`, `grant_option`                                                                                                                                                                                                                                                          |
| `acl_membership`        | child and parent                                                 | `added_by`, `added_at`. Roles and JID-to-sub claims share it                                                                                                                                                                                                                                        |
| `secrets`               | scope kind, scope id, key                                        | `value`, sealed AES-256-GCM, `created_at`. Surrogate OAuth adds `provider`, `refresh_val`, sealed like `value`, `expires_at` and `scope_list` to the same item, so an OAuth connect at `GITHUB_TOKEN` takes the item a pasted PAT at that key held. `provider` present is what marks the OAuth half |
| `secret_use_log`        | the instant a tool call resolved one                             | spawn, caller, folder, tool, key, outcome, latency                                                                                                                                                                                                                                                  |
| `network_rules`         | folder and target                                                | the per-folder egress allowlist                                                                                                                                                                                                                                                                     |
| `scheduled_tasks`       | the task id                                                      | owner, chat, prompt, cron, `next_run`, status, `context_mode`                                                                                                                                                                                                                                       |
| `task_run_logs`         | the instant a task ran                                           | task, duration, status, result, error                                                                                                                                                                                                                                                               |
| `pending_actions`       | the call id                                                      | one tool call held for a human verdict                                                                                                                                                                                                                                                              |
| `installed_packages`    | folder and name, `''` for instance-wide                          | source, revision, manifest, asset hashes                                                                                                                                                                                                                                                            |
| `github_sources`        | the source key                                                   | gitd's sealed webhook secret, owning folder                                                                                                                                                                                                                                                         |
| `gdrive_files`          | the Drive file id                                                | the folder that binds it; googd polls exactly the files it names                                                                                                                                                                                                                                    |
| `user_profiles`         | the person's sub                                                 | username, daily spend cap                                                                                                                                                                                                                                                                           |
| `group_watchers`        | observer and source                                              | nothing: the pair is the fact                                                                                                                                                                                                                                                                       |
| `pane_sessions`         | team, user, thread                                               | the Slack pane's channel and context JID                                                                                                                                                                                                                                                            |
| `counters`              | the name of what it numbers                                      | the count; one counter, `routes.id`, moved by compare-and-swap                                                                                                                                                                                                                                      |
| `onboarding`            | the chat JID                                                     | status, `user_sub`, gate, created, prompted, queued and admitted instants. No credential: the link is a `pair` row in `route_tokens`                                                                                                                                                                |
| `onboarding_gates`      | the gate                                                         | `limit_per_day`, enabled                                                                                                                                                                                                                                                                            |
| `invites`               | the invite ref                                                   | `target_glob`, issuer, issued and expiry instants, `max_uses`, `used_count`                                                                                                                                                                                                                         |
| `invite_redemptions`    | the person, then the instant                                     | one unspent redemption's `target_glob`; the point delete is the claim                                                                                                                                                                                                                               |

## Multi-table writes are security invariants

A write that spans two entities carries a security property, not tidiness.
Each site below writes its parts in one guarded batch, or orders them so a
partial run is safe, because a partial write escalates.

| Site                                  | What a partial write would cost                                                                                                          |
| ------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| group delete, `DeleteGroup`           | a re-created folder inherits the last tenant's admin row                                                                                 |
| invite redeem, `invites.go`           | without the conditional increment on `used_count`, redemption past `max_uses`                                                            |
| world creation, onbod `writeWorld`    | admin on someone else's world; the group, edge and grant go in one batch whose create-if-absent on `groups` refuses a concurrent creator |
| pairing, `RedeemPairing`              | two parents for one JID; the edge and the token delete go in one batch                                                                   |
| `/always`, `routd/pending_actions.go` | an approved call with no rule, or a standing skip-approval rule with no approved call                                                    |
| engagement, `SetEngagementAudited`    | a window with no audit row                                                                                                               |

**A batch stays inside one owner.** The driver names tables by owner, and one
`TransactWriteItems` reaches one owner's tables. So invite redemption cannot
put onbod's counter and routd's grant in one batch: `RestoreInvite` releases
the use when the grant write fails, through the counter protocol so a lost
race cannot become a lost decrement.

**A group delete is a purge.** `groups` goes first, which makes the folder
unreachable. A failure part-way leaves rows nothing can find, and the next run
collects them. `folder_scope.go` lists every scope that confers on a folder or
routes into it once, a completeness test walks the schema, and nothing relies
on a cascade.

**Membership is a graph, not a tree.** `acl_membership` edges form a DAG; a
parent need not be a path prefix of its child.
`objstore.AddEdge` guards every write against a cycle, and the read path walks
the same graph (`Ancestors`). A JID-to-sub alias and a role assignment share
the table.

## How the storage layer is organised

- One file per entity. An operation that carries an invariant stays whole in
  the file of the entity it guards, even when it writes a second one.
- A method is named for the question it answers, never for the verb it runs.
- A daemon with handler code of its own gets its own storage package:
  `authd/authstore`, `runed/runstore`, `emaid/emaistore`,
  `chanlib/chanstore`, `dashd/dashstore`. `audit` and `store` get none.

A `*Store` built over a pool holds it and reaches the entities through the
driver over it (`Store.Obj`); one handed a handle holds no pool at all, so
`Close` ends nothing and every daemon's `defer Close` stays correct either
way.

The two cursors are the reason the hot tier moved together.
`chats.agent_cursor` and `sessions.observed_cursor` are copies of a
`messages.timestamp` compared back against that column, so both are declared
ordered at `core.TS`'s width and `cursor.go` moves each one forward and only
forward, a guarded transition rather than an update. A cursor that moves
ahead of the rows it is compared against re-feeds consumed messages (BUGS
`L97`).

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
- ACL: `AddACLRow`, `RemoveACLRow`, `ListACL`, `ListACLByScope`, `ACLRowsFor`, `ACLWildcardRows` (scope listing is `auth.UserScopes` — a projection of these, not a second query)
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
`observe_window_chars` override the env defaults.

`sessions` carries topic lineage as attributes: `parent_topic`,
`forked_at`, `observed_cursor`. `EnsureTopicLineage` seeds an item
idempotently on first turn, inheriting the parent's cursor rather than
stamping now; `ForkTopic` branches explicitly. Parent context arrives via a
`cp` of the parent's Claude Code session jsonl (`container.CopySession`) — no
inline injection. `ObservedSince` feeds the `<observed>` block, and
`AdvanceObservedCursor` moves each topic's cursor independently and forward
only (`ROUTING.md` § Topic Routing).

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

- `ARCHITECTURE.md` § Data ownership
