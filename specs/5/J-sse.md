---
status: shipped
shipped: 2026-05-01
relates-to: [W-webhook-routes]
---

# SSE streams + the round surface

How webd streams an agent turn back to a web client. The token that
authorizes the stream, and the URL prefixes, are
[`W-webhook-routes.md`](W-webhook-routes.md) — this spec is only the
streaming half.

## Decisions

**The group is the auth boundary.** Possessing the route token = group
membership; there is no per-sender scoping inside a group, because that
fights the shared-context model every group is built on. "Can you access
this group" is the only auth question the public surface answers.

**Subscriptions key on `folder/topic`, not on a connection or a user**
(`webd/hub.go:17`). A round is `(folder, topic)`, so any number of
viewers of the same conversation get the same frames and a reconnect
re-attaches to the live round instead of starting a new one.

**`round_done` is published on the CHAT JID's folder, not the routing
target.** When a rule maps `web:X/submissions → groupY` those differ, and
keying on the target means the browser never learns the round closed and
the form UI hangs. Fixed in `routd/turns.go:707` —
`strings.TrimPrefix(tc.ChatJID, "web:")`. This was a live bug; the
subscriber side (`groupfolder.JidFolder`) has always keyed on the chat.

**Slow clients are dropped, not backpressured.** Each subscriber gets a
16-frame buffer; a client that can't keep up loses frames rather than
stalling the publisher. `maxHubKeys` (10000) and `maxSubsPerKey` (256)
bound memory under flood — a public URL is a flood surface.

**Blocking-poll is a first-class dual of the stream, not a fallback.**
`get_round(turn_id, wait=true)` and the `/status` endpoint read the same
hub events an EventSource would. MCP clients and anything behind a proxy
that mangles SSE use it without a second delivery path existing.

## Surface

`webd/turn.go`, mounted at `webd/server.go:101-112`:

- `GET /chat/<token>/<turn_id>` — turn snapshot; `?after=<msg_id>`
  cursor-pages forward.
- `GET /chat/<token>/<turn_id>/status` — status + counts, no frames.
- `GET /chat/<token>/<turn_id>/sse` — frame stream for one turn, closes
  on `round_done`.
- `GET /chat/stream` — the `(folder, topic)` stream.
- `POST /chat/<token>/mcp` — `send_message` / `get_round` /
  `get_round_status` (`webd/chat_mcp.go`).

Frames and turn results are read from `routd.db` — webd holds no
conversation state of its own.
