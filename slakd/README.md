# slakd

Slack channel adapter (bot-token, single-workspace, HTTP Events API).

## Purpose

Bridges Slack workspace events to the router. Receives via Events API
webhook (proxyd → `/slack/events`); sends via Web API. Mirrors `discd`
in shape — registers with `gated` via `chanlib.RouterClient`, exposes
`/send`, `/like`, `/delete`, `/edit`, `/health`, `/files/`.

## Responsibilities

- Verify `X-Slack-Signature` HMAC of `v0:<ts>:<body>` on every webhook;
  reject on skew >5 min or mismatch.
- Post inbound to router with `slack:<workspace>/<kind>/<id>` JIDs
  (kind ∈ {`channel`, `dm`, `group`}).
- Emit synthetic `like`/`dislike` inbound for `reaction_added` (raw
  name on `InboundMsg.Reaction`, classified via `chanlib.ClassifyEmoji`).
- Derive `Verb="mention"` when text contains `<@bot_user_id>`; channel
  messages without mention emit `Verb=""`; DMs emit `Verb=""`.
- Handle `/send` (`chat.postMessage` +`thread_ts`), `/edit`
  (`chat.update`), `/delete`, `/like` (`reactions.add`), `/dislike`
  (`reactions.add` thumbsdown — dislike-via-like), `/send-file`
  (`files.getUploadURLExternal` + `files.completeUploadExternal`).
- Proxy `url_private` file URLs via `GET /files/<id>` adding upstream
  `Authorization: Bearer $SLACK_BOT_TOKEN`.

## Entry points

- Binary: `slakd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9009`; compose template sets `:8080`)
- Public surface: proxyd forwards `/slack/events` → `slakd:8080`
- Router registration: `slack:` prefix, caps `send_text`, `send_file`,
  `edit`, `like`, `delete`, `dislike`, `post`. Hint-only verbs
  (`forward`, `quote`, `repost`) return 501 with structured hints.

## Verb support

| Verb        | Status | Notes                                                            |
| ----------- | ------ | ---------------------------------------------------------------- |
| `send`      | native | `chat.postMessage`; honours `reply_to` → `thread_ts`             |
| `send_file` | native | `files.getUploadURLExternal` + PUT + `completeUploadExternal`    |
| `edit`      | native | `chat.update`                                                    |
| `delete`    | native | `chat.delete`                                                    |
| `like`      | native | `reactions.add` (emoji name from `Reaction`, default `thumbsup`) |
| `dislike`   | native | `reactions.add` with `thumbsdown` (per dislike-via-like)         |
| `post`      | native | maps to `send` on a channel JID                                  |
| `forward`   | hint   | redirects to `send` quoting source                               |
| `quote`     | hint   | redirects to `send(reply_to=<ts>)` to thread under the source    |
| `repost`    | hint   | redirects to `send`                                              |

## Dependencies

- `chanlib`

## Configuration

```
SLACK_BOT_TOKEN=xoxb-...      required (Bot User OAuth Token)
SLACK_SIGNING_SECRET=...      required (Slack App → Basic Information)
ROUTER_URL=http://gated:8080  required
CHANNEL_SECRET=...            (or SLAKD_CHANNEL_SECRET) for chanlib.Auth
LISTEN_ADDR=:9009             internal HTTP listener
LISTEN_URL=http://slakd:9009  URL the router reaches us on
SLAKD_USERS_CACHE_TTL=900     users.info + conversations.info TTL (seconds)
MEDIA_MAX_FILE_BYTES=20971520 file-proxy cap
CHANNEL_NAME=slack            registration name
```

`SLAKD_CHANNEL_SECRET` takes precedence over `CHANNEL_SECRET` so the
operator can scope the chanlib.Auth bearer per adapter.

## Operator runbook (per-workspace install)

1. Create a Slack App at <https://api.slack.com/apps> → "From scratch".
2. **OAuth & Permissions** → add Bot Token Scopes:
   `channels:history`, `channels:read`, `groups:history`, `groups:read`,
   `im:history`, `im:read`, `mpim:history`, `mpim:read`,
   `chat:write`, `chat:write.public`, `reactions:write`,
   `files:write`, `users:read`.
3. **Event Subscriptions** → enable, set Request URL to
   `https://<your-host>/slack/events` (proxyd handles TLS + forwards
   verbatim to slakd). Subscribe to bot events:
   `message.channels`, `message.groups`, `message.im`, `message.mpim`,
   `reaction_added`, `member_joined_channel`, `file_shared`.
4. **Basic Information** → copy _Signing Secret_ into
   `SLACK_SIGNING_SECRET`.
5. Install to Workspace → copy _Bot User OAuth Token_ (starts with
   `xoxb-`) into `SLACK_BOT_TOKEN`.
6. Invite the bot to channels: `/invite @<botname>`.
7. `arizuko create <inst> && arizuko run <inst>` — check
   `GET /health` returns 200 once `auth.test` succeeds.

## Health signal

`GET /health` returns 503 when `auth.test` has not succeeded (token
revoked, network outage, Slack down). Reconnect = restart the process;
slakd doesn't long-poll, so transient Slack outages are observed only
on the next outbound call.

## Threat model

| Surface         | Verifier                                       | Failure mode                                  |
| --------------- | ---------------------------------------------- | --------------------------------------------- |
| `/slack/events` | `X-Slack-Signature` HMAC (signing secret)      | proxyd re-marshals body → HMAC breaks → 401   |
| `/send`, etc.   | `chanlib.Auth` (`CHANNEL_SECRET` bearer)       | router compromise → no further isolation      |
| `/files/<id>`   | `chanlib.Auth` (same bearer) + opaque cache id | id is hex of sha256 prefix; not enumerable    |
| upstream fetch  | `Authorization: Bearer $SLACK_BOT_TOKEN`       | `url_private` URLs unauthorized without token |

proxyd MUST forward `X-Slack-Signature` + `X-Slack-Request-Timestamp`
and the raw body unmodified — any re-marshal invalidates the HMAC.

## Files

- `main.go` — env load + chanlib.Run wiring; `b.files = srv.files`
  before `b.start` to avoid a race with inbound events.
- `bot.go` — Events API dispatch, signing verify, Slack Web API client,
  TTL caches.
- `server.go` — `/slack/events`, `/files/<id>` proxy, chanlib adapter
  mux.
- `jid.go` — `slack:<workspace>/<kind>/<id>` parser/formatter (strict).

## Out of scope (v1)

- OAuth install (manual install only per runbook above).
- Socket Mode (HTTP webhooks via proxyd is the permanent default).
- Enterprise Grid, slash commands, modals, home tab, Block Kit, user
  tokens — separate specs.
- Custom-emoji-as-dislike (needs per-workspace `emoji.list` mapping;
  v1 falls through `ClassifyEmoji`'s unknown→like default and the
  agent gets the raw name on `Reaction`).

## Related docs

- `specs/2/l-slakd.md` — adapter spec
- `specs/4/1-channel-protocol.md` — router contract
