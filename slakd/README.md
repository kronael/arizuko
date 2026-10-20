# slakd

Slack channel adapter (bot-token, single-workspace, HTTP Events API).

## Purpose

Bridges Slack workspace events to the router. Receives via the Events API
webhook (proxyd → `/slack/events`); sends via the Web API. Same shape as
`discd`: registers with routd through `chanlib.RouterClient` and mounts the
standard adapter surface from `chanlib.NewAdapterMux`.

## Responsibilities

- Verify `X-Slack-Signature` HMAC of `v0:<ts>:<body>` on every webhook;
  reject on skew >5 min or mismatch.
- Post inbound to router with `slack:<workspace>/<kind>/<id>` JIDs
  (kind ∈ {`channel`, `dm`, `group`}).
- Emit synthetic `like`/`dislike` inbound for `reaction_added` (raw
  name on `InboundMsg.Reaction`, classified via `chanlib.ClassifyEmoji`).
- Derive `Verb="mention"` when text contains `<@bot_user_id>`; channel
  messages without mention emit `Verb=""`; DMs emit `Verb=""`.
- Serve the outbound verbs; the Slack call behind each is in the table below.
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
- `GET /dash/slakd/` — the operator page, mounted for free by
  `chanlib.NewAdapterMux` from the one renderer in `chanlib/dash.go` (spec
  6/31). Routed by DAEMON name (`AUTHD_SERVICE_NAME`), never by platform.

## Verb support

| Verb               | Status             | Notes                                                            |
| ------------------ | ------------------ | ---------------------------------------------------------------- |
| `send`             | native             | `chat.postMessage`; `thread_ts` from the thread fields, below    |
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

## Replies thread on the trigger

slakd sets `thread_ts` from `thread_id`, the thread a turn already runs in,
or else `thread_root`, the trigger message routd asks it to root a new thread
on (`bot.go` `Send`). `reply_to` never sets `thread_ts`: it often names an
earlier bot row, and a thread rooted there hides the reply in a thread the
user never started. routd sends `thread_root` only for a `reply` to the
trigger chat, when the turn is not in a thread yet and the group's
`thread_replies` setting allows it; unset, the setting follows whether the
chat has several users (`replyThreadRoot`, `routd/turns.go`).
`send` never roots a new thread. When Slack cannot find the
thread, slakd posts the reply once more at the channel root, so the reply is
not lost.

## Assistant pane (specs/8/D)

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
- `Typing()` adds a 👀 reaction to the newest turn-starting inbound for the JID
  (on=true) and removes it from that same message (on=false). The state is
  `chanlib.AckMark`'s: the mark is recorded in the adapter store, a boot sweep
  clears a mark older than `chanlib.DefaultTypingMaxTTL`, and a mark a restart
  or a graceful shutdown left is removed when the turn ends. Same path for pane
  and regular channels — no `conversations.typing` (RTM-only, discontinued) and no
  `assistant.threads.setStatus`. Silent no-op when no inbound is known.
- `Send()` into a pane channel drains pending prompts/title staged
  via the MCP tools below and fires `assistant.threads.setSuggestedPrompts`
  / `setTitle` after the `chat.postMessage` succeeds.

MCP-driven control (per outbound, one-shot):

- `POST /v1/pane/prompts {jid, prompts}` — channel-secret gated;
  stages prompts on the bot for the next Send into that pane.
  Returns 404 when the jid doesn't map to an open pane (chanreg
  maps that to `chanlib.ErrUnsupported`).
- `POST /v1/pane/title {jid, title}` — same shape; one-shot title.

Both endpoints are reached by routd through `chanreg.HTTPChannel`'s
`SetSuggestions` / `SetName`. Today slakd is the only adapter that implements
them.

Pane READS open routd's owner (`store.OpenOwner(store.OwnerRoutd)`) for
`pane_sessions`; routd owns that table, and pane WRITES go over HTTP
(`POST /v1/pane`). The store server is the target and no path is configured.

## Dependencies

- `chanlib`, `store` (the `pane_sessions` read only — routd declares the
  entity and nothing here does)

## Configuration

```
SLACK_BOT_TOKEN=xoxb-...      required (Bot User OAuth Token)
SLACK_SIGNING_SECRET=...      required (Slack App → Basic Information)
ROUTER_URL=http://routd:8080  required
AUTHD_URL=http://authd:8080   required (JWKS + service-token exchange)
AUTHD_SERVICE_KEY=...         required (exchanged for service:slakd)
AUTHD_SERVICE_NAME=slakd      exchange principal — the DAEMON, not CHANNEL_NAME
LISTEN_ADDR=:8080             internal HTTP listener
LISTEN_URL=http://slakd:8080  URL the router uses to reach slakd
ASSISTANT_NAME=...            shown in pane title (<name> — chat); omit → "chat"
DATA_DIR=...                  slakd's own state dir; holds the watched-message record
SLAKD_USERS_CACHE_TTL=900     users.info + conversations.info TTL (seconds)
MEDIA_MAX_FILE_BYTES=20971520 file-proxy cap
CHANNEL_NAME=slack            registration name
SLAKD_STALE_SECONDS=300       inbound silence past this ⇒ /health status=stale (still 200) + watchdog auth.test probe
SLAKD_WATCHDOG_SECONDS=60     watchdog re-check interval
SLAKD_STALE_FAIL_LIMIT=5      consecutive auth.test failures (while stale) before os.Exit(1) restart
```

`AUTHD_SERVICE_NAME` must be the daemon name (`slakd`), not `CHANNEL_NAME`
(`slack`) — authd seeds and grants `service:<daemon>`, so a mismatch 401s
every outbound call. Multi-account variants share the base principal.

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
4. **Interactivity & Shortcuts** → enable, set Request URL to
   `https://<your-host>/slack/interactivity`. A button press lands here;
   without it the buttons draw and do nothing.
5. **Basic Information** → copy _Signing Secret_ into
   `SLACK_SIGNING_SECRET`.
6. Install to Workspace → copy _Bot User OAuth Token_ (starts with
   `xoxb-`) into `SLACK_BOT_TOKEN`.
7. Invite the bot to channels: `/invite @<botname>`.
8. `arizuko create <inst> && arizuko run <inst>` — check
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

| Surface                | Verifier                                       | Failure mode                                       |
| ---------------------- | ---------------------------------------------- | -------------------------------------------------- |
| `/slack/events`        | `X-Slack-Signature` HMAC (signing secret)      | proxyd re-marshals body → HMAC breaks → 401        |
| `/slack/interactivity` | same HMAC                                      | a press is chat text; the operator gate is routd's |
| `/send`, etc.          | `chanlib.Auth` (`service:routd` ES256 token)   | router compromise → no further isolation           |
| `/files/<id>`          | `chanlib.Auth` (same bearer) + opaque cache id | id is hex of sha256 prefix; not enumerable         |
| upstream fetch         | `Authorization: Bearer $SLACK_BOT_TOKEN`       | `url_private` URLs unauthorized without token      |

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
- `specs/5/34-channel-protocol.md` — router contract
