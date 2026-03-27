## 049 — get_history MCP tool

`get_history` is now an MCP tool. The `/recall-messages` skill can use it
to fetch older message history on demand.

Tool: `mcp__arizuko__get_history`

Parameters:
- `chat_jid` (required) — the chat JID to fetch history for
- `limit` — max messages to return (default 100, max 200)
- `before` — ISO 8601 timestamp cursor; returns messages before this time

Returns JSON with:
- `messages` — XML `<messages>` block (same format as injected history)
- `count` — number of messages returned
- `oldest` — ISO 8601 timestamp of the oldest returned message (use as
  `before` cursor to paginate backwards)

Access: root agents can query any JID. Non-root agents can only query JIDs
routed to their group folder.
