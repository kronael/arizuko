# slakd

Slack channel adapter (bot-token, single-workspace, HTTP Events API).

## Purpose

Bridges Slack workspace events to the router. Receives via Events API
webhook (proxyd → `/slack/events`); sends via Web API. Mirrors `discd`
in shape — registers with `routd` via `chanlib.RouterClient`, exposes
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
- Outbound Web API calls go through `chanlib.DoWithRetry` — 429 honours
  `Retry-After` (capped at 30s); 5xx and network errors retry with
  jittered backoff (~300ms, ~800ms), 3 attempts total.

## Entry points

- Binary: `slakd/main.go`
- Listen: `$LISTEN_ADDR` (default `:8080`; container-internal per
  arizuko convention)
- Public surface: proxyd forwards `/slack/events` → `slakd:8080`
- Router registration: `slack:` prefix, caps `send_text`, `send_file`,
  `edit`, `like`, `delete`, `dislike`, `post`. Hint-only verbs
  (`forward`, `quote`, `repost`) return 501 with structured hints.

## Verb support

| Verb               | Status             | Notes                                                            |
| ------------------ | ------------------ | ---------------------------------------------------------------- |
| `send`             | native             | `chat.postMessage`; honours `reply_to` → `thread_ts`             |
| `send_file`        | native             | `files.getUploadURLExternal` + PUT + `completeUploadExternal`    |
| `edit`             | native             | `chat.update`                                                    |
| `delete`           | native             | `chat.delete`                                                    |
| `like`             | native             | `reactions.add` (emoji name from `Reaction`, default `thumbsup`) |
| `dislike`          | native             | `reactions.add` with `thumbsdown` (per dislike-via-like)         |
| `post`             | native             | maps to `send` on a channel JID                                  |
| `forward`          | hint               | redirects to `send` quoting source                               |
| `quote`            | hint               | redirects to `send(reply_to=<ts>)` to thread under the source    |
| `repost`           | hint               | redirects to `send`                                              |
| `pane_set_prompts` | native (pane only) | `assistant.threads.setSuggestedPrompts` after next Send          |
| `pane_set_title`   | native (pane only) | `assistant.threads.setTitle` after next Send                     |

## Assistant pane (specs/6/D)

Slack's "Agents & AI Apps" feature gives the bot a dedicated sidebar
pane. slakd implements the full lifecycle when the app has
`assistant:write` scope and subscribes to `assistant_thread_started`
and `assistant_thread_context_changed`:

- `assistant_thread_started` → upserts a row in `pane_sessions` keyed
  by `(team_id, user_id, thread_ts)`, sets the pane title
  (`<ASSISTANT_NAME> — chat`), seeds three default suggested prompts,
  and dispatches a synthetic `pane_open` inbound so the agent gets
  a turn.
- `assistant_thread_context_changed` → updates
  `pane_sessions.context_jid` with the workspace channel the user is
  viewing. No synthetic turn (context drift isn't a user action).
- `Typing()` adds a 👀 reaction to the last inbound message for the JID
  (on=true) and removes it (on=false). Same path for pane and regular
  channels — no `conversations.typing` (RTM-only, discontinued) and no
  `assistant.threads.setStatus`. Silent no-op when no prior inbound
  message is known.
- `Send()` into a pane channel drains pending prompts/title staged
  via the MCP tools below and fires `assistant.threads.setSuggestedPrompts`
  / `setTitle` after the `chat.postMessage` succeeds.

MCP-driven control (per outbound, one-shot):

- `POST /v1/pane/prompts {jid, prompts}` — channel-secret gated;
  stages prompts on the bot for the next Send into that pane.
  Returns 404 when the jid doesn't map to an open pane (chanreg
  maps that to `chanlib.ErrUnsupported`).
- `POST /v1/pane/title {jid, title}` — same shape; one-shot title.

Both endpoints are reached by routd via `chanreg.HTTPChannel`'s
`SetSuggestions` / `SetName` (implementing the optional
`core.Suggester` and `core.Namer` capabilities). Today slakd is the
only adapter that implements them.

slakd opens `routd.db` to read `pane_sessions` rows (routd owns the
table); pane writes go via HTTP (`POST /v1/pane` to routd). `DB_PATH`
(preferred) or `DATA_DIR/store` points to the DB directory. Unset →
pane reads fail, prompts/title staging returns 404.

## Dependencies

- `chanlib`, `store` (pane_sessions table only — `routd` owns the
  schema and runs migrations)

## Configuration

