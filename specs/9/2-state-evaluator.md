---
status: draft
depends-on: specs/9/1-self-eval-haiku.md
source: aeon mechanisms #3 (reactive state triggers) + #9 (cooldown)
---

# 6/2 — State evaluator (reactive message producer)

A small periodic worker reads `skill_health` (plus a `cooldowns` table)
and, when conditions match, **writes a message** with `verb=state-event`
to the bus. From there the agent picks it up like any other inbound,
dispatches to a skill (typically a repair playbook from `6/3`), and
the loop closes.

No new trigger type in the gateway. No new MCP tool. No new daemon
shape. Same message-producer pattern as `timed`. The seam is the
table, not a new primitive.

## Why this shape

Aeon's "reactive state machine triggers" are conceptually richer (a
small DSL: `consecutive_failures >= 3 AND not_in_cooldown`) but
that's GHA-shaped. In arizuko, message routes + skill dispatch
already handle the "what to do" half. We need only the "when to fire"
half — which is a SQL query on `skill_health` joined against
cooldowns.

A small Go worker beats a DSL. Conditions live in code, are
versioned with the daemon, easy to add. Spec is what to write down
about each condition.

## Placement: absorb into `timed`, or new daemon?

Decision: **absorb into `timed`**.

Why:

- `timed` already polls on a tick, already writes messages to the bus,
  already has the DB handle.
- The condition list is small (5-10 in v1). Doesn't justify a new
  binary.
- One less daemon to operate. CLAUDE.md "boring code" rule.

Cost: `timed` grows a `state_evaluator.go` module + a new poll job
on a slower tick (60s, vs `timed`'s second-resolution scheduling).
If this metastasises (50+ conditions), promote to `evald` then.

## The `cooldowns` table

```sql
CREATE TABLE cooldowns (
  actor    TEXT NOT NULL,   -- folder OR "global"
  action   TEXT NOT NULL,   -- e.g. "repair-api-change"
  until_ts INTEGER NOT NULL,
  reason   TEXT,
  PRIMARY KEY (actor, action)
);
```

When the evaluator fires a state-event message, it also writes (or
upserts) a row: `(folder, "repair-api-change", now+86400, "fired")`.
Subsequent ticks check this row before firing again.

Default cooldown: 24h per (folder, action). Override per-condition.

Cleanup: rows with `until_ts < now` are stale; either prune lazily on
read or with a daily sweep in `timed`.

## v1 condition set

| Name                        | When                                                                | Fires              | Cooldown |
| --------------------------- | ------------------------------------------------------------------- | ------------------ | -------- |
| `consecutive_low_scores`    | Last 3 `_turn` rows for folder all score ≤ 2                        | `verb=state-event` | 24h      |
| `flag_pattern_api_change`   | ≥2 of last 5 rows have `api_change_suspected` flag                  | `verb=state-event` | 24h      |
| `flag_pattern_rate_limit`   | ≥3 of last 10 rows have `rate_limited` flag                         | `verb=state-event` | 12h      |
| `flag_pattern_schema_drift` | ≥2 of last 5 rows have `schema_drift` flag                          | `verb=state-event` | 24h      |
| `flag_pattern_stale_data`   | ≥3 of last 10 rows have `stale_data` flag, AND last data fetch >24h | `verb=state-event` | 6h       |
| `judge_silenced`            | No `skill_health` rows in 24h for an active folder                  | `verb=state-event` | 48h      |

Each condition has a constant name, a SQL query (or Go predicate),
and a payload-builder that fills the message:

```
verb=state-event
condition=consecutive_low_scores
folder=corp/eng/sre
context={ score_history: [...], flagged_skills: ["..."] }
suggested_action=/repair-low-quality
```

The agent's persona will dispatch on `verb=state-event` →
`/repair-<condition>` skill (see `6/3`). If no playbook matches, the
agent reads `context` and decides freeform.

## Why message-driven, not direct skill invocation

Three reasons to write a message instead of poking the agent
directly:

1. **Auditability.** Every state-event has a row in `messages`. The
   reasoning, the timestamp, and the agent's response are all in one
   table. Direct skill-call has no audit row.
2. **Routing reuse.** Folder routing, grants, and rate limits all
   apply uniformly. A state-event to a quiet folder respects the
   same gates as a user message.
3. **Replay.** A bad condition that fires too often can be muted by
   the same route-drop machinery as any noisy channel.

The message is marked with a system source-tag so the agent can
distinguish it from user input but treats it as a normal turn.

## Trigger discipline

- **Tick interval**: 60s. Smaller is wasteful (Haiku writes lag the
  data); larger delays repair.
- **One condition per tick per folder**: if multiple conditions match
  simultaneously, fire the highest-priority one and cool down the
  others for the same tick. Prevents thundering herd.
- **Global mute**: an env var `STATE_EVAL_ENABLED=0` silences all
  conditions. Operator escape hatch.
- **Per-folder mute**: row in `cooldowns` with `actor=<folder>,
action="_all", until_ts=...` — added via a CLI or chat command.

## Honest gaps

- **Conditions are coded, not configurable.** Adding a new condition
  is a code change + daemon rebuild. That's the trade for not
  building a DSL. If conditions multiply, revisit.
- **Cooldown is heuristic.** 24h is a guess. Different conditions
  warrant different cooldowns; the table allows per-row override but
  the defaults are eyeballed.
- **No backpressure feedback.** If the agent ignores the state-event
  (no skill matched, no acknowledgement), the cooldown still
  consumes the slot. Mitigation: state-event payload includes
  `urgency=low|normal|high`; if `low` and ignored, next tick can
  retry sooner.
- **Cross-folder conditions** (e.g. "if 5 folders all report
  schema_drift, broadcast a platform-wide notice") are out of scope
  for v1. They're the natural extension to v2.

## Out of scope

- **Direct agent invocation.** Always via the bus.
- **Score smoothing / EWMA.** Crude `count >= N` predicates first;
  smoothing if false-positives sting.
- **Operator UI for conditions.** Dashd may surface fired conditions
  but the condition set itself is code.
- **Cross-instance evaluation.** Each instance evaluates its own
  `skill_health` table.

## Decisions

- The evaluator is a module inside `timed`, not a new daemon.
- It writes messages with `verb=state-event`, payload includes
  `condition`, `context`, `suggested_action`.
- `cooldowns` is a flat table keyed on `(actor, action)`. No tiers,
  no exponential backoff in v1.
- Conditions are Go code, not config. Spec is what gets
  reviewed when adding new ones.
- Tick is 60s. One condition fires per folder per tick.
- Env kill-switch `STATE_EVAL_ENABLED=0` for emergencies.
