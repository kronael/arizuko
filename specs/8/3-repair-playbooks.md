---
status: draft
depends-on: specs/8/2-state-evaluator.md
source: aeon mechanism #4 (category repair playbooks)
---

# 8/3 — Repair playbooks (typed-failure recovery skills)

A small set of opinionated skills the agent invokes when handling a
`verb=state-event` message. Not a new primitive — additions to
`ant/skills/`. The agent's persona dispatches `verb=state-event` +
matching `suggested_action` → corresponding `/repair-*` skill.

## Why opinionated playbooks beat freeform

When the agent gets `consecutive_low_scores`, freeform debugging
varies wildly per turn. A playbook codifies the steps that experienced
operators know work: re-read the API docs, check rate limit headers,
diff the schema, retry with backoff. The playbook captures the
_recipe_ once; the agent applies it consistently.

This is the same logic as `/facts` or `/onboard` skills — bottle the
recipe so the agent doesn't reinvent it. Aeon's category playbooks
formalise this for _failure recovery_ specifically.

## v1 playbook set

Five playbooks, mapping 1:1 to the state-evaluator conditions in
`8/2`.

### `/repair-api-change`

**Trigger**: `condition=flag_pattern_api_change`.

**Recipe**:

1. Identify which channel/tool flagged. Read recent `skill_health`
   reasoning entries for that channel.
2. Fetch the latest API docs for that channel (`davd` + web fetch).
3. Diff against the cached previous version (per-folder
   `facts/api-spec-<channel>.md`).
4. If a breaking change is detected, file a `bugs.md` entry in the
   folder AND queue a `/facts` refresh for that channel.
5. If false alarm (API unchanged), log to diary and exit.

**Output**: updated `facts/api-spec-*`, new `bugs.md` row if real,
diary entry.

### `/repair-rate-limit`

**Trigger**: `condition=flag_pattern_rate_limit`.

**Recipe**:

1. Read the last 10 outbound actions to identify which tool is being
   rate-limited.
2. Check the channel's published rate limit (headers cached in
   `facts/api-spec-*` if present).
3. Reduce poll cadence: write to `cooldowns(folder, "<channel>-poll",
now + backoff)`. Backoff doubles on each repair (cap 1h).
4. Diary entry with the new cadence.

**Output**: cooldown row, diary entry. Crucially does NOT touch the
channel adapter config — backoff is on the agent's outbound
behaviour, not on infra.

### `/repair-schema-change`

**Trigger**: `condition=flag_pattern_schema_drift`.

**Recipe**:

1. Read the suspected entity (DB row shape, API payload shape).
2. Compare against last-known-good schema cached in
   `facts/schema-<entity>.md`.
3. If real drift, queue a `/facts` refresh + file `bugs.md`.
4. Notify operator via routing to `<root>` folder if the drift
   affects more than one downstream.

### `/repair-stale-data`

**Trigger**: `condition=flag_pattern_stale_data`.

**Recipe**:

1. Identify which fact-source is stale (read folder's `facts/`
   directory ages).
2. Refresh the oldest fact via the source's `/facts` skill.
3. If refresh fails, file a `bugs.md` and escalate.

### `/repair-low-quality`

**Trigger**: `condition=consecutive_low_scores` (no specific flag).

**Recipe**:

1. Read the last 3 `skill_health` rows including reasoning.
2. If a common skill appears, suggest reviewing/editing that skill.
3. If no pattern, write a diary entry summarising the dip and
   request operator attention.
4. Output is purely advisory — playbook never auto-edits the
   persona or skill files. Operator gates everything.

## Why not auto-fix

The playbooks observe, suggest, refresh facts, and back off — they
don't auto-edit personas, grant new permissions, or push code. The
`9/7-self-learning` spec covers the proposal-and-operator-approval
loop for code/persona changes. Repair playbooks are the _immediate
response_ to a state-event; durable changes go through self-learning.

This split is intentional:

| Concern              | Spec   | Authority                 |
| -------------------- | ------ | ------------------------- |
| Immediate response   | `8/3`  | Agent (skill dispatch)    |
| Persistent learnings | `10/7` | Operator (proposal queue) |

A playbook can _propose_ a self-learning entry; the operator approves
it. The playbook itself does not write to persona/skills.

## Where they live

`ant/skills/repair-api-change/SKILL.md`, etc. — stock skills bundled
in the agent image. Operators can override per-folder by placing a
custom `groups/<folder>/.claude/skills/repair-api-change/SKILL.md`
which takes precedence (existing skill-resolution rules).

## Lifecycle

These ship under the standard skill migration flow
(`specs/4/P-personas.md ## Versioning`):

1. Add the skill files.
2. Add a migration entry in `ant/skills/self/migrations/`.
3. Bump `MIGRATION_VERSION`.
4. Rebuild agent image; auto-migrate broadcasts on next folder
   container start.

No infra changes. New skills, new MCP-visible recipes.

## Honest gaps

- **Coverage is narrow.** Five playbooks cover 5 conditions; real
  failure modes are richer. Expect playbooks to grow as
  `skill_health` accumulates data.
- **The "common skill" detection in `/repair-low-quality` is
  hand-wavy.** v1 reads the `skill` column on the last N rows; if
  most are `_turn`, the recipe degrades to "summarise the dip".
- **No way to chain playbooks.** If a `/repair-rate-limit` fires and
  succeeds, then immediately a `/repair-api-change` matches, the
  agent runs both in series via separate state-events. No combined
  recipe yet.
- **Playbooks are language-bound.** They live in markdown SKILL.md;
  the agent reads them. They don't compile to anything. That's the
  trade — easy to author, harder to test mechanically.

## Out of scope

- **Auto-rollback on bad deploys** — needs `12/d-agent-code-modification`
  to land first.
- **Cross-folder repair coordination** — when 10 folders all hit
  api-change, broadcast once instead of 10 parallel repairs. Belongs
  in a future "repair-orchestrator" layer if needed.
- **Telemetry beyond `skill_health`** — playbooks read only that
  table + the folder's own files. No new data sources in v1.
- **A repair-playbook DSL** — keep them as SKILL.md prose; the agent
  interprets.

## Decisions

- Playbooks are skills in `ant/skills/repair-*/`. Not a new
  primitive.
- Dispatch is via normal skill-on-verb routing. The agent's persona
  is taught to map `verb=state-event ∧ suggested_action=X` → `/X`.
- Playbooks observe and suggest; they don't auto-edit persona/skills/
  permissions. Durable changes flow through `10/7-self-learning`.
- v1 ships 5 playbooks; the catalog grows from `skill_health`
  pattern data.
- Per-folder override via `groups/<f>/.claude/skills/` — same as any
  other skill.
