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
| teled  | Telegram | `telegram:` | 9001 | shipped |
| discd  | Discord  | `discord:`  | 9002 | shipped |
| emaid  | Email    | `email:`    | 9003 | planned |
| mastd  | Mastodon | `mastodon:` | 9004 | shipped |
| bskyd  | Bluesky  | `bluesky:`  | 9005 | shipped |
| reditd | Reddit   | `reddit:`   | 9006 | shipped |

## Capabilities

| Adapter | send_text | send_file | typing |
| ------- | --------- | --------- | ------ |
| teled   | yes       | yes       | yes    |
| discd   | yes       | yes       | yes    |
| mastd   | yes       | —         | —      |
| bskyd   | yes       | —         | —      |
| reditd  | yes       | —         | —      |

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

IMAP TLS polling + SMTP STARTTLS replies. JID format: `email:<address>`.
Config: `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_IMAP_PORT` (default 993),
`EMAIL_SMTP_PORT` (default 587), `EMAIL_USER`, `EMAIL_PASS`.

## Layout

```
teled/   — Telegram adapter (Go)
discd/   — Discord adapter (Go)
mastd/   — Mastodon adapter (Go)
bskyd/   — Bluesky adapter (Go)
reditd/  — Reddit adapter (Go)
chanlib/ — shared primitives (RouterClient, Auth, WriteJSON, EnvOr, MustEnv)
```
