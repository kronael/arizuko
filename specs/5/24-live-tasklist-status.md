---
status: shipped
---

# Live tasklist status — harness-driven, one edited message per turn

Progress reporting moves from an LLM-invoked skill to a **harness hook on
the structured tasklist**, delivered as **one message edited in place** per
turn instead of a stream of ⏳ pings.

## Problem

Both prior mechanisms were weak for the same reason — they depended on the
model choosing to narrate. `<status>` blocks require the agent to remember
to emit them (a 90 s timer poked it), and each one was delivered as a
**new** message, so a long turn spammed the channel. The `progress` skill
had the right shape (post once, edit it) but was a skill: models apply
skills unreliably, so users saw either nothing or a wall of separate
plan/step/done messages (rhias, 2026-07-16).

Progress is deterministic bookkeeping over the agent's own task list. It
should not be something the model can forget.

## Design

The agent already plans with the SDK **`TodoWrite`** tool. Two orthogonal
changes turn that structured list into a live status message:

1. **ant — a `TodoWrite` PostToolUse hook** (`ant/src/todo-status.ts`). On
   each `TodoWrite`, render the whole list (`☑`/`⏳`/`☐`) and call
   `submitStatus({turn_id, text})`. **No new MCP tool, no new
   agent-facing surface** — the hook reads the tool input the agent
   already writes, so there is nothing for the model to skip.
2. **routd — `submit_status` edits one message per turn.** First status
   sends `⏳ …`; subsequent ones `deliver.Edit`.

Two decisions inside (2) carry the weight:

- **Interim rows carry `verb="status"` and are excluded from
  `TurnHasBotReply`.** A status must never count as the agent's reply, or
  `recordTurnResult` skips delivering the real answer — the reply-swallow
  regression codex caught pre-deploy.
- **The live message id is the last persisted `verb="status"` row's
  `platform_id`, not an in-memory map.** Nothing to leak, nothing to
  clean up, and edit-in-place survives a routd restart.
- **Only `ErrUnsupported` (email/WhatsApp/Reddit) falls back to a fresh
  send.** A real edit failure (401/500/timeout) surfaces — never a silent
  duplicate.

Both paths feed the same `submitStatus` sink, so `<status>` blocks get the
one-live-message behavior too. They remain the floor for turns without a
task list; the 90 s `nudgeProgress` self-nudge stays for planless long
turns but is no longer the primary path.

Consequence: the `progress` skill collapses to a one-line pointer ("plan
with a task list; the harness shows it live") and `ant/CLAUDE.md` loses
its manual-checklist paragraph.

## Code pointers

- `ant/src/todo-status.ts` — render + the `TodoWrite` PostToolUse hook.
- `ant/src/backend/claude.ts` — hook registration (`matcher: 'TodoWrite'`)
  - the `SessionConfig.turnID` plumb.
- `routd/mcp.go` `mcpSubmitStatus` — edit-or-send.
- `routd/turns.go` `deliverStatus` — stamps `verb="status"`.
- `routd/db.go` — `TurnHasBotReply` (excludes status),
  `LastStatusPlatformID`.

## Tier

Hot-tier per-turn agent output: no REST twin, no resreg resource. It is
delivery plumbing on the existing `submit_status` MCP method, not a
managed entity.
