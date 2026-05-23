---
status: draft
supersedes: specs/12/8-self-eval-skill.md (judge-model choice + data shape)
source: aeon mechanism #2 (Haiku-scores-Opus); reference_aeon.md
---

# 8/1 — Haiku-as-judge self-eval

After every container turn, run a second short Anthropic call against
Haiku that reads the run transcript + outbound action and emits a
JSON verdict. Persist that verdict to `skill_health`. Same shape, every
run, every folder.

## Why Haiku, not the same model

Aeon's design: Opus does the work, Haiku judges. Two reasons:

1. **Cheaper.** Self-eval fires on every turn; a same-model judge
   doubles the bill. Haiku is ~10x cheaper.
2. **Less reflexive.** A model judging its own output tends to
   rationalise. A different model — even a smaller one — gives the
   self-eval signal more independence. The judge isn't authoritative
   ground-truth; it's a noise floor on quality.

This supersedes the `specs/12/8-self-eval-skill.md` design where the
judge was a restricted `query()` in the same container with the same
model. That spec is marked superseded on the model + data-shape
axes; the trigger-shape research (run at container exit, not on cron)
carries through unchanged.

## Verdict shape

JSON, written by Haiku, one object per turn:

```json
{
  "score": 4,
  "flags": ["api_change_suspected", "rate_limited"],
  "reasoning": "Reply was correct but Github API returned 422 on first attempt; agent recovered with retry. Suggests the schema may have moved."
}
```

| Field       | Type           | Notes                                                    |
| ----------- | -------------- | -------------------------------------------------------- |
| `score`     | int 1-5        | 5=clean, 1=disaster. Anchor: 3=neutral, no clear signal. |
| `flags`     | string[]       | Free-form tags. Stable vocab grows from `8/3` playbooks. |
| `reasoning` | string (≤500c) | Operator-readable; cited by `8/2` when firing triggers.  |

Stable flag vocab to seed (each maps to a repair playbook in `8/3`):
`api_change_suspected`, `rate_limited`, `schema_drift`, `stale_data`,
`auth_failure`, `output_low_quality`, `empty_response`. Free-form is
allowed; the rolling history surfaces emergent flags via
operator-visible aggregates.

## Table: `skill_health`

```sql
CREATE TABLE skill_health (
  folder       TEXT NOT NULL,
  skill        TEXT NOT NULL,      -- skill name or "_turn" for whole turn
  ts           INTEGER NOT NULL,   -- unix seconds
  score        INTEGER NOT NULL,   -- 1-5
  flags        TEXT NOT NULL,      -- JSON array
  reasoning    TEXT NOT NULL,
  message_id   TEXT,               -- link back to the run
  PRIMARY KEY (folder, skill, ts)
);
CREATE INDEX skill_health_recent ON skill_health (folder, skill, ts DESC);
```

Owned by `gated` (it owns all migrations). Other daemons connect
read-only.

`skill` column logic:

- If the run invoked a single skill (`/repair-api-change`, `/facts`,
  etc.), the row is keyed to that skill name.
- If the run was a normal turn with no skill-as-prime, key it
  `_turn`. The aggregate "how is this folder doing" view groups
  on `_turn`.

## Rolling history

Per `(folder, skill)`, keep the last 30 rows. Aggregate views in
`8/2`'s state-evaluator read from this directly — no separate
rolling-buffer table. SQLite `LIMIT 30` on the recent index is enough
at our volume.

Retention beyond 30 rows: keep, don't prune. Disk is cheap;
`8/4`-style longitudinal analysis benefits from the long tail. Add a
cleanup job in `timed` only when disk pressure shows.

## Trigger

Sub-`query()` at container exit, NOT a separate cron. (Same trigger
choice as `specs/12/8-self-eval-skill.md`; that part holds.) Fires on
every successful run. Cost gate via existing per-folder budget — if
the folder is over its cap, skip the judge and write a `score=NULL`
sentinel row.

## Runner shape

A new sub-call in `ant/src/index.ts`'s post-turn hook, OR a new
skill-judge daemon. Decision: in-container sub-call for v1 (no new
daemon, no IPC). Same `query()` shape as the existing main turn —
just with the Haiku model, restricted tools, and a fixed prompt.

```typescript
// ant/src/self-eval.ts (sketch)
const verdict = await query({
  model: 'claude-haiku-4-5',
  systemPrompt: SELF_EVAL_PROMPT,
  allowedTools: [], // no tools; judging only
  maxTurns: 1,
  responseFormat: { type: 'json_object' },
  messages: [{ role: 'user', content: buildJudgeContext(transcript) }],
});
await mcp.call('write_skill_health', { folder, skill, ...verdict });
```

The `write_skill_health` MCP tool is a single new entry on the agent
socket — gated-side handler appends a row.

## Restricted prompt

The judge sees only:

- The user message that started the turn
- The tool calls the agent made + their results (text content only;
  no file contents larger than 4KB)
- The final outbound action

The judge does NOT see persona, memory, or skill files. The judge
scores the work, not the personality.

## Cost model

Haiku at ~$1/MTok input. A typical judge call: ~3KB context + ~200B
output = ~$0.003 per turn. At 10k turns/month/instance: ~$30/month.
Acceptable; cap-able via `SELF_EVAL_ENABLED=0`.

## Honest gaps

- **No ground-truth labels.** Score=4 means "Haiku thinks this was
  fine". The judge can be wrong. Use scores as a noise floor over a
  window (3-of-last-5 < 2) rather than as a gate on any one turn.
- **Skill attribution is loose.** A turn that runs `/facts` and then
  replies normally is ambiguous — was the reply judged, or the
  facts-build? v1 keys to the prime skill; if absent, `_turn`. Better
  attribution needs per-skill sub-runs (deferred).
- **Judge model drift.** If Anthropic ships Haiku 5, scores from old
  vs new judges aren't comparable. Stamp `judge_model` on each row.
- **Adversarial agent.** A misbehaving agent could try to influence
  the judge via its outbound. The judge prompt explicitly instructs
  to score the _work_, not the persuasiveness — but this is not a
  hard boundary.

## Out of scope

- **Routing on score.** The state-evaluator (`8/2`) reads
  `skill_health` and decides what to fire. This spec only writes.
- **Operator UI.** A dashd `/health` page comes in `9/18-daemon-dashboards`
  scope; not blocked by this spec.
- **Auto-revert on low score.** That's `8/3` repair-playbook
  territory.
- **A second judge for ensembling.** Single-judge v1; multi-judge
  voting is a phase-12 candidate.

## Decisions

- Judge model is Haiku, not the same model as the main turn.
- Verdict is JSON `{score, flags, reasoning}`, not free text.
- Storage is one `skill_health` row per turn (or per-skill where
  attributable). No aggregation table — `8/2` aggregates on read.
- Trigger is in-container sub-call at turn exit, not a separate
  daemon. Reuses the run's container, no new IPC.
- `gated` owns the migration. New MCP tool: `write_skill_health`.
- Cost-gated by per-folder budget; over-cap turns write
  `score=NULL` sentinel.
