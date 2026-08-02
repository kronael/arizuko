---
status: draft
source: aeon mechanisms #2 (Haiku-scores-Opus), #3 (reactive triggers), #4 (repair playbooks), #9 (cooldown)
---

# specs/10/1 — the self-healing loop

Three mechanisms imported from [Aeon](https://github.com/aaronjmars/aeon),
which runs on cron in GitHub Actions. The architecture does not port; the
loop does:

1. **Judge** — after every turn, a cheap second model scores the run and
   writes a `skill_health` row.
2. **Evaluate** — a periodic worker reads that history and, on a matching
   condition, writes a `verb=state-event` message to the bus.
3. **Repair** — the agent picks that message up like any other inbound and
   dispatches to a `/repair-*` skill.

They are one spec because they are one loop: each is inert without the
others, and the seams between them are two SQLite tables plus the existing
message bus. Nothing else is new — no trigger type, no daemon shape, no MCP
surface beyond one write tool.

## 1. Judge — Haiku, not the same model

Opus does the work; Haiku judges. Two reasons, and the second is the real
one:

- **Cheaper.** Self-eval fires every turn; a same-model judge doubles the
  bill.
- **Less reflexive.** A model judging its own output rationalises. A
  different model gives the signal independence. The judge is a noise floor
  on quality, never ground truth.

Verdict is JSON — `{score: 1-5, flags: string[], reasoning: ≤500 chars}` —
not free text, so the evaluator can predicate on it. Seed flag vocabulary,
each mapping to a playbook: `api_change_suspected`, `rate_limited`,
`schema_drift`, `stale_data`, `auth_failure`, `output_low_quality`,
`empty_response`. Free-form flags are allowed; emergent ones surface in the
aggregate.

The judge sees only the triggering user message, the tool calls and their
results, and the final outbound action. It does **not** see persona, memory,
or skill files — it scores the work, not the personality.

Trigger is an in-container sub-call at turn exit, not a cron and not a new
daemon: it reuses the run's container and needs no IPC. Over-budget folders
skip the judge and write a `score=NULL` sentinel so a gap in the history is
distinguishable from a healthy silence.

```sql
CREATE TABLE skill_health (
  folder      TEXT NOT NULL,
  skill       TEXT NOT NULL,      -- skill name, or "_turn" for a whole turn
  ts          INTEGER NOT NULL,
  score       INTEGER,            -- 1-5; NULL = judge skipped (over budget)
  flags       TEXT NOT NULL,      -- JSON array
  reasoning   TEXT NOT NULL,
  judge_model TEXT NOT NULL,      -- scores from different judges aren't comparable
  message_id  TEXT,
  PRIMARY KEY (folder, skill, ts)
);
```

## 2. Evaluate — a module in `timed`, not a new daemon

`timed` already ticks, already writes messages to the bus, and the condition
list is small. It grows a state-evaluator module on a slow (60s) tick. If
the condition set ever reaches ~50, promote it to its own daemon then, not
now.

Conditions are **Go code, not a DSL**. Aeon's richer trigger language is
GHA-shaped; here, message routes and skill dispatch already handle the "what
to do" half, so only "when to fire" is needed — and that is a SQL predicate.
The trade is explicit: adding a condition is a code change and a rebuild.

The v1 set predicates on windows, never a single turn — a judge can be
wrong, so `3-of-last-5` is the shape:

| Condition                   | When                                               | Cooldown |
| --------------------------- | -------------------------------------------------- | -------- |
| `consecutive_low_scores`    | last 3 `_turn` rows all score ≤ 2                  | 24h      |
| `flag_pattern_api_change`   | ≥2 of last 5 rows flagged `api_change_suspected`   | 24h      |
| `flag_pattern_rate_limit`   | ≥3 of last 10 rows flagged `rate_limited`          | 12h      |
| `flag_pattern_schema_drift` | ≥2 of last 5 rows flagged `schema_drift`           | 24h      |
| `judge_silenced`            | no `skill_health` rows in 24h for an active folder | 48h      |

```sql
CREATE TABLE cooldowns (
  actor    TEXT NOT NULL,   -- folder, or "global"
  action   TEXT NOT NULL,   -- e.g. "repair-api-change"; "_all" mutes a folder
  until_ts INTEGER NOT NULL,
  reason   TEXT,
  PRIMARY KEY (actor, action)
);
```

**Why a message, not a direct skill call.** Three reasons, and they are the
reason this rides the bus at all: every state-event gets a `messages` row so
the trigger, the reasoning and the agent's response sit in one table
(auditability); folder routing, grants and rate limits apply uniformly
(routing reuse); and a condition that fires too often is muted by the same
route-drop machinery as any noisy channel (replay). Direct invocation has
none of the three.

Discipline: one condition per folder per tick — highest priority wins, the
rest cool down — so a bad day can't produce a thundering herd.
`STATE_EVAL_ENABLED=0` is the operator kill switch.

## 3. Repair — playbooks are skills, not infrastructure

The `/repair-*` set drops into `ant/skills/` and is invoked by normal
dispatch when the agent handles `verb=state-event`. Nothing in the platform
knows they exist.

Why opinionated recipes beat freeform: given `consecutive_low_scores`,
freeform debugging varies wildly per turn. A playbook bottles what
experienced operators know works — re-read the API docs, check rate-limit
headers, diff the schema, retry with backoff — so the agent applies it
consistently. Same logic as `/facts` or `/onboard`.

One binding constraint learned from the rate-limit case: a playbook adjusts
the **agent's own outbound behaviour** (writing a `cooldowns` row for its
polling), never the channel adapter's config. Repair stays inside the
agent's blast radius; infra is the operator's.

## Where the tables live

`routd` owns both tables and their migrations. `timed` owns no DB in the
split — it reads task rows from routd's `/v1` over its `service:timed`
bearer, and the evaluator reads `skill_health` the same way. The judge
writes through one new MCP tool on the agent socket (`write_skill_health`),
which routd handles like any other resource write.

This is the part most likely to be got wrong by copying Aeon directly:
Aeon's evaluator owns its own state, ours does not.

## Honest gaps

- **No ground-truth labels.** `score=4` means "Haiku thought this was fine".
  Hence windowed predicates, never a gate on one turn.
- **Skill attribution is loose.** A turn that runs `/facts` then replies
  normally is ambiguous. v1 keys to the prime skill, else `_turn`; better
  attribution needs per-skill sub-runs.
- **Judge drift.** A new Haiku generation makes old and new scores
  incomparable — hence `judge_model` on every row.
- **Adversarial agent.** A misbehaving agent could try to influence its
  judge through its outbound. The prompt instructs scoring the work, not the
  persuasiveness; that is guidance, not a hard boundary.
- **No backpressure.** If the agent ignores a state-event, the cooldown
  still consumes the slot.

## Out of scope

Cross-folder and cross-instance conditions (each instance evaluates its
own); score smoothing/EWMA before crude counting has been shown to
false-positive; multi-judge ensembling; an operator UI for editing
conditions — `dashd` may surface fired conditions ([`../7/2-dashd-hub.md`](../7/2-dashd-hub.md)),
but the condition set stays code.
