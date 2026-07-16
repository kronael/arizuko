---
status: shipped
---

# Live tasklist status — harness-driven, one edited message per turn

Progress reporting moves from an LLM-invoked skill (unreliable — models
skip skills) to a **harness hook on the structured tasklist**, delivered
as **one message edited in place** per turn instead of a stream of ⏳
pings.

## Problem

Two mechanisms report mid-turn progress today, both weak:

- **`<status>` blocks** — the agent must remember to emit
  `<status>…</status>`; `ant/CLAUDE.md` mandates it and a 90 s timer
  (`index.ts` `nudgeProgress`) pokes the agent to. Each one is delivered
  as a **new** `⏳ …` message (`routd.mcpSubmitStatus` →
  `deliverStatus`), so a long turn spams the channel.
- **the `progress` skill** — asks the agent to post a checklist once and
  `edit` it. Correct shape, but it is a skill: the model applies it
  unreliably, so users see either nothing or a wall of separate
  plan/step/done messages (rhias, 2026-07-16).

Progress is deterministic bookkeeping over the agent's own task list — it
should not depend on the model choosing to narrate it.

## Design

The agent already plans with the SDK **`TodoWrite`** tool (available
under `bypassPermissions` — no allowlist, `claude.ts` query options). Two
orthogonal changes turn that structured list into a live status message:

1. **ant — a `TodoWrite` PostToolUse hook** (`ant/src/todo-status.ts`,
   wired in `claude.ts` `events()` next to the existing tool-log +
   IPC-drain hooks). On each `TodoWrite`, render the whole list
   (`☑` completed · `⏳` in-progress · `☐` pending) and call
   `submitStatus({turn_id, text})`. The turn id threads in via
   `SessionConfig` (new `turnID` field, set in `index.ts.buildSessionConfig`).
   No new MCP tool, no new agent-facing surface — the hook reads the SDK
   tool input the agent already writes.

2. **routd — `submit_status` edits one message per turn** instead of
   posting a new one each call. Interim rows carry `verb="status"` and are
   **excluded from `TurnHasBotReply`** — a status must never count as the
   agent's reply, or `recordTurnResult` skips delivering the real answer
   (the reply-swallow regression codex caught pre-deploy; `db.go`). The
   live message id is the **last persisted `verb="status"` row's
   `platform_id`** (`DB.LastStatusPlatformID`) — no in-memory map, nothing
   to leak or clean up. First status sends the `⏳ …` message; subsequent
   statuses `deliver.Edit`. Only `ErrUnsupported` (email/WhatsApp/Reddit)
   falls back to a fresh send; a real edit failure (401/500/timeout)
   surfaces, never a silent duplicate. Improves the existing `<status>`
   path too — one live message, not a stream.

Both paths feed the same `submitStatus` sink → the same edited message.
`<status>` blocks still work for turns without a task list (the floor);
the hook covers multi-step turns automatically.

## Consequences

- The **`progress` skill collapses** to a one-line pointer: "plan with a
  task list; the harness shows it live" — no manual post/capture-id/edit
  dance. `ant/CLAUDE.md` § Status updates loses the manual-checklist
  paragraph.
- The 90 s `nudgeProgress` self-nudge stays as the floor for planless
  long turns; it is no longer the primary progress path.
- Edit-in-place survives a routd restart: the id lives in the persisted
  `verb="status"` row, not memory. A truly un-editable adapter still
  streams (the `ErrUnsupported` fallback).

## Code pointers

- `ant/src/todo-status.ts` — new: render + `TodoWrite` PostToolUse hook.
- `ant/src/backend/claude.ts` — register the hook (`matcher: 'TodoWrite'`);
  `SessionConfig.turnID` plumb.
- `ant/src/index.ts` — pass `turnID` into `buildSessionConfig`.
- `routd/mcp.go` `mcpSubmitStatus` — edit the last `verb="status"` row or
  send; `ErrUnsupported`-only fallback.
- `routd/turns.go` `deliverStatus` — stamp `verb="status"` on the row.
- `routd/db.go` `TurnHasBotReply` (excludes `verb="status"`),
  `LastStatusPlatformID` (the live id, from the persisted row).

## Tier

Hot-tier (per-turn agent output). No REST twin, no resreg resource — it
is delivery plumbing on the existing `submit_status` MCP method, not a
managed entity.
