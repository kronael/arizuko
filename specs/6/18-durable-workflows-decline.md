---
status: draft
source: paradigmxyz/centaur@20f3021 (Apache-2.0) peel, 2026-08-10
---

# specs/6/18 — durable workflow engine: decline

A costed decision NOT to build, in the `5/5`/`5/16`-step-2 tradition.
Centaur ships a real durable-execution engine — the honest kind, not a
job queue with good ergonomics — and after reading it end to end the
recommendation is still to keep [`5/A`](../5/A-primitives-framing.md)
decision 5: workflow is an operating discipline at the session boundary,
not a seventh primitive. This spec records what the engine actually is,
so the next "should arizuko have workflows?" debate starts from evidence
instead of vibes.

## What Centaur ships

Postgres-backed durable execution in the Temporal replay style, via a
vendored `absurd` library. All paths are into `paradigmxyz/centaur` at
`20f3021`.

- **Four tables per queue**: tasks (state machine
  `pending/running/sleeping/completed/failed/cancelled`), runs (one per
  attempt, lease columns `claimed_by`/`claim_expires_at`/`available_at`),
  checkpoints keyed `(task_id, checkpoint_name)`, and waits linking a
  suspended run to an `event_name`
  (`services/api-rs/crates/centaur-session-sqlx/migrations/0007_absurd_workflows.sql:151-230`).
- **Checkpoint-or-execute replay.** The handler re-runs from the top on
  every resume; `TaskContext::step(name, f)` returns the stored result
  when the checkpoint exists and only otherwise executes the closure
  (`services/api-rs/crates/absurd-sdk/src/lib.rs:1215-1229`). Repeated
  step names get a deterministic counter suffix `name#2`, `name#3`
  (`absurd-sdk/src/lib.rs:1462-1475`).
- **Sleep without a timer daemon**: `sleep_until` checkpoints the wake
  time, flips the row to `sleeping, available_at=wake`, and aborts the
  handler with `Err(Error::Suspend)`; any worker's normal claim poll
  (`WHERE available_at <= now()`, `0007_absurd_workflows.sql:936-946`)
  wakes it (`absurd-sdk/src/lib.rs:1272-1290`).
- **External events**: `await_event` suspends the run; `emit_event`
  first-write-wins the event row and flips matching waits back to
  `pending` in one CTE (`0007_absurd_workflows.sql:1817-1909`).
- **Worker model**: `FOR UPDATE SKIP LOCKED` claims, expired leases
  swept and failed in the claim transaction, at-least-once with
  idempotency via the checkpoint layer; a `LeaseWatchdog` process-exits
  a worker at 2x claim timeout (`absurd-sdk/src/lib.rs:1611-1636`).
- **Poison-pill guard**: a `RemovedWorkflowReaper` force-cancels tasks
  whose workflow vanished from discovery, otherwise a deleted workflow
  retries forever
  (`services/api-rs/crates/centaur-workflows/src/lib.rs:1976-1983`).

## What even they don't have

Two gaps, both structural, both in the part that is actually hard:

- **Zero determinism enforcement.** Nothing verifies that replay
  reproduces the prior call sequence — the checkpoint-name counter is an
  unchecked assumption, and there is no workflow-code versioning. Code
  changed mid-flight silently misattributes checkpoints instead of
  failing loud. Their only guard is a doc sentence ("keep step names
  stable after deployment", `docs/pages/extend/workflows.mdx:91-92`).
- **Doc/code drift on a core verb**: `ctx.wait_for_workflow` /
  `ctx.run_workflow` are documented (`docs/pages/extend/workflows.mdx:86`)
  but absent from the Python-bridge protocol handler
  (`centaur-workflows/src/lib.rs:3397-3450` implements fire-and-forget
  `workflow.start` only).

And a telling deployment fact: of their four production queues, three
carry ETL/sync jobs (`centaur_workflows`, `_slack_live`, `_etl`,
`_etl_backfill`; `0036_readonly_all_workflow_queues.sql:1-89`;
`workflows/attio_sync.py`). The engine mostly hauls data pipelines, not
agent orchestration — agent turns block inline via `ctx.agent_turn`.

## What arizuko has instead

The recovery unit is the **turn**, and the replay engine is the agent.

- A failed turn retries whole, with a retry note injected into the next
  prompt (`routd/dispatch.go:308-318`, `routd/loop.go:126` `maxTurnRetry`,
  spec `5/12`) — no step granularity, by design.
- Sleep/cron is `timed`: due task → message row → ordinary turn
  (`timed/split.go`, `timed/README.md`).
- Wait-for-event is routing: the event arrives as a message and triggers
  the turn that handles it (`5/A` "No special cases" table — scheduled
  tasks, autocalls). No suspended execution exists to wake.
- Child work is `delegate_group`/`escalate_group` (`ipc/ipc.go:1828`),
  depth-capped, through the org tree.
- Durability is the two stores: DB rows + the folder
  (`5/A` §The primitives). An agent resumes by reading state and
  deciding what remains — LLM-native recovery instead of code replay.

## The delta, costed

Adopting the absurd shape means: a lease-claim engine re-implemented on
SQLite (no `FOR UPDATE SKIP LOCKED`; claim columns + WAL polling —
buildable, but a new engine), a checkpoint SDK surface inside `ant/`, a
worker/queue operational plane (stuck runs, lease sweeps, poison-pill
reaping, per-queue concurrency), and — the real cost — the determinism
discipline Centaur itself does not enforce, imposed on code written by
an LLM per turn. That last one is disqualifying: checkpoint-replay
assumes deterministic orchestration code, and arizuko's orchestrator is
a model. The primitive would sit under exactly the component least able
to honor its contract.

## Decision

Decline. `5/A` decision 5 stands. What Centaur's engine buys —
crash-safe multi-step side-effect sequences with per-step cost — is not
a product arizuko sells; the conversational/turn recovery model covers
the platform's actual load.

**Reopen trigger**: a shipped product needs a multi-hour side-effecting
job where re-running the whole turn is unacceptable (paid API calls per
step, bulk migrations). The shape to copy then is absurd's four tables +
checkpoint-or-execute `step()` — mechanical, well-factored, and the SQL
is readable — NOT a workflow DSL, and NOT the undelivered child-await
API. Wire it as its own daemon owning its own DB, per the split
discipline.

## Attribution

Analysis derives from reading `paradigmxyz/centaur` (Apache-2.0), commit
`20f3021`: `services/api-rs/crates/absurd-sdk/`,
`services/api-rs/crates/centaur-workflows/`,
`services/api-rs/crates/centaur-session-sqlx/migrations/`,
`services/workflow-python/`, `docs/pages/extend/workflows.mdx`. No code
was copied.
