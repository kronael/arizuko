---
status: shipped
---

# Slink

Web channel for a group. Public token = POST endpoint. Token lives in
`groups.slink_token`; proxyd resolves it on `/slink/*` requests.

Slink is the universal "drop a message into this group, observe the
agent's response" surface. Used by the browser chat UI, by curl-style
sync clients, by the operator CLI, and by anything else that needs to
inject a message and watch the round.

## Token design

- 32 random bytes, base64url-encoded (~43 chars, 256 bits)
- Public, freely shared in page source
- Generated once at registration, never rotated
- Security via rate limiting, not token secrecy

## Rate limiting

- Anon: `SLINK_ANON_DOS_RPM` per-IP request rate (default 10/min) — DoS shield, not metering.
- Auth: no edge rate limit; governed by per-folder + per-user cost caps (spec 5/34).
- Agents / operators: bypass /slink/\* entirely via direct daemon access.

## Sender identity derivation

- With valid JWT: `sender = jwt.sub`, `sender_name = jwt.name`
- Without JWT: `sender = anon:<ip-hash>`, `sender_name` omitted
- Malformed JWT returns 401; omitting Authorization header is allowed

## Round-handle protocol

A "round" is one container spawn — exactly one inbound batch in,
exactly one `submit_turn` out. The slink protocol exposes the round
as a first-class object with a stable handle so callers can wait on
it, page through its assistant frames, or stream them over SSE.

`turn_id` = the originating inbound `messages.id`. This is the round
handle.

### Endpoints

| Method | Path                                      | Purpose                                 |
| ------ | ----------------------------------------- | --------------------------------------- |
| GET    | `/slink/<token>`                          | Browser chat UI (HTML page)             |
| POST   | `/slink/<token>`                          | Inject a message; returns turn_id       |
| GET    | `/slink/<token>/<turn_id>`                | Snapshot: status + all assistant frames |
| GET    | `/slink/<token>/<turn_id>?after=<msg_id>` | Cursor page: frames after `<msg_id>`    |
| GET    | `/slink/<token>/<turn_id>/status`         | Cheap status check (no frame payload)   |
| GET    | `/slink/<token>/<turn_id>/sse`            | Live SSE stream + terminal `round_done` |

The second URL segment after the token (when present) is always a
turn_id used to observe a round. There is no POST endpoint at that
path — to continue a conversation, POST a fresh message to
`/slink/<token>` with the same `topic`.

### POST shape

Request body: `content=<text>&topic=<optional>` (form) or
`{"content":"...","topic":"..."}` (JSON).

Response (default — JSON, fire-and-handle):

```json
{
  "user": { "id": "msg_abc", "content": "...", "created_at": "..." },
  "turn_id": "msg_abc",
  "status": "pending"
}
```

`turn_id == user.id` for the first message of a round (turn_id is the
inbound message id by definition). The caller doesn't need to mint
ids; the response carries everything the next call needs.

`Accept: text/html` keeps the legacy HTMX-fragment behavior used by
the browser chat UI.

`Accept: text/event-stream` upgrades to SSE on `(folder, topic)` and
holds open. This is the legacy chat-UI streaming mode and is preserved
for backward compatibility. New clients should prefer `/sse` on the
turn handle (next section).

### GET turn snapshot / cursor

```
GET /slink/<token>/<id>
GET /slink/<token>/<id>?after=<msg_id>
```

Returns:

```json
{
  "turn_id": "msg_abc",
  "status": "pending|done|failed",
  "frames": [
    {
      "id": "out_001",
      "content": "...",
      "created_at": "...",
      "kind": "message"
    },
    { "id": "out_002", "content": "...", "created_at": "...", "kind": "status" }
  ],
  "last_frame_id": "out_002"
}
```

`frames[]` is ordered by `created_at` ascending. Each frame has a
stable `id` (`messages.id`). Use that id as `?after=<id>` on the next
call to fetch only what's new — same convention as `get_history`'s
`before` param.

`kind`:

- `message` — the agent's reply to the user
- `status` — interim `<status>` block emitted via the agent's
  status protocol (rendered as the `⏳` prefix on the platform side)

When `status="done"` the caller can stop polling. `status="failed"`
carries an `error` field with a short message.

### GET status (cheap)

```
GET /slink/<token>/<id>/status
```

Returns a tiny envelope so a client can decide between "poll once
and stop" vs "open SSE":

```json
{
  "turn_id": "msg_abc",
  "status": "pending",
  "frames_count": 2,
  "last_frame_id": "out_002"
}
```

### GET SSE stream

```
GET /slink/<token>/<id>/sse
```

SSE stream for the round. Events:

- `event: message` / `event: status` — same payload as a frame in the
  GET snapshot, one per `<status>` block or final reply emitted by
  the agent during the round.
- `event: round_done` — fired exactly once, after `submit_turn`.
  Payload `{turn_id, status, error?}`. The server then closes the
  stream.

`Last-Event-Id` reconnect is supported; on reconnect the server
replays frames whose id is greater than the last seen id (same query
as `?after=<id>`) before resuming live.

### Follow-up messages

To continue a conversation, the caller submits another `POST
/slink/<token>` with the same `topic` value. Topic continuity is
the only mechanism: there is no dedicated steer endpoint, no
`chained_from` field, no round-priority queue. When the gateway
sees an inbound message while a container is still running for that
group, it writes the message to the container's IPC inbox and the
agent picks it up mid-loop (`queue.SendMessages`, automatic). When
no container is running, a fresh round spawns. Either way the
caller observes the new round via the same `turn_id` snapshot/SSE
endpoints.

## Browser chat UI (legacy path)

The chat widget at `GET /slink/<token>` uses the legacy
`POST /slink/<token>` (HTML fragment) + `GET /slink/stream` SSE
combo, keyed on `(folder, topic)` not on turn. This continues to
work and is unchanged. New consumers should use the round-handle
endpoints; the chat widget will migrate when there's a UX reason.

## Hub keying

The webd SSE hub stays keyed by `(folder, topic)`. The round-handle
SSE handler subscribes to that same key and filters frames whose
payload `turn_id` matches the requested round. The gateway notifies
webd via `POST /v1/round_done` when `submit_turn` lands; webd
publishes a `round_done` event on `(folder, topic)` so any open
round-handle SSE subscriber can disconnect cleanly.

## Schema dependencies

Per [../1/Y-system-messages.md](Y-system-messages.md), messages and
turn_results are durable rows in `messages.db`. The round-handle
protocol relies on:

- `messages.turn_id` (added in migration 0038) — every outbound
  assistant message produced inside a run carries the inbound
  message id that triggered the run.
- `turn_results(folder, turn_id)` (migration 0036) — one row per
  completed round; consumed by the snapshot/status endpoints.

## Retention

Rounds are durable until the message is pruned. Today messages and
turn_results are kept indefinitely. When a retention policy is added
it covers both tables uniformly.

## MCP transport

`POST /slink/<token>/mcp` exposes a standards-compliant Streamable
HTTP MCP endpoint scoped to the token's group. Token is auth; same
DoS shield as the form-encoded slink path. Two tools:

- `send_message(content, topic?)` — submit a message. Reusing a
  `topic` continues that conversation thread.
- `get_round(turn_id, wait?)` — read frames for a round.

External agents register the URL in their MCP client config and
talk to the group as if the two tools were local. See
[../5/J-sse.md](../5/J-sse.md) for the relationship to the SSE
stream.
