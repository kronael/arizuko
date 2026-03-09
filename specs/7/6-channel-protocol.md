# Channel Adapter Protocol

**Status**: design

Channel adapters connect to platforms and talk to the
router over HTTP. Both sides are HTTP servers. Channel
self-registers with router so router knows where to
find it.

## Why self-registration

Router doesn't manage channel lifecycle. Channels are
external processes — started by systemd, docker, manual,
whatever. On startup, channel registers with router:
"I handle these JID prefixes, call me at this URL."

This makes channels modular. Anyone can write one in any
language. Router doesn't need static config for channels —
they announce themselves.

## Why REST, not WebSocket

Both directions are synchronous HTTP calls:

- Channel → router: deliver inbound message
- Router → channel: send outbound message

Each call is a complete transaction. No connection state,
no reconnect logic, no message ordering concerns. When
router's POST to /send returns 200, the message is on
the platform. Done.

## Protocol

### Router endpoints (channel → router)

#### Register

Channel starts, tells router what it handles.

```
POST /v1/channels/register
Authorization: Bearer <shared-secret>

{
  "name": "telegram",
  "url": "http://telegram:9001",
  "jid_prefixes": ["tg:"],
  "capabilities": {
    "send_text": true,
    "send_file": true,
    "typing": true,
    "threading": false,
    "reactions": false,
    "edit": false,
    "delete": false
  }
}

→ 200 {"ok": true, "token": "<session-token>"}
```

`url` is where router calls the channel. Session token
for subsequent channel→router calls.

#### Deliver inbound message

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
      "url": "http://telegram:9001/files/abc123",
      "size": 84210
    }
  ]
}

→ 200 {"ok": true}
```

Attachments: channel serves files on its own HTTP server.
Router fetches if needed for agent context.

#### Chat metadata

```
POST /v1/chats
Authorization: Bearer <session-token>

{
  "chat_jid": "tg:-1001234567",
  "name": "Dev Chat",
  "is_group": true
}

→ 200 {"ok": true}
```

#### Deregister

```
POST /v1/channels/deregister
Authorization: Bearer <session-token>

→ 200 {"ok": true}
```

### Channel endpoints (router → channel)

Router calls these on the URL registered by the channel.

#### Send message

```
POST /send
Authorization: Bearer <shared-secret>

{
  "chat_jid": "tg:-1001234567",
  "content": "reply text",
  "reply_to": "msg-uuid",
  "format": "markdown"
}

→ 200 {"ok": true, "message_id": "tg-msg-456"}
```

Synchronous delivery. 200 = on the platform.

#### Send file

```
POST /send-file
Authorization: Bearer <shared-secret>

Content-Type: multipart/form-data
- chat_jid: "tg:-1001234567"
- filename: "report.pdf"
- file: <binary>

→ 200 {"ok": true, "message_id": "tg-msg-457"}
```

#### Typing

```
POST /typing
Authorization: Bearer <shared-secret>

{"chat_jid": "tg:-1001234567", "on": true}

→ 200 {"ok": true}
```

Fire-and-forget. Failure is not an error.

#### Health

```
GET /health

→ 200 {"status": "ok", "name": "telegram", "jid_prefixes": ["tg:"]}
```

Router calls every 30s. Three consecutive failures →
auto-deregister. Outbound queues until channel re-registers.

## Capabilities

Declared at registration. Router skips calls the channel
can't handle:

| Capability  | If false                |
| ----------- | ----------------------- |
| `send_text` | channel is receive-only |
| `send_file` | skip /send-file calls   |
| `typing`    | skip /typing calls      |
| `threading` | no reply_to on outbound |
| `reactions` | no reaction forwarding  |
| `edit`      | no edit forwarding      |
| `delete`    | no delete forwarding    |

Extensible. Unknown capabilities ignored.

## Auth

Shared secret from .env (`CHANNEL_SECRET`). Used for:

- Channel → router: registration (gets session token back)
- Router → channel: all calls use the same shared secret

Simple. Both sides trust each other via the shared secret.

## Lifecycle

```
1. Channel starts, connects to platform (telegram API, etc)
2. Channel POSTs /v1/channels/register to router
3. Router stores registration, starts health checks
4. Inbound: platform event → channel POSTs /v1/messages
5. Outbound: router POSTs /send to channel → platform
6. Channel shuts down → POSTs /v1/channels/deregister
7. Channel crashes → health fails → auto-deregister
```

Queued outbound: if channel is down, router queues
messages internally. When channel re-registers, router
replays them.

## Transport

Channel's registered `url` determines transport:

| URL                                      | Transport      |
| ---------------------------------------- | -------------- |
| `http://telegram:9001`                   | Docker network |
| `http://localhost:9001`                  | TCP local      |
| `http://10.0.0.5:9001`                   | TCP remote     |
| `http+unix:///run/arizuko/telegram.sock` | Unix socket    |
| vsock CID:port                           | vsock          |

**Future**: HTTP over unix socket and vsock are natively
supported in Go (`net/http` accepts any `net.Listener`).
The protocol is pure HTTP regardless of transport — no
changes needed, just a different dialer. Not building
toward this now, but the design is compatible.

## Why this design

- **Testable**: mock router with any HTTP server, test
  channel in isolation. Mock channel with any HTTP server,
  test router in isolation.
- **Agent-writable**: clear boundary, clear contract. An AI
  agent can write a channel adapter given this spec. No
  context about router internals needed.
- **Language-free**: any language with an HTTP client/server
  library works. That's all of them.
- **Modular**: add a new platform by writing one adapter.
  Remove by deregistering. No router changes.
- **Transport-agnostic**: HTTP works over localhost, network,
  vsock. The protocol doesn't care.

## Migration from in-process channels

Current Go code has channels as in-process interfaces
(`core.Channel`). Migration:

1. Router exposes HTTP API alongside existing channels
2. HTTP channel adapter implements `core.Channel` — proxies
   between HTTP protocol and internal interface
3. External channels register and work via HTTP
4. Retire in-process channels one by one

## Open questions

### Large file delivery

Outbound: router sends file via multipart POST. Simple.
Inbound: channel provides URL, router fetches. What if
channel is behind NAT? Options:

- Channel uploads to router (POST /v1/files)
- Base64 inline (doubles size, fine for <25MB)
- Presigned upload URL from router

### Event types beyond messages

Reactions, edits, deletes, joins, leaves — each gets its
own router endpoint? Or one generic `/v1/events` with a
type field? Specific endpoints for now.

### Multiple instances of same channel

Two telegram bots: each registers with different JID
prefixes. Router routes by prefix. No conflict.
