---
status: draft
sources:
  - hermes-agent peel (2026-04-11)
  - hermes-agent deep read (tmp/hermes-deep.md, 2026-05-13)
  - muaddib reference (2026-05-13)
  - letta docs (2026-05-13)
---

# Self-learning and self-improvement

Pattern recognition over a group's history that produces _proposals the
operator reviews_ — never silent rewrites of agent state. Default off,
opt-in per group.

Threat-pattern scanning of agent-written skills is a **separate concern**
([23-skill-guard.md](23-skill-guard.md)): learning produces files for
review, the guard inspects bytes at write time. They share no
implementation surface.

## Why

The agent only learns when the user invokes `/learn` by hand
(`ant/skills/learn/`). The data is there — `episodes/`, `.diary/`, the
message DB, per-user files — but nothing drives consolidation. Hermes,
muaddib, and Letta all have a continuous loop; arizuko has only the manual
trigger.

## The decision: proposals, never edits

Root `CLAUDE.md` says "platform stays mechanical, operator owns truth,"
which tilts against this whole feature. The reconciliation, and the only
reason this is not drift:

**Learning writes to `~/proposals/` inside the group workspace. Nothing
mutates `PERSONA.md`, `.claude/skills/*/SKILL.md`, `users/*`, or
`CLAUDE.md` without operator acceptance.** Acceptance moves the file to
its declared `target_path`; rejection deletes it. No third state.

Three modes per group (`learning.mode`): `off` (default), `proposals`
(everything gated), `auto-low-risk` (memory auto-accepts; skills and
persona still gated). There is **no `auto-all`**.

- **Persona is always gated**, in every mode, permanently. Register drift
  is the failure this feature could cause that an operator would not
  notice, and the signal (user corrections) is the weakest of the lot. A
  persona proposal is a _diff_ the operator applies by hand, never an
  edit.
- **Skills are always gated.** A skill carries token cost in every
  session; an unreviewed skill silently grows the system prompt.
- **Every accepted proposal snapshots the prior file first**
  (`~/.snapshots/`), so acceptance is reversible with one command
  (hermes does the same before its curator mutates the skill tree —
  best-effort, never blocking).

## Two triggers, one renderer

The renderer is the `~/proposals/` queue; both triggers write only there.

1. **Turn-counter nudge** — every N user turns since the last
   memory/skill write, prepend a `<nudge>` block inviting `/learn` or
   `/diary` review. **The agent decides whether to act** — the nudge is a
   hint, not a job.
2. **Inactivity-triggered curator pass** — after the group is idle ≥
   `min_idle_hours` AND ≥ `interval_hours` since the last pass, `timed`
   schedules a low-priority task that re-reads recent `episodes/` +
   `.diary/` + per-user files and emits proposals. **No new daemon** —
   this is an ordinary cron task reading `groups.last_message_at`.

Cadence knobs live in group config (`learning:` frontmatter) with hermes-
derived defaults: nudge every 10 turns, curator at 2 h idle / 168 h
interval, recurrence threshold 3, failure threshold 5.

Pattern kinds and their artifacts: recurring question → a skill stub;
recurring failure → a workaround proposal; recurring tool pairing → a
composite skill stub; stale/contradicted memory → a proposed merge;
persona drift → a diff. Each proposal carries an `evidence:` list of
source files — arizuko's substitute for hermes's per-record
Active/Stale/Archived lifecycle column. Corrections are detected by regex
plus retry tracking, **not LLM judgement at trigger time**.

**Pattern detection is SQL + regex, never an LLM call in the daemon.**
The LLM judgement happens inside the curator's review pass. Cheaper,
deterministic, debuggable — and a trigger that costs a model call per
message will be turned off.

Borrowed from muaddib: the curator prompt reuses its session-end `<meta>`
envelope wording ("DO NOT RESPOND ANYMORE") so a background pass cannot
leak a user-facing reply.

## Operator gate

`/dash/groups/<folder>/proposals` lists pending files with diff previews;
accept/reject triggers the snapshot + move. Reuses existing dashd
file-edit primitives — no new daemon. Per the resreg rule (root
`CLAUDE.md`), `proposals` is an operator-managed entity and must register
as a resreg resource with both faces, not a bespoke ipc tool.

## Out of scope

- **Pre-tool-call threat scanning** — [23-skill-guard.md](23-skill-guard.md).
  Different surface (`PreToolUse` on `Write`/`Edit`), different lifecycle
  (per-call, not per-pattern).
- **Auto-skill-acceptance** — never (see above).

## Implementation note

The pre-split draft named `gateway/learning.go` and `gateway/proposals.go`.
**`gateway/` no longer exists** (`gated` removed at v0.50.0); the nudge
counter and curator scheduler belong in `routd`, alongside prompt build
and the dispatch loop.

## Cross-project reference

| Project                 | Mechanism                                                                       | Source                                      |
| ----------------------- | ------------------------------------------------------------------------------- | ------------------------------------------- |
| hermes-agent            | Curator + turn nudges + lifecycle states                                        | `agent/curator.py`, `run_agent.py`          |
| muaddib                 | Session-end memory wrap-up via `<meta>` envelope + heartbeat file               | `src/rooms/command/command-executor.ts`     |
| openclaw-managed-agents | External-only `PATCH /v1/agents/:id` + `/archive`; no internal self-update path | `src/orchestrator/server.ts`                |
| Letta                   | `core_memory_append` / `core_memory_replace` agent-callable tools               | https://docs.letta.com/guides/agents/memory |
| agno                    | `enable_agentic_memory=True` — agent gets memory CRUD tools                     | https://docs.agno.com                       |
| AnythingLLM             | RAG-only; no explicit self-learning loop                                        | n/a                                         |

Letta and agno docs are thin: tool names are documented, trigger cadence
is not. Inferred, not verified.
