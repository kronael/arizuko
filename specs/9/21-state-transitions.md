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
  names: [MigrateStateIndexes]
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
condition changes neither data nor work. Replace/import uses the same handle.
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

## Open questions

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
