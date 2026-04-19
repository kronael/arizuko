---
status: shipped
---

# Channel Adapter Protocol

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
  "platform": "telegram",
  "account":  "mybot",
  "url":      "http://telegram:8080",
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
  "chat_jid": "telegram:mybot/-1001234567",
  "sender": "telegram:mybot/12345",
  "sender_name": "Alice",
  "content": "hello",
  "timestamp": 1709942400,
  "reply_to": "msg-prev-uuid",
  "attachments": [
    {
      "mime": "image/jpeg",
      "filename": "photo.jpg",
      "url": "http://telegram:8080/files/abc123",
      "size": 84210
    }
  ]
}

→ 200 {"ok": true}
```

Attachments: channel serves files on its own HTTP server.
Router fetches if needed for agent context.

The router stamps `messages.source` with the registered adapter
name on every inbound delivery. This is the canonical record of
which adapter received the message and is what outbound routing
uses to pick a return adapter.

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
  "chat_jid": "telegram:mybot/-1001234567",
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
- chat_jid: "telegram:mybot/-1001234567"
- filename: "report.pdf"
- caption:  "optional caption text"   (optional)
- file: <binary>

→ 200 {"ok": true, "message_id": "telegram-msg-457"}
```

#### Typing

```
POST /typing
Authorization: Bearer <shared-secret>

{"chat_jid": "telegram:mybot/-1001234567", "on": true}

→ 200 {"ok": true}
```

Fire-and-forget. Failure is not an error.

#### Health

```
GET /health

→ 200 {"status": "ok", "platform": "telegram", "account": "mybot"}
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

### Outbound via router (`/v1/outbound`)

Internal services (onbod, timed, dashd) send outbound messages
through the router rather than POSTing adapter `/send` endpoints
directly. This lets the router resolve the correct adapter by
JID prefix and enforce auth:

```
POST /v1/outbound
Authorization: Bearer <CHANNEL_SECRET>

{
  "jid":     "<chat_jid>",
  "text":    "<reply text>",
  "channel": "telegram-REDACTED"   // optional: pin to a specific adapter
}

→ 200 {"ok": true}
```

**Adapter resolution.** When multiple adapters share the same JID
prefix (e.g. primary `telegram` + `telegram-REDACTED` both handle
`telegram:`), the router resolves the return adapter in this order:

1. Explicit `channel` field on `/v1/outbound`, if registered.
2. `messages.source` of the latest non-bot inbound on this chat
   (`store.LatestSource`).
3. `chanreg.ForJID(jid)` — first owner found by prefix.

`messages.source` is stamped at inbound delivery, so the second
step always succeeds when the chat has any prior inbound. Internal
producers (onbod, timed) pass `channel: "onboarding"` etc. via
`/v1/outbound` only when they need to override the inbound source.

## Route targets

A route target is a string. Its shape determines how gated
dispatches the message:

- **Folder path** (default) — e.g. `REDACTED/content`, optionally
  written as `folder:REDACTED/content`. Gateway writes the message
  to the messages table; the agent container picks it up.
- **`daemon:<name>`** — HTTP POST to a registered daemon's `/send`
  endpoint (same lookup as external channel adapters). Reserved
  for future expansion.
- **`builtin:<name>`** — in-gateway handler. Reserved.

`folder:` is optional; existing bare-path rows continue to work.
Only explicit daemon/builtin targets need a prefix.

gated's command layer (`gatewayCommands` table in
`gateway/commands.go`) dispatches `/approve`, `/reject`, and all
other slash-commands directly — they never flow through routes.
See `specs/4/9-gated.md` for route resolution details.

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
| `http://telegram:8080`                   | Docker network |
| `http://localhost:8080`                  | TCP local      |
| `http://10.0.0.5:8080`                   | TCP remote     |
| `http+unix:///run/arizuko/telegram.sock` | Unix socket    |
| vsock CID:port                           | vsock          |

**Future**: HTTP over unix socket and vsock are natively
supported in Go (`net/http` accepts any `net.Listener`).
The protocol is pure HTTP regardless of transport — no
changes needed, just a different dialer. Not building
toward this now, but the design is compatible.

## Decided (previously open)

### Large file delivery

**Inbound**: Channel adapters serve files on their own HTTP server and
reference file URLs in the `attachments` array. The gateway enricher
fetches them and writes to `groups/<folder>/media/<YYYYMMDD>/` before
agent spawn. teled serves `GET /files/{fileID}` as a proxy to the
Telegram CDN (Telegram file URLs require a bot token and are ephemeral).
discd uses direct CDN URLs.

**Outbound**: router sends file via multipart POST to channel's
`/send-file` endpoint with optional `caption` form field.

### Event types beyond messages

Specific endpoints for now. Each event type gets its own
router endpoint when needed. No generic `/v1/events`.

### Multiple instances of same channel

Two telegram bots: each has a different bot username — the account
segment comes from `api.Self.UserName` after auth. JIDs become
`telegram:mainbot/<id>` vs `telegram:supportbot/<id>`. Each registers
with its own prefix. Router routes by prefix. No conflict.
`CHANNEL_ACCOUNT` overrides the platform name if needed.
See specs/5/R-multi-account.md and specs/5/S-jid-format.md.
