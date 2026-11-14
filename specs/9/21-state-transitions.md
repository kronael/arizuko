---
status: partial
---

# Work follows state transitions

Work is a durable obligation that a state-changing write schedules and its consumer acknowledges.

## The problem

At `ba80338b4`, timers discover work by filtering ordinary rows. Their cost grows with unrelated data.
The owner approved replacing these reads with explicit transitions on 2026-11-14.
`objstore.Store` remains exactly `Query` and `GuardedBatch`.

## Where it fits

```text
writer ── guarded state + index write (changed) ──► owner store
                                                        │
clock ── due key range (changed) ──► consumer ◄───────────┘
                                      │
                                      └── acknowledge or reschedule (changed)
whole-partition work discovery (removed)
```

The hot-tier index contains pointers, not a second copy of resource data.
Each owner prepares and migrates its own index. Cold-tier resources retain their REST/MCP handlers.
Queries, claims and acknowledgements retain their existing ownership and authorization checks.

## Inventory

Computed costs below count rows selected by keys before attribute filters, per pass.
`N` means all rows in the named partition; `D` means due rows; `A` means active rows.
Startup and run-once migration walks are distinct from recurring work discovery.

| Component / entry                          | State sought and current access                                    | Frequency                      | Before → target                                              |
| ------------------------------------------ | ------------------------------------------------------------------ | ------------------------------ | ------------------------------------------------------------ |
| routd `maybeRecoverTurns`                  | running/retry turns; `RunningTurns` filters live partition         | 30 s                           | N → recovery obligations                                     |
| routd `turnControl.tick`                   | retries; `DeferredTurns` filters state and deadline                | 30 s                           | N → D                                                        |
| routd input ordering                       | whole chats plus live turns, then folder/cursor filters            | each dispatch                  | N chats + A turns → time candidates + folder/chat live turns |
| routd breaker                              | `ReadTurnControl` exact singleton; refused chats in memory         | 30 s                           | 1 → 1 at deadline                                            |
| routd `recoverStatuses`                    | `status_owed`; filters live turns                                  | 30 s                           | N → owed pointers                                            |
| routd `maybeRetryOutbound`                 | pending delivery by creation time; backoff checked after hydration | 30 s, limit 200                | up to 200 non-due payloads → D                               |
| routd `maybeGC`                            | running turns filtered by start time; activity already time-keyed  | hourly                         | N + expired activity → expired rows                          |
| routd `flushProgress`                      | memory pending updates; exact turn/status reads                    | loop tick, publish ≥5 s        | pending updates → due updates                                |
| routd `ScheduleLoop`                       | all active tasks, parses deadlines; fills missing schedules        | startup + 60 s                 | N → D                                                        |
| routd approval wait                        | exact pending-action id                                            | 2 s                            | 1 → verdict notification / deadline                          |
| onbod pending check                        | whole onboarding partition; alias reads per awaiting sender        | 10 s                           | N + awaiting senders → state work                            |
| onbod gate admission                       | gates plus onboarding filtered twice per gate                      | about 60 s                     | gates × N → due gate queue                                   |
| chanlib retention: gitd, slakd, discd      | all retained inbound; deadline filter, oldest-first limit 50       | 15 s                           | N + payload bytes → D pointers + ≤50 payloads                |
| chanlib adapter health                     | whole retained inbound for `pending_inbound`                       | chanreg 30 s + requests        | N → 1 count                                                  |
| runed spawn retention                      | whole spawns filtered on creation time                             | hourly                         | N → expired rows                                             |
| runed VM sweep                             | host runtime records; no store poll                                | startup                        | filesystem only                                              |
| googd file polling                         | whole bound-file resource, local deadline check                    | default 30 s, minimum 10 s     | N → due files                                                |
| proxyd snapshots                           | whole route tables on requests; no ticker                          | each request                   | N → version point read, changed snapshot only                |
| dashd                                      | request-driven dashboard/HTMX reads                                | browser refresh                | filtered partitions → requested key ranges                   |
| webd SSE                                   | ping only; messages arrive through hub publication                 | 15 s                           | 0 → 0 store reads                                            |
| CLI `send` / `pollRound`                   | bot replies by chat/time range or exact turn snapshot              | 1 s                            | bounded keys → notification where available                  |
| whapd retained inbound                     | reads and sorts every retained JSON file                           | 30 s                           | N files → scheduled retained files                           |
| adapter marks                              | exact mark lookup on expiry; startup recovery only                 | per-mark deadline              | 1 → 1                                                        |
| adapter registration / health, API retries | platform or HTTP calls; health counting listed above               | configured heartbeat / backoff | no other store discovery                                     |
| emaid                                      | IMAP polling fallback / IDLE renewal                               | 30 s / 28 min                  | 0 store reads on empty polls                                 |
| teled / discd / slakd                      | platform reconnect, readiness, typing refresh                      | platform deadlines             | 0 store discovery reads                                      |

