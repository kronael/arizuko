---
status: partial
---

# SSE streams

Groups are the auth boundary. Public group → SSE open. Private group
→ token required at proxyd before webd. Per-user isolation via
prototype-spawned groups. For web chat, topic scopes delivery within
a group (see [../6/3-chat-ui.md](../6/3-chat-ui.md)).

Rationale: per-sender scoping within a group fights the shared-context
model. "Can you access this group" is the only auth question.

Unblockers: prototype-spawned groups interact with cleanup + limits
(undecided). Streamable-HTTP MCP transport (v2025-03-26) per group is
plausible future direction — not planned.
