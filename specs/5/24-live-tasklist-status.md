---
status: partial
---

# Live tasklist status — harness-driven, one edited message per turn

Progress uses a harness hook on the structured task list, delivered as one
editable checklist that lives while the turn runs. Placement, capability and
removal belong to `../6/46-turn-progress.md`.

The message lifecycle exists. The shared native checklist producer remains
unbuilt, as recorded in `../6/46-turn-progress.md` §"Build state".

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

`ant` owns one progress producer for native checklist changes and planless
`<status>` fallback. The producer, rendering, reminder, and native-tool contracts
are defined in `../6/46-turn-progress.md` §"The declaration" and §"The nudge".
`routd/turn_status.go` owns placement, edits, and deletion of the temporary message.

The renderer keeps these boundaries:

- **Interim rows carry `verb="status"`.** It confers a reply obligation and
  does not satisfy final delivery or last-answer tracking.
- **The live message id is the last persisted `verb="status"` row's
  `platform_id`, not an in-memory map.** Nothing to leak, nothing to
  clean up, and edit-in-place survives a routd restart.
- **A channel without `edit` and `delete` receives no progress message.** A status the
  agent sends there is dropped and logged at debug. A real edit failure
  (401/500/timeout) surfaces — never a silent duplicate.

The producer uses the existing hidden `submit_status` method.
It sends no checklist messages directly and adds no status protocol.
The module map in `../6/46-turn-progress.md` names the code boundaries.

## Tier

Hot-tier per-turn agent output: no REST twin, no resreg resource. It is
delivery plumbing on the existing `submit_status` MCP method, not a
managed entity.