For a backlog of 17,816 held messages, the 15-second retention sweep plus
30-second registry health probe selects 6,413,760 rows per hour per adapter.
This is a computed example using L822's recorded backlog, not a live measurement.
The Go source inventory searches `time.NewTicker`, `NewTimer`, `AfterFunc`,
`Tick`, `After` and `Sleep` outside vendor/test code. The WhatsApp inventory
also includes `setInterval` and `setTimeout`.

Sources: `routd/loop.go`, `routd/turn_control.go`, `routd/turn_status.go`,
`routd/schedule.go`, `store/tasks.go`, `store/message_delivery.go`,
`onbod/main.go`, `chanlib/chanstore/inbound_pending.go`, `runed/runstore/spawns.go`,
`googd/main.go`, `proxyd/main.go`, `webd/hub.go`, `webd/turn.go`.

## Module map

```yaml
- path: objstore/indexed.go (new)
  job: commit a primary mutation and its derived index pointers atomically
  names: [Indexed, Indexer, IndexItems, QueryIndex, DueIndexKey, Reindex]
  never: adds a driver method or splits one guarded batch
- path: objstore/replace.go (changed)
  job: bound replace batches including derived index writes
  names: [Replacer.Write]
  never: separates a row from its work pointers
- path: objstore/consistent.go (changed)
  job: reuse immutable snapshots only under a verified settled owner version
  names: [Snapshot.Read, ReadConsistent]
  never: serves cached data on a missing, held or unreadable marker
- path: proxyd/resource.go (changed)
  job: keep route and proxy snapshots consistent with the current owner version
  names: [snapshot, webSnapshot, buildSnapshot]
  never: caches authorization decisions
- path: googd/state.go (new)
  job: reconcile verified bindings and preserve persistent polling deadlines
  names: [boundFiles, reconcileBindings, pollState]
  never: forgets cursors when a binding is removed
- path: googd/googstore/state.go (new)
  job: index active file deadlines and migrate channel work once
  names: [Open, Due]
  never: adds an objstore method
- path: store/state_index.go (new)
  job: declare access keys for state transitions and migrate existing rows once
  names:
    [MigrateStateIndexes, RecoverableTurns, LiveTurnsInFolder, LiveTurnsInChat]
  never: exposes an operator-managed resource
- path: store/messages.go (changed)
  job: resolve chat identities and turn work by key
  names:
    [MessageByID, ActiveProgress, LastStatusRow, TurnFrames, BotRepliesSince]
  never: reinterprets a clamped timestamp as a stored coordinate
- path: cmd/arizuko/group_rename.go (changed)
  job: commit primary and derived pointer changes from guarded journal images
  names: [renameApplyBatch, renameSteps]
  never: journals derived pointers as source data
- path: store/store.go (changed)
  job: give every writer the same index-maintaining storage handle
  names: [NewOver, Obj]
  never: derives runtime identity from a path
- path: store/tasks.go (changed)
  job: claim due schedules and acknowledge their next transition
  names: [DueTasks, RecoverFiringTasks, RescheduleTask]
  never: discovers due tasks by an attribute filter
- path: store/entities.go (changed)
  job: prepare the owner's internal index and bound bulk writes
  names: [EntitiesByOwner]
  never: registers internal work as a management resource
- path: store/acl.go (changed)
  job: read current wildcard-principal policy only after its index is complete
  names: [ACLWildcardRows, aclRows]
  never: authorizes with a partial legacy backfill
- path: store/routd_work.go (new)
  job: schedule and acknowledge coalesced router obligations
  names: [DueWork, AcknowledgeWork, RescheduleWork]
  never: acknowledges another generation
- path: routd/approval_work.go (new)
  job: notify registered waiters from durable verdict work
  names: [waitForApproval, publishApprovalWork]
  never: polls unchanged pending-action rows
- path: store/onboarding_state.go (new)
  job: schedule greetings, pairing acknowledgments, notices and gate admissions
  names:
    [AdmitGate, PairingWork, ReadyOnboardingPrompts, ReadyOnboardingNotices]
  never: consumes another generation's work
- path: onbod/main.go (changed)
  job: consume due work and enqueue each generation durably
  names: [pollOnboarding, consumePairings, queueNotices, admitFromQueue]
  never: treats unreadable gates as an ungated instance
- path: store/messages_archive.go (changed)
  job: preserve delivery deadlines and operations through archive restoration
  names: [ExportMessagesJSONL, ImportMessagesJSONL]
  never: restores an expiring credential without its expiry
- path: routd/db.go (changed)
  job: migrate task pointers before serving requests
  names: [Open, Obj]
  never: runs a migration on a periodic timer
- path: resreg/entity.go (changed)
  job: preserve indexes during manifest replacement
  names: [On]
  never: declares its own task index format
- path: cmd/arizuko/group_rename_rows.go (changed)
  job: recognize derived pointers during rename preflight
  names: [renameMap]
  never: treats internal migration markers as resource rows
- path: store/state_index_test.go (new)
  job: prove due-only reads, atomic transitions, and migration restart behavior
  names: [TestDueTasksReadOnlyDueRows, TestStateIndexKeepsConcurrentTransition]
  never: uses a live instance
- path: store/turn_context.go (changed)
  job: read recovery and expiry work by key
  names: [RunningTurns, SweepExpiredRunning, TurnsSince]
  never: expires a concurrently restarted turn
- path: store/turn_control.go (changed)
  job: select due retries and unacknowledged status intents
  names: [DeferredTurns, OwedTurnStatuses, InterruptTurn]
  never: filters the live partition to discover work
- path: store/turn_archive.go (changed)
  job: keep archived history keyed by start time
  names: [archivedTurnsSince, MigrateTurnArchive]
  never: drops an existing migration
- path: routd/turn_control.go (changed)
  job: reconcile returned runs during runtime and live runs at startup
  names: [recover]
  never: treats an occupied folder as abandoned
- path: chanlib/chanstore/inbound_index.go (new)
  job: declare retry keys and migrate retained messages and the backlog count
  names: [MigrateInbound]
  never: changes a primary message ID
- path: chanlib/chanstore/inbound_pending.go (changed)
  job: schedule retained retries and maintain their count atomically
  names: [Retain, Reschedule, Drop, DueForRedelivery, HeldCount]
  never: loads payloads outside the selected due batch
- path: chanlib/dedup.go (changed)
  job: finish adapter migrations before serving requests
  names: [OpenAdapterStore]
  never: touches another adapter's namespace
- path: runed/runstore/state_index.go (new)
  job: declare spawn expiry and live pointers with a marked backfill
  names: [MigrateStateIndexes]
  never: changes the execution port
- path: runed/runstore/spawns.go (changed)
  job: select expiry and liveness from their work populations
  names: [DropSpawnsBefore, ExpireOrphans, countLive, LiveSpawnInFolder]
  never: reads finished history to discover active runs
- path: runed/db.go (changed)
  job: migrate runed work before serving
  names: [Open]
  never: migrates routd data
- path: store/message_delivery.go (changed)
  job: commit retry deadlines, attempts and acknowledgments atomically
  names:
    [PendingOutbound, RecordDeliveryFailure, DeliveryBackoff, MarkDelivered]
  never: finds retry eligibility after loading unrelated payloads
- path: routd/loop.go (changed)
  job: deliver only the due work selected by the store
  names: [maybeRetryOutbound, retryOutbound]
  never: computes a second retry schedule
```

