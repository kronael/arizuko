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
  "name":         "telegram-mybot",
  "url":          "http://telegram:8080",
  "jid_prefixes": ["telegram:mybot/"],
  "capabilities": {
    "send_text":     true,
    "send_file":     true,
    "typing":        true,
    "fetch_history": false,
    "fwd":           false,
    "quote":         false,
    "repost":        false,
    "dislike":       false,
    "edit":          false
  }
}

→ 200 {"ok": true, "token": "<session-token>"}
```

`name` is the unique adapter identifier (also written to
`messages.source` on inbound delivery). `jid_prefixes` declares which
JIDs the adapter owns — the router picks a return adapter by prefix
match. `url` is where router calls the channel. Session token is used
for subsequent channel→router calls.

Re-registering an existing `name` from a different source IP or under
a different **service principal** is rejected with 409 (origin pin);
same origin re-registers transparently and refreshes the session token.
The pin is the verified `sub`, not the bearer — a `service:<daemon>`
token rotates ~hourly and pinning the raw JWT would reject every
legitimate refresh (`routd/channels.go`).

#### Deliver inbound message

```
POST /v1/messages
Authorization: Bearer <session-token>

{
  "id": "msg-uuid",
  "chat_jid": "telegram:mybot/-1001234567",
  "sender": "telegram:user/12345",
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

Attachments: channel serves files on its own HTTP server and the
router fetches them for agent context. Adapters that already have
the bytes in hand (e.g. whapd) may instead inline base64 in
`attachments[].data` and omit `url`.

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
  "chat_jid":  "telegram:mybot/-1001234567",
  "content":   "reply text",
  "reply_to":  "msg-uuid",
  "thread_id": "topic-123"
}

→ 200 {"ok": true, "id": "telegram-msg-456"}
```

`reply_to` is the prior message ID; `thread_id` pins the message to a
platform-native thread/topic when the adapter supports it (Telegram
forum topics, Discord threads, Mastodon thread roots). Both fields are
optional. Synchronous delivery — 200 means the message landed on the
platform. The returned `id` is the platform-native message ID and may
be empty when the adapter has no useful ID to surface.

#### Send file

```
POST /send-file
Authorization: Bearer <shared-secret>

Content-Type: multipart/form-data
- chat_jid: "telegram:mybot/-1001234567"
- filename: "report.pdf"
- caption:  "optional caption text"   (optional)
- file:     <binary>

→ 200 {"ok": true}
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

→ 200 {"status":"ok","name":"telegram-mybot","jid_prefixes":["telegram:mybot/"],"last_inbound_at":1709942400}
→ 503 {"status":"disconnected", ...}                       (platform link down)
→ 503 {"status":"stale","stale_seconds":420, ...}          (no inbound past threshold)
```

Status precedence: `disconnected` > `stale` > `ok`. The staleness
threshold is per-adapter (5m default; 10m for email, 60m for reddit).
Router calls every 30s; three consecutive failures auto-deregister
and outbound queues internally until the channel re-registers.

## Capabilities

Declared at registration. The router refuses outbound calls when the
relevant capability is `false`:

| Capability      | Endpoint guarded                      |
| --------------- | ------------------------------------- |
| `send_text`     | `/send`                               |
| `send_file`     | `/send-file`                          |
| `typing`        | `/typing` (silently no-op when false) |
| `fetch_history` | `GET /v1/history`                     |
| `fwd`           | `/forward`                            |
| `quote`         | `/quote`                              |
| `repost`        | `/repost`                             |
| `dislike`       | `/dislike` (separate from `/like`)    |
| `edit`          | `/edit`                               |

Extensible. Unknown capabilities are ignored. `/like`, `/post`,
`/delete` are not capability-gated by the router — adapters that
can't satisfy them respond `501 unsupported` with a structured
`{tool, platform, hint}` body.

## Internal services and outbound

Internal services (onbod, timed, dashd) are NOT channels. They do
not call `/v1/channels/register`. They are HTTP clients of the
router that emit outbound messages via `/v1/outbound` (see below)
when they need to deliver something to a chat.

The earlier design persisted an adapter registry in a SQL
`channels` table for cross-process lookup. That table was retired
in v0.28.0 — the in-memory `chanreg` registry (fed by HTTP
self-registration of external adapters) is the only registry.
The SQL table was dropped in migration 0065.

### Outbound via router (`/v1/outbound`)

Internal services (onbod, timed, dashd) send outbound messages
through the router rather than POSTing adapter `/send` endpoints
directly. This lets the router resolve the correct adapter by
JID prefix and enforce auth:

```
POST /v1/outbound
Authorization: Bearer <service:<daemon> JWT>   // scope messages:write

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

