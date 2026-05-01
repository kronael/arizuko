---
status: shipped
shipped: 2026-05-01
---

# Message history MCP

Agent-side tools to query message history:

- `get_history(chat_jid, limit, before)` — shipped in `ipc/ipc.go` + `webd/mcp.go`
- `get_thread(chat_jid, topic, limit, before)` — shipped in `ipc/ipc.go`
- `fetch_history(chat_jid, limit, before)` — shipped in `ipc/ipc.go`,
  platform-truth fallback

Rationale: agents need lookup outside their sliding window. Used by
`recall-messages` skill. `get_thread` narrows to one (chat_jid, topic)
slice — Telegram forum topics, web-chat topics — without scanning the
whole chat.
