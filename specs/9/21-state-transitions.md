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

| Component / entry                          | State sought and current access                                    | Frequency                      | Before → target                               |
| ------------------------------------------ | ------------------------------------------------------------------ | ------------------------------ | --------------------------------------------- |
| routd `maybeRecoverTurns`                  | running/retry turns; `RunningTurns` filters live partition         | 30 s                           | N → recovery obligations                      |
| routd `turnControl.tick`                   | retries; `DeferredTurns` filters state and deadline                | 30 s                           | N → D                                         |
| routd breaker                              | `ReadTurnControl` exact singleton; refused chats in memory         | 30 s                           | 1 → 1 at deadline                             |
| routd `recoverStatuses`                    | `status_owed`; filters live turns                                  | 30 s                           | N → owed pointers                             |
| routd `maybeRetryOutbound`                 | pending delivery by creation time; backoff checked after hydration | 30 s, limit 200                | up to 200 non-due payloads → D                |
| routd `maybeGC`                            | running turns filtered by start time; activity already time-keyed  | hourly                         | N + expired activity → expired rows           |
| routd `flushProgress`                      | memory pending updates; exact turn/status reads                    | loop tick, publish ≥5 s        | pending updates → due updates                 |
| routd `ScheduleLoop`                       | all active tasks, parses deadlines; fills missing schedules        | startup + 60 s                 | N → D                                         |
| routd approval wait                        | exact pending-action id                                            | 2 s                            | 1 → verdict notification / deadline           |
| onbod pending check                        | whole onboarding partition; alias reads per awaiting sender        | 10 s                           | N + awaiting senders → state work             |
| onbod gate admission                       | gates plus onboarding filtered twice per gate                      | about 60 s                     | gates × N → due gate queue                    |
| chanlib retention: gitd, slakd, discd      | all retained inbound; deadline filter, oldest-first limit 50       | 15 s                           | N + payload bytes → D pointers + ≤50 payloads |
| chanlib adapter health                     | whole retained inbound for `pending_inbound`                       | chanreg 30 s + requests        | N → 1 count                                   |
| runed spawn retention                      | whole spawns filtered on creation time                             | hourly                         | N → expired rows                              |
| runed VM sweep                             | host runtime records; no store poll                                | startup                        | filesystem only                               |
| googd file polling                         | whole bound-file resource, local deadline check                    | default 30 s, minimum 10 s     | N → due files                                 |
| proxyd snapshots                           | whole route tables on requests; no ticker                          | each request                   | N → version point read, changed snapshot only |
| dashd                                      | request-driven dashboard/HTMX reads                                | browser refresh                | filtered partitions → requested key ranges    |
| webd SSE                                   | ping only; messages arrive through hub publication                 | 15 s                           | 0 → 0 store reads                             |
| CLI `send` / `pollRound`                   | bot replies by chat/time range or exact turn snapshot              | 1 s                            | bounded keys → notification where available   |
| whapd retained inbound                     | reads and sorts every retained JSON file                           | 30 s                           | N files → scheduled retained files            |
| adapter marks                              | exact mark lookup on expiry; startup recovery only                 | per-mark deadline              | 1 → 1                                         |
| adapter registration / health, API retries | platform or HTTP calls; health counting listed above               | configured heartbeat / backoff | no other store discovery                      |
| emaid                                      | IMAP polling fallback / IDLE renewal                               | 30 s / 28 min                  | 0 store reads on empty polls                  |
| teled / discd / slakd                      | platform reconnect, readiness, typing refresh                      | platform deadlines             | 0 store discovery reads                       |

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
  names: [Indexed, Indexer, IndexItems, Reindex]
  never: adds a driver method or splits one guarded batch
- path: objstore/replace.go (changed)
  job: bound replace batches including derived index writes
  names: [Replacer.Write]
  never: separates a row from its work pointers
- path: store/state_index.go (new)
  job: declare access keys for state transitions and migrate existing rows once
  names: [MigrateStateIndexes, RecoverableTurns]
  never: exposes an operator-managed resource
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
- path: routd/db.go (changed)
  job: migrate task pointers before serving requests
  names: [Open, Obj]
  never: runs a migration on a periodic timer
- path: resreg/entity.go (changed)
  job: preserve indexes during manifest replacement
  names: [On]
  never: declares its own task index format
- path: cmd/arizuko/group_rename_rows.go (changed)
  job: recognize folder-independent task pointers during rename preflight
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
  names: [DeferredTurns, OwedTurnStatuses]
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

## Open questions

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