## Atomic work keys

```text
primary state (changed) ── same guarded batch ──► work pointer (new)
        ▲                                            │
        └──────────── exact key ◄── due range ─────────┘
```

The index protocol reads only the primary keys named by a write. It guards the
observed revision and writes the state and pointer changes in the same batch.
A concurrent writer causes a reread, never a stale pointer. A failed caller
condition changes neither data nor work. Updates preserve untouched attribute
bytes, including legacy timestamp widths; pointer derivation uses the full
next state without rewriting that full state. Replace/import uses the same handle.
Batch packing includes derived writes; no atomic transaction is split.

Current `store/tasks.go`, `DueTasks`:

```go
items, err := s.Obj().Query(ctx, objstore.Query{
    Entity: ScheduledTasks, Partition: OwnerRoutd,
    Where: []objstore.Cmp{{Attr: attrTaskStatus, Op: objstore.Eq, Value: core.TaskActive}},
})
```

Proposed:

```go
items, err := objstore.IndexItems(ctx, s.Obj(), "tasks/due",
    objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 0)
```

Active schedules enter a due-time index. Claim moves them to `firing` and
removes that due key. Reschedule writes the next due key or a terminal state.
Pause and delete remove work. A missing initial cron time is a scheduled
initialization obligation; its consumer computes the first real deadline.
The startup recovery reader selects only `firing` keys.

