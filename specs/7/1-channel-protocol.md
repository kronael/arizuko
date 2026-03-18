# Channel Adapter Protocol

**Status**: design

Channel adapters connect to platforms and talk to the
router over HTTP. Both sides are HTTP servers. Channel
self-registers with router so router knows where to
find it.

## Why self-registration

Router doesn't manage channel lifecycle. Channels are
external processes — started by docker compose, manual,
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
  "jid_prefixes": ["telegram:"],
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
  "chat_jid": "telegram:-1001234567",
  "sender": "telegram:12345",
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
  "chat_jid": "telegram:-1001234567",
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
  "chat_jid": "telegram:-1001234567",
  "content": "reply text",
  "reply_to": "msg-uuid",
  "format": "markdown"
}

→ 200 {"ok": true, "message_id": "telegram-msg-456"}
```

Synchronous delivery. 200 = on the platform.

#### Send file

```
POST /send-file
Authorization: Bearer <shared-secret>

Content-Type: multipart/form-data
- chat_jid: "telegram:-1001234567"
- filename: "report.pdf"
- file: <binary>

→ 200 {"ok": true, "message_id": "telegram-msg-457"}
```

#### Typing

```
POST /typing
Authorization: Bearer <shared-secret>

{"chat_jid": "telegram:-1001234567", "on": true}

→ 200 {"ok": true}
```

Fire-and-forget. Failure is not an error.

#### Health

```
GET /health

→ 200 {"status": "ok", "name": "telegram", "jid_prefixes": ["telegram:"]}
```

Router calls every 30s. Three consecutive failures →
auto-deregister. Outbound queues until channel re-registers.

## Capabilities

Declared at registration. Router skips calls the channel
can't handle:

| Capability     | If false                       |
| -------------- | ------------------------------ |
| `send_text`    | channel is send-disabled       |
| `send_file`    | skip /send-file calls          |
| `typing`       | skip /typing calls             |
| `threading`    | no reply_to on outbound        |
| `reactions`    | no reaction forwarding         |
| `edit`         | no edit forwarding             |
| `delete`       | no delete forwarding           |
| `receive_only` | channel never delivers inbound |

Extensible. Unknown capabilities ignored.

## Internal service channels

Internal services (onbod, dashd) register in the same
channels table as external adapters using `receive_only: true` —
they receive routed messages but never deliver inbound messages.
No JID prefixes — router sends to them only when a route rule
targets their service name. See individual service specs for
their registration details.

## Route targets

A route target is either:

- **Folder path** (contains `/`) — write message to
  messages table; agent picks it up.
- **Service name** (no `/`) — look up URL in channels
  table, HTTP POST to `/send`. Same protocol as any
  channel.

gated holds no hardcoded knowledge of `/approve`,
`/reject`, etc. They're just routes. See `specs/7/9-gated.md`
for route resolution details.

## Agent channel

The agent is currently an implicit channel — gated
hardcodes `docker run` to spawn agent containers. The
conceptual model is identical to any other channel:

- Route target is a group folder path
- "Channel" receives the message and responds
- Responses route back through the originating channel

Future: agent registers as `http://agentd:8092`. gated
becomes a pure router with no container logic.

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

## Decided (previously open)

### Large file delivery

**Inbound**: channel uploads file to router via
`POST /v1/files` (multipart). Router stores in group
media dir, returns path. Channel then references path
in the message attachment. This works regardless of
NAT — channel always initiates the connection to router.

```
POST /v1/files
Authorization: Bearer <session-token>
Content-Type: multipart/form-data
- chat_jid: "telegram:-1001234567"
- filename: "photo.jpg"
- file: <binary>

-> 200 {"ok": true, "path": "media/photo-abc123.jpg"}
```

Channel includes the returned `path` in the attachment
object instead of a URL. Router resolves locally.

**Outbound**: router sends file via multipart POST to
channel's `/send-file` endpoint. Unchanged.

### Event types beyond messages

Specific endpoints for now. Each event type gets its own
router endpoint when needed. No generic `/v1/events`.

### Multiple instances of same channel

Two telegram bots: each registers with different JID
prefixes. Router routes by prefix. No conflict.
