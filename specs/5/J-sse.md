---
status: shipped
shipped: 2026-05-01
---

# SSE streams + slink MCP

Groups are the auth boundary. Public group → SSE open. Private group
→ token required at proxyd before webd. Slink tokens are the
public-facing primitive: one token = one group, no JWT.

Per-sender scoping within a group fights the shared-context model.
"Can you access this group" is the only auth question.

## Topic shapes

`webd/hub.go` keys subscriptions on `folder/topic`:

- `GET /slink/stream?token=<t>&group=<folder>&topic=<t>` — SSE stream
  of every `message` event published on (folder, topic). Auth via
  proxyd-signed slink token + matching `X-Folder`, or proxyd-signed
  user identity with folder ACL.
- `POST /slink/<token>` with `Accept: text/event-stream` — same SSE
  stream, opened inline after the user's own bubble is published.
  `?wait=<sec>` (with `Accept: application/json`) blocks for the
  first assistant reply on the same (folder, topic), capped at 120s.

The hub buffers ≤16 frames per subscriber and drops slow clients
silently; capacity caps (`maxHubKeys`, `maxSubsPerKey`) bound memory
under flood.

## MCP transport

`POST /slink/<token>/mcp` exposes a streamable-HTTP MCP endpoint
scoped to the token's group. The token IS the auth — no JWT, no
bearer; possessing it = group membership. Three tools:

- `send_message(content, topic?)` → `{turn_id, topic, folder}` —
  drops a fresh user message into the group; topic is the round id.
- `steer(turn_id, content)` — extends an existing round on the same
  topic.
- `get_round(turn_id, wait?)` → `{frames, done}` — reads assistant +
  user frames for a round. With `wait: true`, blocks until at least
  one assistant frame arrives or the server cap (~5 min) hits;
  `done` is true when an assistant frame was observed.

`get_round` is the MCP-side dual of the SSE stream: clients that
prefer polling-with-blocking over EventSource use it. Both surface
the same hub events.

External agents register the URL as a remote MCP server in their
Claude Code (or any MCP client) `mcpServers` config; the agent then
talks to the group as if the three tools were local.
