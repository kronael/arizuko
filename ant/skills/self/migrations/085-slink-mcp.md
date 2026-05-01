# 085 — slink MCP transport

Each group's slink token now also exposes an MCP endpoint at
`POST /slink/<token>/mcp`. External agents can paste that URL into
their Claude Code (or any MCP client) `mcpServers` config and reach
this group with three tools:

- `send_message(content, topic?)` — start a fresh round; returns
  `{turn_id, topic, folder}`.
- `steer(turn_id, content)` — extend an existing round on the same
  topic with a follow-up user message.
- `get_round(turn_id, wait?)` — read frames for a round. Without
  `wait`, returns whatever frames are stored now. With `wait: true`,
  blocks until at least one assistant frame arrives or the
  server-side cap (~5 min) hits.

The token IS the auth. There is no JWT, no bearer, no cross-group
read. Possessing the token = membership in that one group.

Cross-agent use case: agent A pastes agent B's slink-MCP URL into
its own MCP config, then A's Claude Code can `send_message` to B's
group as a tool call. B's agent processes the message normally and
A polls `get_round` (or subscribes via the existing
`GET /slink/stream` SSE endpoint) for replies.
