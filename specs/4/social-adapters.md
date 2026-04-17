---
status: draft
---

## status: shipped

# social adapters

Channel adapters for social platforms. Each is a standalone Go daemon
that registers with the router via HTTP, then forwards inbound events as
messages and receives outbound replies.

All adapters use `chanlib` for: router registration, JWT token exchange,
message delivery, deregistration on shutdown, and HTTP auth middleware.

## Adapter protocol

Every adapter:

1. On startup: `POST /v1/channels/register` with `name`, `url`, `jid_prefixes`, `capabilities`
2. Receives `token` from router; uses it as Bearer for all subsequent calls
3. On inbound event: `POST /v1/messages` with `InboundMsg`
4. Serves `POST /send` for outbound delivery from router
5. On shutdown: `POST /v1/channels/deregister`

See `specs/4/1-channel-protocol.md` for full HTTP protocol spec.

## Adapters

| Daemon | Platform | JID prefix  | Port | Status  |
| ------ | -------- | ----------- | ---- | ------- |
| teled  | Telegram | `telegram:` | 8080 | shipped |
| discd  | Discord  | `discord:`  | 8080 | shipped |
| emaid  | Email    | `email:`    | 8080 | shipped |
| mastd  | Mastodon | `mastodon:` | 8080 | shipped |
| bskyd  | Bluesky  | `bluesky:`  | 8080 | shipped |
| reditd | Reddit   | `reddit:`   | 8080 | shipped |
| whapd  | WhatsApp | `whatsapp:` | 8080 | shipped |

## Capabilities

| Adapter | send_text | send_file | typing |
| ------- | --------- | --------- | ------ |
| teled   | yes       | yes       | yes    |
| discd   | yes       | yes       | yes    |
| mastd   | yes       | —         | —      |
| bskyd   | yes       | —         | —      |
| reditd  | yes       | —         | —      |
| whapd   | yes       | yes       | yes    |

## Environment variables

### Common (all adapters)

| Var              | Default                           | Required |
| ---------------- | --------------------------------- | -------- |
| `CHANNEL_NAME`   | platform name (e.g. `"telegram"`) | no       |
| `ROUTER_URL`     | —                                 | yes      |
| `CHANNEL_SECRET` | —                                 | no       |
| `LISTEN_ADDR`    | `:PORT` (see table above)         | no       |
| `LISTEN_URL`     | `http://<name>:PORT`              | no       |

### teled

| Var                  | Required |
| -------------------- | -------- |
| `TELEGRAM_BOT_TOKEN` | yes      |
| `ASSISTANT_NAME`     | no       |

### discd

| Var                 | Required |
| ------------------- | -------- |
| `DISCORD_BOT_TOKEN` | yes      |
| `ASSISTANT_NAME`    | no       |

### mastd

| Var                     | Required |
| ----------------------- | -------- |
| `MASTODON_INSTANCE_URL` | yes      |
| `MASTODON_ACCESS_TOKEN` | yes      |

### bskyd

| Var                  | Default               | Required |
| -------------------- | --------------------- | -------- |
| `BLUESKY_IDENTIFIER` | —                     | yes      |
| `BLUESKY_PASSWORD`   | —                     | yes      |
| `BLUESKY_SERVICE`    | `https://bsky.social` | no       |
| `DATA_DIR`           | `/srv/data/bskyd`     | no       |

### reditd

| Var                    | Required             |
| ---------------------- | -------------------- |
| `REDDIT_CLIENT_ID`     | yes                  |
| `REDDIT_CLIENT_SECRET` | yes                  |
| `REDDIT_USERNAME`      | yes                  |
| `REDDIT_PASSWORD`      | yes                  |
| `REDDIT_SUBREDDITS`    | no (comma-separated) |
| `REDDIT_USER_AGENT`    | no (`arizuko/1.0`)   |

## emaid

IMAP IDLE push + SMTP STARTTLS replies. JID format: `email:<address>`.
Persistent TLS connection; server pushes EXISTS on new messages.
Reconnects with exponential backoff on error.
Config: `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_IMAP_PORT` (default 993),
`EMAIL_SMTP_PORT` (default 587), `EMAIL_USER`, `EMAIL_PASS`.

## whapd

WhatsApp adapter written in TypeScript using Baileys. JID format:
`whatsapp:<lid>@lid` for DMs (Baileys' opaque LID identifier),
`whatsapp:<group-id>@g.us` for groups. LIDs are opaque and stable per
account; arizuko does not translate them to phone numbers.
Credentials stored under `DATA_DIR/baileys/` via Baileys
`useMultiFileAuthState`. Pairing uses QR code on first run.

**Registration resilience.** Router registration is retried with
exponential backoff — whapd never calls `process.exit()` on
register failure. The prior behavior caused a restart loop that
truncated `creds.json` mid-write during the next container kill,
because Baileys' `writeFile` is non-atomic.

**Credential recovery.** On startup whapd calls `recoverCredsIfEmpty`
to restore from `creds.json.bak` if the live file is 0 bytes, and
`backupCreds` before any rewrite. Stale backups (>3 days) require a
manual QR re-pair.

Config: `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_URL` (default
`http://whapd:8080`), `DATA_DIR` (default `/data`).

## Layout

```
teled/   — Telegram adapter (Go)
discd/   — Discord adapter (Go)
mastd/   — Mastodon adapter (Go)
bskyd/   — Bluesky adapter (Go)
reditd/  — Reddit adapter (Go)
chanlib/ — shared primitives (RouterClient, Auth, WriteJSON, EnvOr, MustEnv)
```
