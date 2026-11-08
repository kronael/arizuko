---
status: shipped
---

# A provider refusal pauses the instance

One pause holds all turns until a held turn runs without a provider refusal.

Owner measurements, 2026-11-08: rhias/trip exits 78 within eight seconds after
a spend limit; an eval receives 107 HTTP 429s in ten minutes on a shared token.
References use `f14b3a7a3f646332a578fe1e5901a0242b8c96dc`:
`ant/src/index.ts:494` emits exit 78; `runed/runtime.go:21` recognizes it.
`routd/dispatch.go:556` retries each turn; later messages start fresh attempts.
`routd/loop.go:360` backs off outbound delivery, not model turns.

## One guarded turn claim controls the pause

```text
messages ──► turn claim (changed) ──normal spawn──► runed ──► ant (changed)
                   │ guard (new)                              │
                   ▼                                          │
          instance pause (new) ◄──ProviderFailure (new)────────┘
                   │ due: oldest held turn only (new)
                   └──────────────────────────────► turn claim
operator REST / MCP ◄──► pause handler (new) ◄── dashboard / CLI (changed)
chat/folder breakers and notice suppression (removed)
```

```yaml
- path: ant/src/backend.ts, ant/src/claude.ts, ant/src/codex.ts (changed)
  job: classify provider-origin failures
  names: [Frame, ProviderFailure]
  never: classify assistant or tool text
- path: ant/src/index.ts, ant/src/mcp.ts, routd/api/v1/types.go (changed)
  job: carry ProviderFailure through submit_turn
  names: [TurnResult]
  never: infer a pause from exit 78
- path: store/model_pauses.go (new), store/turn_context.go (changed)
  job: persist one episode and guard turn claims
  names: [OpenTurnContext]
  never: create another admission lock or credential registry
- path: routd/dispatch.go, routd/turns.go, routd/loop.go (changed)
  job: hold input and schedule the normal probe turn
  names: [Loop]
  never: finalize held input or call a separate probe endpoint
- path: >-
    routd/model_pauses.go, resreg/resources/model_pauses.go,
    dashd/model_pauses.go (new)
  job: share list and clear through one resource
  names: [model_pauses]
  never: bypass operator authorization
- path: queue/queue.go, runed/manager.go, runed/api/v1/types.go (changed)
  job: remove breakers
  names: [GroupQueue, Manager, RunOutcome]
  never: own another pause policy
```

### Classification stays strict

Adapters emit `ProviderFailure{reason, reset_at?}` through existing
`submit_turn`: spend limit, usage/rate limit, refused authentication or
provider outage. Sources are an attempt-ending SDK rejection
(`ant/src/claude.ts:422`), structured provider errors, or anchored CLI error
envelopes with fixture-backed codes/messages (`ant/src/codex.ts:173`).
`Monthly spend limit reached` (`routd/failure_notice_test.go:47`) qualifies
only inside a provider error envelope. Informational usage events, assistant
quotations, tool errors, generic 403s and unknown errors stay per-turn.
Exit 78 alone never pauses: HTTP 400 content errors also produce it
(`ant/src/claude.ts:367`). Provider reset times come from those signals.

### The oldest held turn is the probe

One routd-owned row holds `reason`, `opened_at`, `next_attempt_at`, `attempts`
and sanitized evidence. `opened_at` identifies the episode. This pause also
holds BYOA keys and groups on another backend. That cost is accepted because
provider refusals are rare: one rule needs no credential identity computation.
The existing `GuardedBatch` behind `OpenTurnContext`
(`store/turn_context.go:149`, rebind at `store/turn_context.go:164`) is the
one serialization point. Its added guard admits normal turns only when the
pause is absent. Opening, claiming a probe and clearing use that same batch.
Already-admitted turns may finish; their success cannot clear the pause.
At `next_attempt_at`, the existing Loop (`routd/loop.go:314`) selects the oldest
held input by timestamp, then message ID and chat JID. Its conditional claim
atomically binds that turn to the episode and sets `next_attempt_at` null.
Null means a probe owns the episode; every other admission remains blocked.
The probe uses normal spawn, harness and timeout paths, without new runed/ant
surfaces, helpers or a separate probe scheduler.
Probe completion without `ProviderFailure` clears the episode and resumes
normal admission in held order. Ordinary content failures keep their per-turn
handling. Another `ProviderFailure` retains the episode and increments
`attempts`: delay doubles from 30 seconds to one hour, with bounded jitter.
A valid future provider reset time sets the delay instead. New messages and
repeated reports from the same attempt neither reset nor extend the delay.
A failure before spawn requeues the probe; it does not prove recovery.
Probe completion and manual clear condition on the episode and the bound
attempt, so late callbacks cannot clear a newer pause. Restart reads the row
before admitting work. Existing running-turn recovery resolves an interrupted
probe before rearming its due time; it cannot admit a second live probe.
Store errors hold admission. No second lease or in-memory authority exists.
ProviderFailure records the attempt and pause atomically, without completing
`turn_results` or calling `finishTurn` (`routd/turns.go:235`). Held messages keep
cursor, session, content, retry count and order. They bypass error marking,
breaker pruning, busy backoff and retention GC. Commands cannot advance a
cursor across held input (`routd/dispatch.go:79`). Recovery reuses the existing
turn rebind and closes each old run's callback surface.