A route target is a string. Its shape determines how routd
dispatches the message:

- **Folder path** (default) — e.g. `REDACTED/content`, optionally
  written as `folder:REDACTED/content`. routd writes the message
  to the messages table; the agent container picks it up.
- **`daemon:<name>`** — HTTP POST to a registered daemon's `/send`
  endpoint (same lookup as external channel adapters). Reserved
  for future expansion.
- **`builtin:<name>`** — in-routd handler. Reserved.

`folder:` is optional; existing bare-path rows continue to work.
Only explicit daemon/builtin targets need a prefix.

Slash-commands (`/approve`, `/reject`, and the rest) are dispatched
directly by `routd/steer.go` — they never flow through routes. Route
resolution itself is [`E-routd.md`](E-routd.md).

## Agent channel

The agent is an implicit channel: it never registers, and its
"address" is a folder rather than a URL. The conceptual model is
otherwise identical to any other channel:

- Route target is a group folder path
- "Channel" receives the message and responds
- Responses route back through the originating channel

The split made this real — `runed` owns the spawn (`docker run`,
[`P-runed.md`](P-runed.md)) and `routd` is a pure router with no
container logic ([`E-routd.md`](E-routd.md)).

## Auth

An adapter exchanges its `AUTHD_SERVICE_KEY` bootstrap secret for a
`service:<daemon>` ES256 JWT and presents that on **every**
authenticated routd call — registration, `/v1/messages`, `/v1/pane`
(`chanlib.RouterClient.svcToken`). routd offline-verifies against
authd's JWKS; no shared symmetric secret remains
([`1-auth-standalone.md`](1-auth-standalone.md)). The exchange is
under the **daemon** principal (`AUTHD_SERVICE_NAME`, e.g. `teled`),
never the channel name — the mismatch 401s and outbound dies silently.

Router → channel is the internal docker network plus the adapter's own
`/send` handler; adapters are not reachable from outside it.

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

## `chat_jid` and `sender` are different shapes

A chat JID names a CONVERSATION and carries the adapter's registered prefix:
`telegram:mybot/-1001234567`. A sender names a PERSON and does not:
`telegram:user/12345` (`teled/bot.go`). They share only the scheme before the
`:`. The two are checked separately at ingress, and by different rules:

- **`chat_jid`** must fall under a `jid_prefixes` entry the registering adapter
  declared — `Entry.Owns`. An adapter cannot inject conversations it does not
  serve.
- **`sender`** must sit in a platform scheme that adapter registered —
  `Entry.OwnsScheme`. Prefix ownership cannot be used here: registered prefixes
  are chat prefixes (`telegram:mybot/`) while senders are `telegram:user/<id>`,
  so `Owns` would reject every legitimate sender.

The sender check is deliberately NOT nested inside the chat-JID check.
`web:`, `hook:` and bare-folder chat JIDs skip prefix ownership entirely, so an
adapter posting `chat_jid: "web:main"` with another platform's sender would
otherwise meet no check at all.

Sender is authorization-bearing — `routd/steer.go` passes it to `IsOperator`,
which reaches `auth.Authorize(…, "admin", "**")` — so an unvalidated sender is
an escalation, not a cosmetic error. A bare scheme-less sender stays allowed: it
cannot collide with an OAuth sub, which is always `<provider>:<id>`.