## Turn transitions

Admission writes `running` and its startup/live and expiry pointers. A runtime
return writes the recovery obligation; a live guest is not runtime recovery
work. Startup reads only live pointers. The runtime consumer reconciles only
returned runs and the explicit breaker probe. Successful reconciliation
finishes the turn or schedules `pending_retry` at `next_attempt_at`.

A retry claim removes its due key. A status intent creates a status pointer;
publishing and acknowledging that exact attempt removes it. Finishing moves
the primary into `turn_archive` with its history pointer atomically. Both live
and archived history are keyed by start time. Expiry selects only older running
turns without an outstanding provider failure and guards the deadline again
at commit, so a concurrent restart cannot expire a fresh run.

`turns-v1` backfills both primary partitions once. The task migration remains.
Counting tests select one of 40 turns using two reads (pointer and primary),
and zero after acknowledgment. A concurrent restart mutation expires a fresh
run without the deadline guard and fails the test.

## Breaker obligations

Control writes schedule one immediate `routd_work/control/singleton` generation.
The consumer processes returned runs, then parks open state at `next_probe_at`.
Only the consumer can postpone work; concurrent producers always make it due.
Turn lifecycle, unread-span and route/group changes also schedule reconciliation.
Runtime recovery reads returned turns only when this work is due. Startup
still reconciles live turns. A busy probe schedules another attempt in 30 s;
a failed transition leaves its work pending. The work survives archival until
the breaker leaves probe state. Probe interruption compares the observed control
state and atomically guards the hydrated turn lifecycle, so a concurrent
successful completion cannot be reopened. Acknowledgment and rescheduling bind the
consumed generation, so they cannot erase a concurrent transition.

Dispatch registers an in-flight candidate before reading input or admission.
A control wake enqueues both refused and in-flight candidates; an active
worker retains the wake as another pass. Each new attempt clears its previous
refusal. This closes the read-to-refusal registration race.

The control wake enters the queue directly; it does not reenter synchronous
command processing while a candidate holds the chat lock.

`control-work-v1` backfills the singleton once. Route and group writes need no
backfill: startup rebuilds the chat queue and their next mutation schedules work.

`TestControlWorkReadsOnlyDueState` measures zero breaker reads between
transitions and deadlines. The control tests preserve concurrent generations,
hydrated deadlines, completed probes, archived retries and busy recovery.
`TestControlWorkRearmsUnownedProbe` prevents a probe without an owner from
remaining closed to admission. `TestInterruptTurnKeepsConcurrentCompletion`
requires the atomic lifecycle guard. Thirteen guard mutations fail their tests.

## Retained inbound transitions

`Retain` enters held state once per message ID and writes both the due pointer
and backlog increment in the same guarded batch. A repeated platform event
changes neither deadline nor count. The clock selects due pointers, sorts
only that set by `first_seen_at` and ID, and loads at most 50 payloads.
Transient failure moves the due pointer. Acceptance or permanent refusal
removes the primary and pointer and decrements the count exactly once.

The startup `inbound-due-v1` marker gates pointer backfill; the counter's
create-if-absent write gates its initial count. A restarted migration reads
only those two keys. The HTTP `pending_inbound` field retains its meaning.
One of 40 due messages reads two rows; health reads one. Sixty due messages
read 60 pointers and 50 payloads, preserving the existing oldest-first order.

## Run transitions

A queued spawn writes expiry and live pointers. Start keeps it live. Exit,
error and kill remove live pointers; immutable creation time determines its
expiry key. Startup reads only live pointers for orphan teardown. Admission
counts live pointers and dereferences only live runs for a folder check.
The hourly retention timer reads and deletes only expired keys and guards
creation time at deletion. `spawns-v1` backfills existing data once.

