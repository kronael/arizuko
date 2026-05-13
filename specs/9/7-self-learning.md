---
status: spec
sources:
  - hermes-agent peel (2026-04-11)
  - hermes-agent deep read (tmp/hermes-deep.md, 2026-05-13)
  - muaddib reference (2026-05-13)
  - letta docs (2026-05-13)
---

# Self-learning and self-improvement

Pattern recognition over a group's history that produces _proposals_
the operator reviews — never silent rewrites of agent state. Default
off; opt-in per channel via group config. The trust posture is the
spec — arizuko's main `CLAUDE.md` is opinionated that "platform stays
mechanical, operator owns truth," so this is a deliberate, gated
softening of that line, not a drift.

Threat-pattern scanning of agent-written skills is a separate concern
and lives in [8-skill-guard.md](8-skill-guard.md). The two specs
share no implementation surface: learning produces files for review,
the guard inspects bytes at write time.

## Why now

The agent today only learns when the user explicitly invokes
`/learn` (manual subagent, `ant/skills/learn/SKILL.md:1-12`). The
data is there — `episodes/`, `diary/`, message DB, per-user files
— but no cadence drives consolidation. Hermes, muaddib, and Letta
all have a continuous-loop equivalent; arizuko has only the manual
trigger.

## Loop shape

Two triggers, one renderer (the `proposals/` queue):

1. **Turn-counter nudge** — every N user turns since the last
   memory/skill write, the gateway prepends a `<nudge>` block to
   the user's message inviting `/learn` or `/diary` review. The
   agent decides whether to act. Source: hermes
   `run_agent.py:11202-11212` (trigger), `run_agent.py:1879-1887`
   (config), `run_agent.py:10446` (reset on tool use).

2. **Inactivity-triggered curator pass** — after the group has
   been idle for ≥ `min_idle_hours` AND ≥ `interval_hours` since
   the last pass, gated schedules a low-priority cron task that
   re-reads recent `episodes/` + `diary/` + per-user files and
   emits proposals. Source: hermes
   `agent/curator.py:1656-1675` (`maybe_run_curator`),
   `agent/curator.py:56-59` (defaults: 168h interval, 2h idle).

Both write to `~/proposals/` inside the group workspace. Nothing
mutates `~/PERSONA.md`, `~/.claude/skills/*/SKILL.md`,
`~/users/*`, or `~/CLAUDE.md` without operator acceptance.

## Pattern kinds

| Kind                       | Signal                                                               | Proposal artifact                                                                     |
| -------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| **Recurring question**     | Same user asks "what's the deploy command" 3× in 14 days             | New `~/.claude/skills/<topic>/SKILL.md` stub                                          |
| **Recurring failure**      | Same tool call returns the same error 5×                             | `~/proposals/skill-<topic>.md` with the workaround                                    |
| **Recurring tool pattern** | Agent uses `inspect_messages` + `get_thread` together ≥ N times      | Composite skill stub                                                                  |
| **Stale memory**           | `users/<id>.md` not touched in 90 days, contradicted by recent turns | `~/proposals/memory-<id>.md` with proposed merge                                      |
| **Persona drift**          | User corrects register ≥ 3 times in a week                           | `~/proposals/persona-<reason>.md` — a _diff_ against `PERSONA.md`, never an overwrite |

Threshold N is per-kind and lives in `ant/CLAUDE.md` frontmatter
under `learning.thresholds:`. Sensible defaults are 3 for
recurrences, 5 for failures.

## Skill emergence

When the curator pass spots a pattern matching the **recurring
question** or **recurring failure** kind ≥ N times, it writes a
proposal file (not a skill). The proposal is a fully-formed
SKILL.md body in `~/proposals/skill-<slug>.md` with one extra
header line:

```yaml
---
proposal_kind: skill
evidence:
  - episodes/20260411.md#deploy-fail
  - diary/20260415.md#deploy-fail-redux
  - users/telegram-1234.md (asked twice)
target_path: ~/.claude/skills/deploy-runbook/SKILL.md
---
```

Operator reviews via `/dash/groups/<folder>/proposals` (new dashd
surface — see "Operator gate" below). Acceptance moves the file to
`target_path`; rejection deletes it. No third state.

This mirrors hermes' `skill_manage(action="create")` flag —
agent-created skills are marked so the curator can find them later
(`tools/skill_manager_tool.py:773`, per `tmp/hermes-deep.md:271-274`).
arizuko's twist: the file never reaches the live skill path until
the operator accepts.

## Memory curation

Older entries in `users/`, `diary/`, and `facts/` get summarised
by `/compact-memories` already (cron-driven, day → week → month).
Self-learning adds one more pass on top: when a curator run finds
two `users/<id>.md` claims that contradict each other or a fact
older than 30 days that recent turns no longer support, it writes a
`~/proposals/memory-<id>.md` with the proposed merge. Operator
accepts; no auto-edit.

Inspiration:

- hermes lifecycle states (Active → Stale → Archived, 30/90 days,
  `agent/curator.py:56-59`) — arizuko's equivalent is the proposal
  file's `evidence:` list, not a status column on every record.
- muaddib's session-end wrap-up envelope
  (`src/rooms/command/command-executor.ts:980-1004`) — runs the
  memory update prompt with a `<meta>` envelope that says
  "DO NOT RESPOND ANYMORE." arizuko reuses this envelope wording
  for the curator's prompt to avoid the model leaking a user-facing
  reply from a background pass.

## Persona refinement

