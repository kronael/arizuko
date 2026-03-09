# Channel Adapter Protocol

**Status**: design

Channel adapters are HTTP clients that connect to the gateway
(router). Each adapter handles one platform. Both sides are
HTTP servers — gateway receives inbound messages, channel
receives send requests.

## Why REST, not WebSocket

Delivery is synchronous. Gateway calls channel to send,
channel sends to platform, returns 200. Done. No connection
state, no reconnect logic, no heartbeat frames. If the call
fails, the message wasn't delivered — gateway knows immediately.

Channel polls nothing. Gateway polls nothing. Each call is
a complete transaction.

## Registration

Channel starts, registers with gateway. Tells it which JID
prefixes it handles and what it can do.

```
POST /v1/channels/register

{
  "name": "telegram",
  "url": "http://localhost:9001",
  "jid_prefixes": ["tg:"],
  "capabilities": {
    "send_text": true,
    "send_file": true,
    "typing": true,
    "threading": false,
    "native_commands": false
  }
}

→ 200 {"ok": true, "token": "<session-token>"}
```

Gateway stores the registration. Routes outbound messages
to the channel whose `jid_prefixes` match.

Multiple channels can register for overlapping prefixes —
first registered wins (or error, TBD).

### Auth

Channel includes a shared secret from .env in the
`Authorization: Bearer <token>` header on registration.
Gateway returns a session token for subsequent calls.
Channel uses session token; gateway uses the same shared
secret when calling channel endpoints.

### Deregistration

Explicit:

```
POST /v1/channels/deregister
Authorization: Bearer <session-token>

→ 200 {"ok": true}
```

Implicit: gateway pings channel periodically. If ping fails
N times (default 3), gateway deregisters it. JIDs for that
channel become unroutable — messages queue in DB until the
channel re-registers and the gateway replays them.

## Gateway endpoints (channel → gateway)

### Register

```
POST /v1/channels/register
```

See above.

### Deliver inbound message

```
POST /v1/messages
Authorization: Bearer <session-token>

{
  "id": "msg-uuid",
  "chat_jid": "tg:-1001234567",
  "sender": "tg:12345",
  "sender_name": "Alice",
  "content": "hello",
  "timestamp": 1709942400,
  "is_group": true,
  "reply_to": "msg-prev-uuid",
  "attachments": [
    {
      "mime": "image/jpeg",
      "filename": "photo.jpg",
      "url": "http://localhost:9001/files/abc123",
      "size": 84210
    }
  ]
}

→ 200 {"ok": true}
```

Gateway stores the message, routes to the appropriate group.

Attachments: channel serves files on its own HTTP server.
Gateway downloads them if needed (for agent context). This
keeps large blobs out of the protocol — just a URL.

### Chat metadata

```
POST /v1/chats
Authorization: Bearer <session-token>

{
  "chat_jid": "tg:-1001234567",
  "name": "Dev Chat",
  "is_group": true,
  "channel": "telegram"
}

→ 200 {"ok": true}
```

Upserts chat info. Sent on first message from a chat, or
when metadata changes (name, participants).

## Channel endpoints (gateway → channel)

### Send message

```
POST /send
Authorization: Bearer <secret>

{
  "chat_jid": "tg:-1001234567",
  "content": "reply text",
  "reply_to": "msg-uuid",
  "format": "markdown"
}

→ 200 {"ok": true, "message_id": "tg-msg-456"}
```

Synchronous delivery. When this returns 200, the message
is on the platform. Gateway can mark it delivered.

If channel returns 4xx/5xx, delivery failed. Gateway
decides retry policy.

### Send file

```
POST /send-file
Authorization: Bearer <secret>

Content-Type: multipart/form-data
- chat_jid: "tg:-1001234567"
- filename: "report.pdf"
- file: <binary>

→ 200 {"ok": true, "message_id": "tg-msg-457"}
```

### Set typing

```
POST /typing
Authorization: Bearer <secret>

{
  "chat_jid": "tg:-1001234567",
  "on": true
}

→ 200 {"ok": true}
```

Fire-and-forget from gateway's perspective. Failure is
not an error — typing indicators are cosmetic.

### Ping

```
GET /health

→ 200 {"status": "ok", "name": "telegram", "jid_prefixes": ["tg:"]}
```

No auth needed. Gateway calls this every 30s. Three
consecutive failures → deregister.

## Lifecycle

```
1. Channel starts, connects to platform (telegram API, etc)
2. Channel POSTs /v1/channels/register to gateway
3. Gateway stores registration, starts health pings
4. Inbound: platform event → channel POSTs /v1/messages
5. Outbound: gateway POSTs /send to channel → channel sends
6. Channel shuts down → POSTs /v1/channels/deregister
7. Channel crashes → health pings fail → auto-deregister
```

Messages that arrive while a channel is deregistered queue
in the gateway's outbox. When the channel re-registers,
gateway replays queued messages.

## Transport

Channel `url` in registration determines transport:

- `http://localhost:9001` — same host, different process
- `http://10.0.0.5:9001` — different host
- `vsock://3:9001` — VM guest over vsock (CID 3, port 9001)

Gateway treats it as opaque HTTP. For vsock, a thin
HTTP-over-vsock proxy on the host translates. Channel
doesn't know or care about the transport — it's just
an HTTP server.

## Capabilities

Capabilities declared at registration. Gateway uses them
to decide what to attempt:

| Capability        | If false                       |
| ----------------- | ------------------------------ |
| `send_text`       | channel is receive-only        |
| `send_file`       | gateway skips /send-file calls |
| `typing`          | gateway skips /typing calls    |
| `threading`       | no reply_to on outbound        |
| `native_commands` | gateway handles /commands      |
| `reactions`       | no reaction forwarding         |
| `edit`            | no message edit forwarding     |
| `delete`          | no message delete forwarding   |

Extensible — new capabilities added without protocol change.
Unknown capabilities ignored by gateway.

## Why this is simpler than WebSocket

- No connection state management
- No reconnect/backoff logic
- No ping/pong framing
- No message ordering concerns
- Standard HTTP middleware (auth, logging, rate limiting)
- Every call is independently retriable
- Load balancers, proxies, TLS termination all work
- Any HTTP client in any language

The only thing WebSocket gives is push without polling.
But we don't poll — gateway pushes to channel via POST,
channel pushes to gateway via POST. Both sides are servers.

## Relation to existing code

Current Go implementation has channels as in-process
interfaces (`core.Channel`). Migration path:

1. Keep in-process channels working (backward compat)
2. Add HTTP adapter that implements `core.Channel`
3. HTTP adapter forwards to registered external channel
4. External channels implement this protocol
5. Eventually remove in-process channel code

The gateway doesn't change its internal routing — it still
calls `channel.Send()`. The HTTP adapter is just a new
implementation of that interface that does a POST instead
of a direct platform API call.

## Open questions

### Attachment handling

Large files: should channel upload to gateway, or should
gateway fetch from channel URL? Current design: channel
provides a URL, gateway fetches if needed. Keeps the
protocol simple but requires channel to serve files.

### Batching

Send multiple messages in one call? Probably not needed —
chat messages are low volume. Keep it simple: one message
per request.

### Event types beyond messages

Reactions, edits, deletes, joins, leaves — each a POST to
a gateway endpoint? Or one generic `/v1/events` endpoint
with a type field? Generic is more extensible but less
self-documenting. Keeping specific endpoints for now.

### Multiple instances of same channel

Two telegram bots? Each registers with different JID
prefixes. Gateway routes by prefix match. No conflict.