## Outbound transitions

Queuing a delivery writes its next attempt deadline and due pointer. Each
transient failure atomically increments the attempt count and schedules a full
backoff from that failure, capped at one hour. Exhaustion or permanent refusal
moves pending to failed under a must-exist guard. A confirmed delivery removes
pending and failed work together; status acknowledgments still fence revisions.
Replay moves a failed row back to pending, due immediately, with a fresh budget.

The consumer reads at most 200 due pointers and exact delivery/message keys.
Two hundred delayed messages cannot hide one due delivery: that fixture reads
three rows. `outbound-v1` backfills existing pending work once. Legacy rows
have no recorded last-attempt instant, so their initial key preserves the
creation/queued-time calculation; subsequent failures write explicit deadlines.
`PendingMessage.NextAttemptAt` is internal data, not a REST/MCP field.

## Message and progress transitions

Appending a message writes chat-scoped identity, platform/status aliases,
turn/status or turn/reply pointers, and bot/source time pointers in the same
batch. A delivery resolves the stored identity before changing its state, so
append-order timestamp adjustment cannot strand it. Redelivery retains that
coordinate. Progress flush reads only updates whose publication deadline is due.

`messages-v1` walks the chat partition list once before workers start. Group
rename commits primary and derived pointer changes together from its guarded
journal images. Derived pointers never enter the rename journal as data. Index declarations
return complete keys: message indexes retain the primary's exact sort key;
due-time indexes append a SHA-256 source identity to the deadline. Composite
lookup partitions hash their tuple. This preserves full-width primary keys and
prefix-ID cursor ordering within DynamoDB's key limits.

## Onboarding transitions

An unprompted row owns a durable greeting generation and retry deadline.
Claiming a generation retains its work. Onbod queues the pairing token hash
and outbound message in one routd transaction through the common message
renderer. The generation fixes the message ID and timestamp across retries.
Onbod acknowledges only that generation. The raw credential remains only in
the existing outbound message body; delivery stops at its pairing expiry.
Reprompt and expiry rearm clear the whole generation. Queue/refusal verdicts
also persist a notice generation. The daemon queues still-applicable notices
after admission, then acknowledges their exact generation; immediate admission
cancels an unsent queue notice. Legacy prompted rows do
not acquire another greeting merely because the indexes are backfilled.

A committed pairing membership owns an acknowledgment obligation. Onbod reads
only that sender's awaiting rows, applies guarded admissions, then acknowledges
the membership revision. A new greeting generation checks existing pairing
once, covering pairing before onboarding and explicit reprompt.

Queue and approved-admission pointers are partitioned by gate. An onboarding
or gate mutation atomically wakes each affected gate, including deletion and
raw manifest replacement. This before/after companion renderer uses the same
`Query` and `GuardedBatch` port. Internal gate work carries a generation and due
time; its due pointer changes in the same transaction. The consumer reads only
due gates, exact configuration, today's quota and the oldest eligible queue.
It removes empty/disabled work and schedules exhausted gates at UTC midnight.
Admission commits against the observed owner version and queued-row state;
acknowledgment also fences concurrent quota or configuration changes.

`onboarding-v1` and `pairings-v1` backfill each owner's existing rows before its
workers start. Gate work and derived pointers are hot implementation state;
the existing onboarding and gate REST/MCP resources remain the management API.
The onboarding archive carries optional `work` attributes for prompt and notice
generations. Message archives carry optional `delivery` attributes for retry
deadlines, operations and credential expiry. Readers accept archives without
these fields. Restores preserve pending work without copying index revisions.

Measured by `TestOnboardingReadsOnlyDueWork`: forty unrelated rows plus one
greeting read two items; the acknowledged pass reads zero. An idle daemon tick
reads no onboarding rows. Quota reads exclude historical days. Tests cover
pairing before onboarding, reprompt, FIFO admission, midnight, gate replacement,
quota release, owner-version races and archive restoration. Eleven disabled
guards fail their tests; stale-notice, archive-loss and delivery-metadata races
also fail before their fixes.

## Versioned snapshots