Hardest case, weakest signal. Operator owns `PERSONA.md`. The
learning loop can only propose a _diff_ — never an edit — and only
when the user has corrected the register ≥ 3 times in 7 days
(corrections detected as messages of the form "stop X", "don't
say Y", "be shorter", measured by simple regex + retry tracking,
not LLM judgement at this layer).

Proposal: `~/proposals/persona-<slug>.md` with a unified diff
header and the evidence list. Operator pastes the diff into
`PERSONA.md` if accepted. Auto-acceptance for persona is
**permanently disabled** — too easy to drift the agent's voice
away from what the operator wants.

## Pre-mutation snapshot

Every accepted proposal first snapshots the prior file. Snapshot
location: `~/.snapshots/YYYYMMDD-HHMMSS-<kind>-<slug>.tar.gz`.
Mirrors hermes `agent/curator.py:1320-1329` (best-effort tarball
before the curator mutates the skill tree; failure logs at debug
and continues, never blocks).

Rollback is one command: `/dash/groups/<folder>/proposals/rollback`
restores the most recent snapshot for the given artifact. The
snapshot directory is glob-cleaned by `/compact-memories` after
180 days (snapshots are recoverable until then).

## Operator gate

By default, every proposal is gated. Three modes per group
(`learning.mode` in group config):

| Mode                                      | Memory      | Skills | Persona |
| ----------------------------------------- | ----------- | ------ | ------- |
| `off`                                     | n/a         | n/a    | n/a     |
| `proposals` (default when learning is on) | gated       | gated  | gated   |
| `auto-low-risk`                           | auto-accept | gated  | gated   |

Persona is **always gated**, regardless of mode. There is no
`auto-all` mode by design.

The dashd surface is new: `/dash/groups/<folder>/proposals` lists
pending files with diff previews; accept/reject buttons trigger
the snapshot + move. Implementation reuses existing dashd
file-edit primitives — no new daemon.

## Cadence

Both knobs in group config (`ant/CLAUDE.md` frontmatter under
`learning:`):

| Knob                     | Default      | Hermes parallel       |
| ------------------------ | ------------ | --------------------- |
| `nudge_interval_turns`   | 10           | `run_agent.py:1879`   |
| `curator_min_idle_hours` | 2            | `agent/curator.py:57` |
| `curator_interval_hours` | 168 (7 days) | `agent/curator.py:56` |
| `thresholds.recurrence`  | 3            | n/a                   |
| `thresholds.failure`     | 5            | n/a                   |

A new daemon is **not** needed — `timed` schedules the curator
pass like any other cron task; the trigger payload reads idle
seconds from `groups.last_message_at`. See `timed/main.go` for
the cron entry point.

## Trust posture (be explicit)

The main `CLAUDE.md` rule "platform stays mechanical, operator
owns truth" tilts against this whole feature. The reconciliation:
**learning produces files in `~/proposals/`, never edits live
agent state.** Acceptance is a human action through dashd.
Auto-acceptance is restricted to memory updates (the lowest-risk
kind, fully reversible via snapshot) and only when the operator
explicitly opts into `auto-low-risk`.

Default ships `off`. Operators turn it on per group via
`/dash/groups/<folder>/settings`.

## Cross-project reference

| Project                 | Mechanism                                                                              | Source                                                                                  |
| ----------------------- | -------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| hermes-agent            | Curator + turn nudges + lifecycle states                                               | `agent/curator.py:1656-1675`, `run_agent.py:11202-11212`                                |
| muaddib                 | Session-end memory wrap-up via `<meta>` envelope + heartbeat file                      | `src/rooms/command/command-executor.ts:980-1004`, `src/agent/skills/heartbeat/SKILL.md` |
| openclaw-managed-agents | External-only `PATCH /v1/agents/:id` + `/archive`; no internal self-update path        | `src/orchestrator/server.ts:875-948`                                                    |
| Letta                   | `core_memory_append` / `core_memory_replace` agent-callable tools                      | https://docs.letta.com/guides/agents/memory                                             |
| agno                    | `enable_agentic_memory=True` — agent gets memory CRUD tools (names not in public docs) | https://docs.agno.com                                                                   |
| AnythingLLM             | RAG-only; no explicit self-learning loop                                               | n/a                                                                                     |

Gap: Letta and agno docs are thin — the canonical tool names
(`core_memory_append`/`replace` for Letta) are documented but the
trigger cadence isn't. Inferred-not-verified.

## Out of scope

- **Pre-tool-call threat scanning** — see
  [8-skill-guard.md](8-skill-guard.md). Different concern,
  different surface (`PreToolUse` hook on `Write`/`Edit`), different
  lifecycle (per-call, not per-pattern).
- **Auto-skill-acceptance** — never. Skills carry token cost in
  every session; an unreviewed skill silently grows the system
  prompt. Operator gate is permanent.
- **LLM-based pattern detection inside the gateway** — patterns
  are detected by SQL + regex against the message DB; the LLM
  judgement happens _inside the curator's review pass_, not at
  trigger time. Cheaper, deterministic, debuggable.

## Files (when built)

- `gateway/learning.go` — nudge counter, curator scheduler
- `gateway/proposals.go` — proposal write/list/accept/reject
- `dashd/proposals.go` — `/dash/groups/<folder>/proposals` UI
- `ant/skills/learn/SKILL.md` — already exists; update to read
  `~/proposals/` and treat curator-written files as drafts
- `store/migrations/NNN-learning.sql` — `proposals` table
  (id, folder, kind, target_path, evidence_json, created_at,
  reviewed_at, status). Status enum: `pending`, `accepted`,
  `rejected`. Three states; "pending" is the default.

~600 LOC Go + ~200 LOC dashd templates + one migration.
