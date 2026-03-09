# Slink Specification — shipped

## What it is

A slink (REDACTED link) is the web channel for a nanoclaw group. Enabled the
same way as Telegram or Discord — by registering a `web:<name>` group.
The gateway generates a public token at registration time; that token is
the channel's POST endpoint.

```
POST /pub/s/<token>   →  delivers to the web group's agent
```

---

## Setup

```bash
nanoclaw group add web:main       # registers group, generates slink token
nanoclaw group add web:support    # multiple web groups per instance ok
```

Same pattern as:

```bash
nanoclaw group add tg:-100123456
nanoclaw group add discord:123456
```

The slink token is generated once at registration, stored on the group
row, never rotated. It is intentionally public — embedded in web pages.
Security comes from rate limiting, not token secrecy.

---

## Token

- 16-char random, URL-safe (96 bits)
- Public — freely shared in page source
- Routes POST to the correct group agent
- Stored in `registered_groups` row (new `slink_token` column)

---

## POST

```
POST /pub/s/<token>
Authorization: Bearer <jwt>   (optional)
Content-Type: application/json

{ "text": "...", "media_url": "https://..." }
```

`text` and `media_url` are both optional but at least one must be present.
`media_url` — URL to an image or file; gateway fetches and attaches it
as a media message alongside `text`. Content-Type is guessed from the
URL extension; falls back to `application/octet-stream`.

Responses:

```
200  { "ok": true }
401  { "error": "unauthorized" }   (malformed/invalid JWT supplied)
404  { "error": "not found" }
429  { "error": "rate limited" }
```

Returns immediately. Gateway delivers as `onMessage(group, { sender,
sender_name, content })` and returns. Caller does not wait for agent reply.

`sender` — JWT `sub` if present, otherwise `anon_<ip-hash>`
`sender_name` — JWT `name` claim if present, otherwise omitted

A supplied JWT that fails verification returns 401. Omitting the
`Authorization` header entirely is allowed (anonymous mode).

---

## Rate Limiting

| Caller              | Bucket           | Limit                              |
| ------------------- | ---------------- | ---------------------------------- |
| Anonymous (no JWT)  | shared per token | 10 req/min across all anon callers |
| Authenticated (JWT) | per JWT sub      | 60 req/min                         |
| Agent / operator    | —                | unlimited                          |

`REDACTED.js` attaches `Authorization: Bearer <jwt>` automatically if a
valid JWT exists in `localStorage` (from `specs/1/3-auth.md`).
Anonymous callers share one pool so spam from many IPs stays bounded.

Thresholds configurable via `SLINK_ANON_RPM` / `SLINK_AUTH_RPM` env vars.

---

## DB

```sql
ALTER TABLE registered_groups ADD COLUMN slink_token TEXT;
-- populated at group registration for web: groups
```

No separate table. Token lives on the group row.

---

## Agent

The agent receives slink POSTs as normal inbound messages on the
`web:<name>` chat_jid. No special handling — same queue, same
`runContainerAgent` call as any other channel.

Agent knows its own slink URL via env vars injected at container start:

- `SLINK_TOKEN` — the raw token
- `WEB_HOST` — the public host, for constructing the full URL

---

## Gateway Routes

```
POST /pub/s/:token     deliver to group agent
GET  /pub/REDACTED.js     client script (see REDACTED spec)
```

---

## MCP note

The slink POST + SSE stream pattern is structurally the deprecated MCP SSE
transport (v2024-11-05). Current MCP standard (v2025-03-26) uses Streamable
HTTP — one endpoint, server streams back via SSE when needed. A future slink
variant could expose a standards-compliant MCP server endpoint, allowing
Claude Desktop and other MCP clients to connect natively. See
`specs/3/J-sse.md` for trade-offs.

## Related

- `specs/1/3-auth.md` — JWT issuance for authenticated web users
- `specs/1/W-slink.md` — how agents use slinks to build living pages (TBD)
- `specs/3/J-sse.md` — SSE scoping + MCP transport direction
