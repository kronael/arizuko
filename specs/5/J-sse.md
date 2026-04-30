---
status: partial
---

# SSE streams

Groups are the auth boundary. Public group → SSE open. Private group
→ token required at proxyd before webd. Per-user isolation via
prototype-spawned groups.

The webd hub serves two subscription shapes against the same broker:

- **`folder/<topic>`** — chat-UI conversation stream. Used by the
  browser widget at `/slink/<token>` and authed peers at
  `/slink/stream`. Topic scopes delivery within a group (see
  [../6/3-chat-ui.md](../6/3-chat-ui.md)).
- **`turn/<turn_id>`** — round-handle stream. Used by
  `/slink/<token>/turn/<id>/sse` (see [../1/W-slink.md](../1/W-slink.md)).
  Carries assistant frames from a single run plus a terminal
  `round_done` event when `submit_turn` lands. Stream closes after
  `round_done`.

Rationale: per-sender scoping within a group fights the shared-context
model. "Can you access this group" is the only auth question.

Unblockers: prototype-spawned groups interact with cleanup + limits
(undecided). Streamable-HTTP MCP transport (v2025-03-26) per group is
plausible future direction — not planned.