Config writers already bump their owner's scope marker in the same transaction.
`objstore.Snapshot` retains an immutable value and its settled version. Each
reader checks that marker by key. An unchanged version reuses the value; a new
version rebuilds under `ReadConsistent`. Missing, held or unreadable markers
return an error. There is no time-based expiry or stale-on-error fallback.
Proxyd's route data and proxy map are one snapshot; web routes use the same
protocol. Authorization continues to read current grants independently.
Snapshot errors propagate to HTTP 503, including the public-file path; an
unreadable access-rule table never means an empty rule set. With forty routes,
`TestRouteSnapshotsReadOnlyChangedState` measures one marker row and no table
read on reuse. Six mutations fail: reuse, version comparison, held marker,
consistent reload, missing version and HTTP error propagation.

## WhatsApp retained files

The ledger recovers its existing JSON files once at startup. A successful
retain write adds the payload to the pending set. An accepted delivery removes
the file and its pending entry together; failed unlink keeps the entry.
Acknowledgments identify the payload generation, so a newer retain survives an
older send. One drain sends the oldest pending payloads and stops at the first
delivery failure. A remaining obligation schedules a retry after 30 seconds;
an empty set owns no timer. Startup recovery is immediate. No file-format
migration is needed.
Each retain orders the pending set; each send selects its current oldest row
after the previous await. A shared serializer also covers failed-retention
delivery. One callback updates delivery health for every successful send.
The counting filesystem test measures zero directory or payload reads on
repeated drains. Nine mutations fail, including discovery, overlap, generation,
clone, unlink, empty timer, startup recovery, transport serialization and order.

## Google file polling

Google comments require time-triggered API reads. The existing routd scope
marker identifies binding changes. A verified binding snapshot is reconciled
into googd's per-channel `file_poll` partition on startup and committed changes.
New or reactivated bindings enter active work immediately. Removed bindings
become inactive and retain their cursor. Each active row owns a due pointer.
The worker reads only due rows and verifies their binding by file ID before
calling Google. Cursor, status, backoff and `next_poll_at` commit together under
the consumed revision. Successful ordinary polls schedule the configured
interval; rate limits and server errors retain the 30-second to 15-minute
backoff. Failed persistence leaves the old obligation pending.

`file-poll-v1` backfills the channel's existing work keys once. The row and index
are hot adapter state; `gdrive_files` remains the existing managed resource.
The global owner marker is deliberately reused; unrelated config changes may
refresh the snapshot without introducing another invalidation format.
The counting test measures at most eight reads for one due file among forty
future files; an idle pass reads one marker. Seven guard mutations fail,
including due range, hydration state, binding incarnation, revision, backoff
and elapsed time across earlier files. Restart retains cursors and deadlines.

## Dashboard refreshes

Task refreshes reuse the immutable REST result under the existing settled
routd version. Authorization runs on each render, before the 500-row window.
Activity keeps its bounded time-key feed and resolves ordinary chats against a
verified route snapshot. Token JIDs read only their indexed credentials; token
writers create or remove those pointers atomically. `route-tokens-v1` backfills
existing credentials once. Unreadable or held snapshots never expose rows.
No HTTP fields or authorization rules change. Task-list failures propagate as
errors; they cannot be cached as successful empty results. Two unchanged
refreshes read two markers and no tables. One token JID among forty unrelated
credentials reads two rows, then zero after revocation. Three access-path
mutations fail; a task-read fault test fails before error propagation.

## Approval verdicts

Held calls register a waiter before reading their authoritative row. A verdict,
expiry or removal writes one coalesced `routd_work` obligation in the same
transaction. The DB's consumer reads at most 200 due approval keys, sends wake hints and
acknowledges only the observed generation. An unchanged held row owns no work.
Waiters reread under the settled owner version after notification; store errors
schedule a read retry. A held manifest replacement cannot authorize a call or
turn its temporary deletion into a final verdict. Their
deadline still uses conditional expiry and a final read when another verdict
wins. The consumer starts on registration, parks without a timer when no waiters
remain, and cancels its reads before joining at DB close.
`approvals-v1` backfills existing final verdicts once. Work is hot internal
state; pending actions retain their existing management resource.

The counting test measures zero rows for forty held calls, two rows for one
verdict and zero after acknowledgment. Nine mutations fail: scheduling,
generation, notification, registration order, retry, removal, cancellation and
batch bound and settled version.

## Pending input ordering

Chat writes maintain a pointer while `input_last` exceeds `agent_cursor`.
Its due timestamp is the timestamp of `max(input_first, agent_cursor)`; the
consumer verifies the complete cursor after hydration. Equal-timestamp
candidates share the due bucket so a long message ID cannot overflow a key.
Cursor advancement removes consumed spans or moves their lower bound. Live
turns carry folder and chat partitions. Deferred input is selected by folder;
admitted input is checked by chat even when its route changes folders.
`chat-input-v1` and `turn-access-v2` backfill these keys once. The existing
input-span and turn migrations remain intact.

