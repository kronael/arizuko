---
status: shipped
---

# Live tasklist status — harness-driven, one edited message per turn

Progress uses a harness hook on the structured task list, delivered as one
editable checklist that lives while the turn runs. Placement, capability and
removal belong to `../6/46-turn-progress.md`.

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

The agent reports progress in `<status>` blocks and plans with the SDK's
`TodoWrite` tool.

1. **ant — a `TodoWrite` PostToolUse hook** (`ant/src/todo-status.ts`). On
   each `TodoWrite`, render the whole list (`☑`/`⏳`/`☐`) and call
   `submitStatus({turn_id, text})`. The hook reads the list already written by
   the agent.
2. **ant — streamed `<status>` blocks.** Each new block goes through
   `submitStatus({turn_id, text})`.
3. **routd — one renderer.** `routd/progress.go` creates and edits one `⏳`
   checklist where the reply will go, and deletes it when the turn ends.

The renderer keeps these boundaries:

- **Interim rows carry `verb="status"`.** It confers a reply obligation and
  does not satisfy final delivery or last-answer tracking.
- **The live message id is the last persisted `verb="status"` row's
  `platform_id`, not an in-memory map.** Nothing to leak, nothing to
  clean up, and edit-in-place survives a routd restart.
- **A channel without `edit` and `delete` receives no progress message.** A status the
  agent sends there is dropped and logged at debug. A real edit failure
  (401/500/timeout) surfaces — never a silent duplicate.

Both inputs use the existing hidden `submit_status` method. Neither source
sends checklist messages directly or creates an extra status protocol.

## Code pointers

- `ant/src/todo-status.ts` — render + the `TodoWrite` PostToolUse hook.
- `ant/src/claude.ts` — `TodoWrite` hook registration.
- `ant/src/index.ts` — streamed `<status>` forwarding.
- `routd/progress.go` — checklist rendering.
- `routd/placement.go` — common placement for progress and answers.
- `routd/db.go` — `TurnHasBotReply` excludes progress-only rows;
  `LastStatusRow` finds the persisted checklist.

## Tier

Hot-tier per-turn agent output: no REST twin, no resreg resource. It is
delivery plumbing on the existing `submit_status` MCP method, not a
managed entity.
