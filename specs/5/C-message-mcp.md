---
status: partial
---

> Gap: `get_thread(jid)` is not registered in `ipc/ipc.go`. Only
> `get_history` and adapter-backed `fetch_history` exist today.

# Message history MCP

Agent-side tools to query message history:

- `get_history(chat_jid, limit, before)` — shipped in `ipc/ipc.go` + `webd/mcp.go`
- `get_thread(jid)` — unshipped

Rationale: agents need lookup outside their sliding window. Used by
`recall-messages` skill.

Unblockers: add `get_thread` MCP tool in `ipc/`, scope by calling
group's folder.
