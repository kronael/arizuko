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

**Subscriptions key on the chat JID VERBATIM + topic, not on a
connection or a user** (`webd/hub.go`). A round is `(chat JID, topic)`,
so any number of viewers of the same conversation get the same frames
and a reconnect re-attaches to the live round instead of starting a new
one. No end may derive the key: publisher (routd, via the `chat_jid`
field on `/send` and `/v1/round_done`) and subscriber (webd, from the
token row's `JID` / the turn context) both pass the JID they already
hold. The key used to be a folder each end re-derived with string
surgery (`TrimPrefix`, `"web:"+folder`, the client's `group=` param);
the two ends disagreed for every suffixed and `hook:` chat and the
widget hung three separate times (BUGS L30, L4 class).

**`round_done` carries the CHAT JID, not the routing target.** When a
rule maps `web:X/submissions → groupY` those differ, and keying on the
target means the browser never learns the round closed and the form UI
hangs (`routd/turns.go` recordTurnResult).

**Slow clients are dropped, not backpressured.** Each subscriber gets a
16-frame buffer; a client that can't keep up loses frames rather than
stalling the publisher. `maxHubKeys` (10000) and `maxSubsPerKey` (256)
bound memory under flood — a public URL is a flood surface.

**Blocking-poll is a first-class dual of the stream, not a fallback.**
`POST /chat/<token>?wait=<sec>` and the `/status` endpoint read the same
hub events an EventSource would. Anything behind a proxy that mangles SSE
uses it without a second delivery path existing.

## Surface

`webd/turn.go`, mounted at `webd/server.go:101-112`:

- `GET /chat/<token>/<turn_id>` — turn snapshot; `?after=<msg_id>`
  cursor-pages forward.
- `GET /chat/<token>/<turn_id>/status` — status + counts, no frames.
- `GET /chat/<token>/<turn_id>/sse` — frame stream for one turn, closes
  on `round_done`.
- `GET /chat/stream` — the `(token, topic)` stream; the token row
  decides the chat JID and folder, never a client param.
- `POST /chat/<token>/mcp` — the one web MCP surface (`list_groups` /
  `send` / `get_history`, `webd/mcp.go`), pinned to the token's group.
  It has no blocking round read; a client that needs one uses the
  `?wait=` POST or the SSE stream above (BUGS L139).

Frames and turn results are read from `routd.db` — webd holds no
conversation state of its own.