Appends bind their span write to the chat revision used to compute it. A
conflict retries the whole append, including timestamp selection. The message,
span, derived keys and folder count commit together. A refused append is a
duplicate only when its message ID already exists; other refusals remain errors.

`TestInputIndexReadsOnlyEligibleChats` reads two rows with forty irrelevant chats.
Consumption removes the key; long cursor ties and concurrent hydration remain
correct. `TestInputSpanKeepsConcurrentAppend` fails when the observed-revision
guard is removed. `TestInputOrderKeepsAdmissionAcrossRouteChange` requires the
chat-specific admission index.

## Wildcard policy discovery

Authorization verifies the `acl-wildcard-v1` completion marker before reading
the `acl/wildcard` partition. Missing readiness fails closed during upgrades;
other daemons do not backfill the owner's store. Exact-principal reads stay keyed.
Only an asterisk in the principal coordinate enters that partition; wildcards
in actions, scopes or predicates do not qualify. Primaries remain the policy
authority. The common ACL decoder rejects unreadable rows, and owner-version
checks retain a consistent policy. Every authorization reads current pointers;
no policy decision is cached. Revocation deletes the pointer atomically.

`acl-wildcard-v1` validates existing coordinates and backfills once. The marker
is written only after the index is complete. Manifest replacement and rename
use the common indexed writer for forward and reverse transitions.

`TestACLWildcardIndexReadsOnlyWildcardRows` reads three rows with forty
unrelated grants: the readiness marker, one pointer and its primary. Revocation
leaves one marker read. Policy, manifest replacement and forward/reverse rename
tests preserve wildcard deny behavior. Nine mutations cover selection,
principal parsing, pointer production, migration validation and its marker,
read errors, rename maintenance, upgrade readiness and its error cause.

Onbod queues deliveries through its existing routd store handle. It has no
outbound HTTP client or service-token exchange. `AUTHD_URL` remains required
for inbound token verification.

Token `send --wait` and `--stream` share the existing replaying turn stream.
The SSE client has no total body deadline; the POST keeps its thirty-second
timeout. The operator-direct path still reads the indexed reply range because
its current contract ends on quiet time rather than a turn completion event.

## Open questions

- Which stream/auth/completion contract should replace operator-direct send's
  keyed reply poll? The `/me` stream needs a public endpoint, live user grants
  and a topic; it has no replay or equivalent quiet-time completion contract.
  Keep the existing local operator contract until that decision.

- May provisioning drop onbod's unused `AUTHD_SERVICE_KEY` and `ROUTER_URL`?
  The daemon accepts existing environments and does not need those values.

- The archive adds optional `work` and `delivery` metadata. Does any external
  archive consumer require a version signal for these additive fields?
- Onbod uses its existing routd store handle to enqueue greetings atomically
  and acknowledge pairing revisions. If this handle becomes remote, which
  durable command surface should carry these two cross-owner transitions?
- Pairing credentials expire ten minutes after mint. Expired queued greetings
  stop delivery; should a new generation start automatically or require the
  existing inbound/reprompt trigger?
- Run retention currently deletes every state after seven days. Should active
  spawns be exempt? This change preserves the existing all-state contract.

1. Proxyd route/ACL freshness is a request contract. Keep immediate revocation
   and use existing version markers; do not introduce a time-based cache.
2. Retention orders due rows by `first_seen_at`. Keep that order by sorting
   due pointers before selecting 50. The read cost is O(D), not O(50).
3. Cross-daemon state notifications must remain durable across a failed HTTP
   call. Any added notification contract must name its retry and acknowledgement.

## Contract

Baseline: detached `ba80338b4`; no live-instance actions are authorized.
Counting stores measure key-selected rows, including rows hidden by filters.
Every changed consumer must read O(D) work rows and exact primary keys.
Tests cover entry, reschedule, acknowledgement, deletion, competing writes,
restart migration and a second migration run that reads only its marker.
Each guard is disabled in a mutation run and must make its test fail.
Covering tests run one package at a time. Final gates run `./routd` and
`./store` once each with `-short -timeout 45m`, then `make lint` once.