### One notice and one operator control

Each affected chat gets one durable outbound row keyed by episode and chat:
"Model access is paused: <reason>. Your messages are saved. I will retry
automatically." Later messages and restart reuse that row. Forwarded replies
use their destination chat; outbound delivery retries keep their existing
backoff and adapter deduplication. Pausing stops typing while input waits.
The operator-only `model_pauses` resreg resource exposes list and clear through
one annotated REST/MCP handler with injected authorization and atomic audit.
The dashboard shows the row and queued count, with "Clear pause".
CLI `model-pauses list` and `model-pauses clear` use that resource.
Clear releases work; another provider refusal opens a new episode.

## The pause replaces overlapping controls

Delete queue counters/reset (`queue/queue.go:11`, `queue/queue.go:36`,
`queue/queue.go:132-136`, counter branches at `queue/queue.go:235-264`).
Delete runed's folder breaker (`runed/manager.go:139-152`,
`runed/manager.go:329-336`, `runed/manager.go:392-411`), its failure-store
helpers (`runed/runstore/circuit_breaker.go:34`) and `RunOutcome.BreakerOpen`.
Delete `onCircuitBreakerOpen` (`routd/loop.go:905-916`), its caller and metrics
(`routd/dispatch.go:609-612`, `routd/dispatch.go:641`). Keep capacity/exclusion.
Absorb durable suppression (`store/chats.go:171`, `routd/dispatch.go:592-605`)
into outbound IDs; remove the `already` branch at `routd/dispatch.go:598-600`.
`chats.errored` remains diagnostic. Ordinary failure notices use per-turn IDs.
Keep local-crash `MAX_TURN_RETRY`, outbound backoff and fresh-session recovery;
provider failures bypass turn retries.
Expected net production change: approximately 0 lines (±100), excluding tests.
This is an estimate; implementation reports actual added/deleted lines.

## Contract

- Fake provider: 429 then 200 through normal turns. No guest starts during the
  delay; exactly one oldest turn probes, clears the pause and preserves order.
  Removing the admission guard fails this test.
- A provider spend-limit envelope with exit 78 pauses; exit 78 with HTTP 400
  content failure, assistant lookalikes and unknown errors do not.
- Another probe refusal doubles delay; a reset time overrides it. Restart
  during both waiting and a running probe preserves input and single admission.
- Manual clear resumes held order; late probe completion cannot clear a newer
  episode. Repeated reports and failed notice delivery retain one outbound row.
- REST/MCP, dashboard and CLI prove operator-only list/clear and audit parity;
  the Go/TS ProviderFailure contract agrees. No runtime port changes occur.

## Contract tests

1. Normal recovery and admission guard:
   `routd/model_pauses_test.go::TestModelPauseNormalTurns429Then200`;
   polling and queue order: `routd/model_pause_order_test.go::TestModelPausePollHeldOrder`.
2. Provider envelopes and negative fixtures: `ant/src/provider-failure.test.ts`;
   unclassified terminal prose: `routd/failure_notice_test.go::TestFailureNoticePerChat`.
3. Doubling and reset: `store/model_pauses_test.go::TestModelPauseDuplicateBackoffResetAndLateCompletion`;
   restart: `routd/model_pauses_test.go::TestModelPauseRestartWaitingAndRunningProbe`
   and `TestModelPauseRestartAfterSubmissionResolvesTheBoundProbe`.
4. Clear and held order: `routd/model_pauses_test.go::TestModelPauseManualClearAndLateCallback`;
   stale completion: `store/model_pauses_test.go::TestModelPauseLateProbeCannotClearNewEpisodeAndHeldSurvivesGC`;
   callbacks across rebind: `routd/model_pause_race_test.go::TestModelPauseStaleCallbackAcrossRebind`;
   durable notice: `routd/model_pauses_test.go::TestModelPauseNoticeDurableBeyondAppendWindow`;
   command and elevation order: `routd/model_pause_order_test.go`.
5. Operator surfaces and audit: `routd/model_pauses_resource_test.go`,
   `dashd/model_pauses_test.go`, `cmd/arizuko/model_pauses_test.go`;
   shared declarations: `tests/contract_ant_test.go::TestProviderFailureFieldsMatchAcrossLanguages`;
   unchanged runtime port: `runed/model_pause_recovery_test.go::TestModelPauseKeepsRuntimePort`.
