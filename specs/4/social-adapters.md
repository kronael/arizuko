---
status: shipped
---

# social adapters

Channel adapters for social platforms. Each is a standalone daemon
(Go for most, TypeScript for `whapd` and `twitd`) that registers with
the router via HTTP, then forwards inbound events as messages and
receives outbound replies.

Go adapters use `chanlib` for router registration, opaque session
token issuance, message delivery, deregistration on shutdown, and
HTTP auth middleware. The two TypeScript adapters reimplement the
same protocol against `chanlib`'s wire format.

## Adapter protocol

Every adapter:

1. On startup: `POST /v1/channels/register` with `name`, `url`, `jid_prefixes`, `capabilities`
2. Receives `token` from router; uses it as Bearer for all subsequent calls
3. On inbound event: `POST /v1/messages` with `InboundMsg`
4. Serves `POST /send` for outbound delivery from router
5. On shutdown: `POST /v1/channels/deregister`

See `specs/4/1-channel-protocol.md` for full HTTP protocol spec.

## Adapters

Container port is `:8080` for every adapter (compose template pins
`LISTEN_ADDR=:8080`). Source defaults differ — see each daemon's
`README.md`.

| Daemon | Platform  | JID prefix  | Status  |
| ------ | --------- | ----------- | ------- |
| teled  | Telegram  | `telegram:` | shipped |
| discd  | Discord   | `discord:`  | shipped |
| emaid  | Email     | `email:`    | shipped |
| mastd  | Mastodon  | `mastodon:` | shipped |
| bskyd  | Bluesky   | `bluesky:`  | shipped |
| reditd | Reddit    | `reddit:`   | shipped |
| linkd  | LinkedIn  | `linkedin:` | shipped |
| whapd  | WhatsApp  | `whatsapp:` | shipped |
| twitd  | Twitter/X | `x:`        | shipped |

## Capabilities

Basic message capabilities only — see each adapter's `README.md`
"Verb support" table for the full social-action surface (post, like,
edit, delete, reply, etc.).

| Adapter | send_text | send_file | typing |
| ------- | --------- | --------- | ------ |
| teled   | yes       | yes       | yes    |
| discd   | yes       | yes       | yes    |
| emaid   | yes       | —         | —      |
| mastd   | yes       | —         | —      |
| bskyd   | yes       | —         | —      |
| reditd  | yes       | —         | —      |
| linkd   | yes       | —         | —      |
| whapd   | yes       | yes       | yes    |
| twitd   | yes       | yes       | —      |

## Environment variables

### Common (all adapters)

| Var              | Default                                 | Required |
| ---------------- | --------------------------------------- | -------- |
| `CHANNEL_NAME`   | platform name (e.g. `"telegram"`)       | no       |
| `ROUTER_URL`     | —                                       | yes      |
| `CHANNEL_SECRET` | —                                       | no       |
| `LISTEN_ADDR`    | adapter-specific (compose pins `:8080`) | no       |
| `LISTEN_URL`     | `http://<name>:<port>`                  | no       |

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

IMAP IDLE push (with poll fallback) + SMTP STARTTLS replies. JID format:
`email:<address>`. Persistent TLS connection; server pushes EXISTS on new
messages. Reconnects with exponential backoff on error.
Config: `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_IMAP_PORT` (default 993),
`EMAIL_SMTP_PORT` (default 587), `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`,
`EMAIL_STRICT_AUTH` (`true` rejects unsigned senders).

## whapd

WhatsApp adapter written in TypeScript using Baileys. JID format:
`whatsapp:<jid>` where `<jid>` is whatever Baileys returns —
typically `<lid>@lid` for DMs (Baileys' opaque LID identifier) and
`<group-id>@g.us` for groups. LIDs are opaque and stable per
account; arizuko does not translate them to phone numbers.
Credentials stored via Baileys `useMultiFileAuthState` under
`$WHATSAPP_AUTH_DIR` (default `$DATA_DIR/store/whatsapp-auth`).
Pairing uses QR code on first run.

**Registration resilience.** Router registration is retried with
exponential backoff — whapd never calls `process.exit()` on
register failure. The prior behavior caused a restart loop that
truncated `creds.json` mid-write during the next container kill,
because Baileys' `writeFile` is non-atomic.

**Credential recovery.** On startup whapd calls `recoverCredsIfEmpty`
to restore from `creds.json.bak` if the live file is 0 bytes, and
`backupCreds` after each rewrite. Stale backups (>3 days) require a
manual QR re-pair.

Config: `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`,
`WHATSAPP_AUTH_DIR`, `DATA_DIR`, `ASSISTANT_NAME`.

## Layout

```
teled/   — Telegram adapter (Go)
discd/   — Discord adapter (Go)
mastd/   — Mastodon adapter (Go)
bskyd/   — Bluesky adapter (Go)
reditd/  — Reddit adapter (Go)
emaid/   — Email adapter (Go, IMAP + SMTP)
linkd/   — LinkedIn adapter (Go)
whapd/   — WhatsApp adapter (TypeScript, Baileys)
twitd/   — Twitter/X adapter (TypeScript, agent-twitter-client)
chanlib/ — shared primitives for Go adapters; see chanlib/README.md
```
