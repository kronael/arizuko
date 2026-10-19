---
status: shipped
---

# Live tasklist status — harness-driven, one edited message per turn

Progress uses a harness hook on the structured task list, delivered as one
editable checklist beside the declared task summary. Placement belongs to
`../6/46-turn-progress.md`.

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

The agent declares the overall task in its first one-line `<status>` before
planning with the SDK's `TodoWrite` tool.

1. **ant — a `TodoWrite` PostToolUse hook** (`ant/src/todo-status.ts`). On
   each `TodoWrite`, render the whole list (`☑`/`⏳`/`☐`) and call
   `submitStatus({turn_id, text, kind: "progress"})`. The hook reads the
   list already written by the agent.
2. **ant — streamed `<status>` declarations.** The first successful model
   status uses `kind: "summary"`; later statuses use `kind: "progress"`.
   A first task list never substitutes for the summary.
3. **routd — one renderer.** `routd/progress.go` persists an immutable `🤖`
   summary and edits one `⏳` checklist. Compatible channels place progress
   and answers in the opener thread. Existing threads take precedence;
   unsupported new threads keep one editable progress card.

The renderer keeps these boundaries:

- **Interim rows carry `verb="status"` or `verb="status_summary"`.** Both
  confer a reply obligation. Neither satisfies final delivery or last-answer
  tracking.
- **The live message id is the last persisted `verb="status"` row's
  `platform_id`, not an in-memory map.** Nothing to leak, nothing to
  clean up, and edit-in-place survives a routd restart.
- **A channel without `edit` receives no progress message.** A status the
  agent sends there is dropped and logged at debug. A real edit failure
  (401/500/timeout) surfaces — never a silent duplicate.

Both inputs use the existing hidden `submit_status` method. Omitted `kind`
keeps the legacy progress behavior. Neither source sends checklist messages
directly or creates an extra status protocol.

## Code pointers

- `ant/src/todo-status.ts` — render + the `TodoWrite` PostToolUse hook.
- `ant/src/claude.ts` — `TodoWrite` hook registration.
- `ant/src/index.ts` — streamed status declaration kinds.
- `routd/progress.go` — summary declaration and checklist rendering.
- `routd/placement.go` — common placement for progress and answers.
- `routd/db.go` — `TurnHasBotReply` excludes progress-only rows;
  `LastStatusRow` finds the persisted checklist.

## Tier

Hot-tier per-turn agent output: no REST twin, no resreg resource. It is
delivery plumbing on the existing `submit_status` MCP method, not a
managed entity.
