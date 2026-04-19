---
status: unshipped
---

# Message history MCP

Agent-side tools to query message history:

- `get_history(jid?, limit?, since?, until?)` → `<messages>` XML
- `get_thread(jid)` → all messages in a channel thread

Rationale: agents currently have no way to look up messages outside
their sliding window. Needed by `recall-messages` skill (see
[b-memory-skills-standalone.md](b-memory-skills-standalone.md) OQ-1).

Unblockers: add MCP tool in `ipc/`, scope by calling group's folder.