```
SLACK_BOT_TOKEN=xoxb-...      required (Bot User OAuth Token)
SLACK_SIGNING_SECRET=...      required (Slack App → Basic Information)
ROUTER_URL=http://routd:8080  required
CHANNEL_SECRET=...            (or SLAKD_CHANNEL_SECRET) for chanlib.Auth
LISTEN_ADDR=:8080             internal HTTP listener
LISTEN_URL=http://slakd:8080  URL the router uses to reach slakd
ASSISTANT_NAME=...            shown in pane title (<name> — chat); omit → "chat"
DB_PATH=...                   path to routd.db (preferred for pane reads)
DATA_DIR=...                  fallback (uses DATA_DIR/store); omit both → no pane support
SLAKD_USERS_CACHE_TTL=900     users.info + conversations.info TTL (seconds)
MEDIA_MAX_FILE_BYTES=20971520 file-proxy cap
CHANNEL_NAME=slack            registration name
SLAKD_STALE_SECONDS=300       inbound silence past this ⇒ /health status=stale (still 200) + watchdog auth.test probe
SLAKD_WATCHDOG_SECONDS=60     watchdog re-check interval
SLAKD_STALE_FAIL_LIMIT=5      consecutive auth.test failures (while stale) before os.Exit(1) restart
```

`SLAKD_CHANNEL_SECRET` takes precedence over `CHANNEL_SECRET` on the
adapter side. Must equal `CHANNEL_SECRET` until routd gains per-adapter
registration auth (not yet implemented; a different value will cause 401).

## Operator runbook (per-workspace install)

1. Create a Slack App at <https://api.slack.com/apps> → "From scratch".
2. **OAuth & Permissions** → add Bot Token Scopes:
   `channels:history`, `channels:read`, `groups:history`, `groups:read`,
   `im:history`, `im:read`, `mpim:history`, `mpim:read`,
   `chat:write`, `chat:write.public`, `reactions:read`,
   `reactions:write`, `files:read`, `files:write`, `users:read`.
   Note: `reactions:read` is required for `reaction_added` event delivery
   even though only `reactions:write` is used outbound.
3. **Event Subscriptions** → enable, set Request URL to
   `https://<your-host>/slack/events` (proxyd handles TLS + forwards
   verbatim to slakd). Subscribe to bot events:
   `message.channels`, `message.groups`, `message.im`, `message.mpim`,
   `reaction_added`, `member_joined_channel`. File attachments arrive
   piggy-backed on `message.*` events; do not subscribe to `file_shared`.
4. **Basic Information** → copy _Signing Secret_ into
   `SLACK_SIGNING_SECRET`.
5. Install to Workspace → copy _Bot User OAuth Token_ (starts with
   `xoxb-`) into `SLACK_BOT_TOKEN`.
6. Invite the bot to channels: `/invite @<botname>`.
7. `arizuko create <inst> && arizuko run <inst>` — check
   `GET /health` returns 200 once `auth.test` succeeds.

## Health signal

`GET /health` returns 503 when `auth.test` has not succeeded (token
revoked, network outage, Slack down) — body `{"status":"disconnected"}`.
`auth.test` is the honest liveness signal: a `healthProbe` goroutine
re-runs it every 60s and the watchdog re-runs it whenever inbound is
stale, flipping `isConnected()` (and thus the 503) on the result. A
silently-paused event subscription that leaves the token valid is caught
the same way — see the watchdog below.

**Inbound staleness is informational, not a 503.** When the last inbound
is older than `SLAKD_STALE_SECONDS` the body reads
`{"status":"stale","stale_seconds":N}` but the code stays 200 — a quiet
workspace is normal and must not be marked unhealthy. (Returning 503 on
stale-but-connected bounced `slakd_marinade` on a genuinely quiet
workspace, 2026-06-19.) Death detection lives entirely in `auth.test`,
not in silence.

A watchdog goroutine (`SLAKD_WATCHDOG_SECONDS` cadence) re-probes
`auth.test` while stale — the only recovery slakd can self-perform,
since there is no socket to reconnect. Silence alone never restarts:
stale inbound with `auth.test` OK is treated as merely quiet (it bounced
the container every ~10 min on idle instances, and each restart drops
Slack's in-flight POSTs — a self-inflicted flap, marinade 2026-06-08).
Only when the channel is stale AND `auth.test` keeps failing for
`SLAKD_STALE_FAIL_LIMIT` consecutive ticks does it call `os.Exit(1)`,
letting the `restart: on-failure` policy re-create the container — this
is the real dead-subscription backstop that the 2026-06-05 11h-outage
guard ultimately became. A single inbound message — or a single passing
`auth.test` — resets the streak.

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
- Socket Mode (not supported; HTTP webhooks via proxyd only).
- Enterprise Grid, slash commands, modals, home tab, Block Kit, user
  tokens — separate specs.
- Custom-emoji-as-dislike (needs per-workspace `emoji.list` mapping;
  v1 falls through `ClassifyEmoji`'s unknown→like default and the
  agent gets the raw name on `Reaction`).

## Related docs

- `specs/2/l-slakd.md` — adapter spec
- `specs/4/1-channel-protocol.md` — router contract
