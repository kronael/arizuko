# slakd

slakd is the Slack channel adapter. It connects one workspace to arizuko through a bot token and the HTTP Events API. It registers with routd as the owner of the `slack:` JID prefix.

## What slakd does

slakd receives Slack events from the Events API webhook, which proxyd forwards to `/slack/events`, and it sends through the Slack Web API. It has the same shape as `discd`. It registers with routd through `chanlib.RouterClient` and mounts the standard adapter surface from `chanlib.NewAdapterMux`.

- It verifies the `X-Slack-Signature` HMAC of `v0:<ts>:<body>` on every webhook. It rejects a request with a clock skew of more than 5 minutes or a signature mismatch.
- It posts inbound messages to routd with JIDs of the form `slack:<workspace>/<kind>/<id>`. The kind is `channel`, `dm` or `group`.
- It emits a synthetic `like` or `dislike` inbound message for `reaction_added`. The raw emoji name goes on `InboundMsg.Reaction`, and `chanlib.ClassifyEmoji` classifies it. An emoji that is not in the negative list classifies as `like`.
- It emits an inbound message with `Verb="join"` and the content `joined` for `member_joined_channel`.
- It derives `Verb="mention"` when the text of a group message contains `<@bot_user_id>`, and for every `app_mention` event. A channel message without a mention emits `Verb=""`. A DM emits `Verb=""`. `thread_broadcast` messages are dropped, because the original reply already arrived as a regular message event.
- It serves the outbound verbs. The table below shows the Slack call behind each verb.
- It proxies `url_private` file URLs through `GET /files/<id>` and adds the upstream header `Authorization: Bearer $SLACK_BOT_TOKEN`.
- Every outbound Web API call goes through `chanlib.DoWithRetry`. A 429 answer honours `Retry-After`, up to a cap of 30 s. A 5xx answer and a network error retry with jittered backoff of about 300 ms and 800 ms, for 3 attempts in total.

## Entry points

- The binary is `slakd/main.go`.
- The listener is `$LISTEN_ADDR`, default `:8080`, which is the container-internal port by the convention of arizuko.
- The public surface is `/slack/`. The `proxyd_routes` row in `template/services/slakd.yaml` has `auth: public` and `preserve_headers` for `X-Slack-Signature` and `X-Slack-Request-Timestamp`. proxyd forwards `/slack/events` and `/slack/interactivity` to `slakd:8080`. The row is live only when `SLACK_BOT_TOKEN` is set.
- The router registration uses the prefix `slack:` and the caps `send_text`, `send_file`, `typing`, `edit`, `like`, `delete`, `dislike`, `post`, `pin` and `buttons`. `fetch_history` is false, because slakd wires no history API. The verbs `forward`, `quote` and `repost` are hint-only and return 501 with structured hints.
- `GET /dash/slakd/` is the operator page. `chanlib.NewAdapterMux` mounts it from the one renderer in `chanlib/dash.go`. The path uses the daemon name from `AUTHD_SERVICE_NAME` and never the platform name.
- `POST /v1/pane/prompts` and `POST /v1/pane/title` serve the assistant pane, as described below.

## Verb support

| Verb               | Status             | Notes                                                                     |
| ------------------ | ------------------ | ------------------------------------------------------------------------- |
| `send`             | native             | `chat.postMessage`; `thread_ts` from the thread fields, below             |
| `send_file`        | native             | `files.getUploadURLExternal`, a PUT, then `completeUploadExternal`        |
| `edit`             | native             | `chat.update`                                                             |
| `delete`           | native             | `chat.delete`                                                             |
| `like`             | native             | `reactions.add`; the emoji name comes from `Reaction`, default `thumbsup` |
| `dislike`          | native             | `reactions.add` with `thumbsdown`, as a like with the emoji `thumbsdown`  |
| `post`             | native             | maps to `send` on a channel JID                                           |
| `pin`, `unpin`     | native             | `pins.add`, `pins.remove`; unpin of all uses `pins.list`                  |
| `forward`          | hint               | redirects to `send` that quotes the source                                |
| `quote`            | hint               | redirects to `send(reply_to=<ts>)` to thread under the source             |
| `repost`           | hint               | redirects to `send`                                                       |
| `pane_set_prompts` | native (pane only) | `assistant.threads.setSuggestedPrompts` after the next Send               |
| `pane_set_title`   | native (pane only) | `assistant.threads.setTitle` after the next Send                          |

`Typing` is native too: it adds and removes an eyes reaction, as the next sections describe. `send_text` takes buttons. A button press arrives at `/slack/interactivity`.

## Replies thread on the trigger

slakd sets `thread_ts` from `thread_id`, the thread in which a turn already runs. Otherwise it uses `thread_root`, the trigger message that routd asks it to root a new thread on (`Send` in `bot.go`). `reply_to` never sets `thread_ts`. It often names an earlier bot row, and a thread rooted there hides the reply in a thread that the user never started.

routd sends `thread_root` only for a `reply` to the trigger chat. The turn must not be in a thread yet, and the `thread_replies` setting of the group must allow it. When the setting is unset, it follows whether the chat has several users (`replyThreadRoot` in `routd/turns.go`). `send` never roots a new thread. When Slack cannot find the thread, slakd posts the reply once more at the channel root, so the reply is not lost. A permanent caller-side error from Slack, such as a missing or archived channel, returns `chanlib.ErrInvalidRequest` as a 400. It is not a 502 that the agent retries.

## Assistant pane

The "Agents & AI Apps" feature of Slack gives the bot a dedicated sidebar pane. slakd implements the full lifecycle when the app has the `assistant:write` scope and subscribes to `assistant_thread_started` and `assistant_thread_context_changed`.

- `assistant_thread_started` upserts a row in `pane_sessions`, keyed by `(team_id, user_id, thread_ts)`, through `POST /v1/pane` of routd. The row lives in the store of routd and not in the memory of slakd. So a pane survives a restart of slakd, and routd and the agent read it without asking slakd. The call sets the pane title (`<ASSISTANT_NAME> — chat`), seeds three default suggested prompts, and dispatches a synthetic `pane_open` inbound message, so the agent gets a turn.
- `assistant_thread_context_changed` updates `pane_sessions.context_jid` with the workspace channel that the user is viewing. It starts no synthetic turn, because context drift is not an action of the user.
- `Typing()` adds a 👀 reaction to the newest inbound message that started a turn for the JID (on=true). It removes the reaction from that same message (on=false). The state belongs to `chanlib.AckMark`. The adapter store records the mark. A boot sweep clears a mark that is older than `chanlib.DefaultTypingMaxTTL`, and a mark that a restart or a graceful shutdown left is removed when the turn ends. The same path serves pane and regular channels. slakd uses no `conversations.typing`, because it is RTM-only and discontinued, and no `assistant.threads.setStatus`. It does nothing when no inbound message is known.
- `Send()` into a pane channel drains the pending prompts and title that the MCP tools below staged. It fires `assistant.threads.setSuggestedPrompts` and `setTitle` after `chat.postMessage` succeeds.

MCP-driven control, one-shot per outbound message:

- `POST /v1/pane/prompts {jid, prompts}` is gated by the channel secret. It stages prompts on the bot for the next Send into that pane. It returns 404 when the jid does not map to an open pane, and `chanreg` maps that answer to `chanlib.ErrUnsupported`.
- `POST /v1/pane/title {jid, title}` has the same shape and stages a one-shot title.

routd reaches both endpoints through `SetSuggestions` and `SetName` of `chanreg.HTTPChannel`. slakd is the only adapter that implements them.

Pane reads open the owner of routd (`store.OpenOwner(store.OwnerRoutd)`) for `pane_sessions`. routd owns that table, and pane writes go over HTTP (`POST /v1/pane`). The store server is the target, and no path is configured.

## Dependencies

`chanlib`, and `store` for the read of `pane_sessions` only. routd declares the entity, and nothing in slakd does.

## Configuration

```
SLACK_BOT_TOKEN=xoxb-...      required (Bot User OAuth Token)
SLACK_SIGNING_SECRET=...      required (Slack App → Basic Information)
ROUTER_URL=http://routd:8080  required
AUTHD_URL=http://authd:8080   JWKS and service-token exchange; unset leaves the adapter auth open (local development)
AUTHD_SERVICE_KEY=...         exchanged for service:slakd
AUTHD_SERVICE_NAME=slakd      exchange principal, the DAEMON name and not CHANNEL_NAME; also names the /dash/ path
LISTEN_ADDR=:8080             internal HTTP listener
LISTEN_URL=http://slakd:8080  URL the router uses to reach slakd
ASSISTANT_NAME=...            shown in the pane title (<name> — chat); unset gives "chat"
DATA_DIR=...                  state dir of slakd; holds the record of watched messages (default /srv/app/home/store/slakd)
SLAKD_USERS_CACHE_TTL=900     users.info and conversations.info TTL, in seconds
MEDIA_MAX_FILE_BYTES=20971520 cap of the file proxy
CHANNEL_NAME=slack            registration name
SLAKD_STALE_SECONDS=300       inbound silence past this makes the watchdog probe auth.test
SLAKD_WATCHDOG_SECONDS=60     watchdog re-check interval
SLAKD_STALE_FAIL_LIMIT=5      consecutive auth.test failures, while stale, before os.Exit(1) restarts the daemon
```

`AUTHD_SERVICE_NAME` must be the daemon name (`slakd`) and not `CHANNEL_NAME` (`slack`). authd seeds and grants `service:<daemon>`, so a mismatch gives a 401 on every outbound call. Multi-account variants share the base principal.

## Operator runbook for one workspace

1. Create a Slack App at <https://api.slack.com/apps> with "From scratch".
2. Under **OAuth & Permissions**, add these Bot Token Scopes: `channels:history`, `channels:read`, `groups:history`, `groups:read`, `im:history`, `im:read`, `mpim:history`, `mpim:read`, `chat:write`, `chat:write.public`, `reactions:read`, `reactions:write`, `files:read`, `files:write` and `users:read`. `reactions:read` is required for the delivery of `reaction_added` events, even though slakd uses only `reactions:write` outbound. slakd also calls `pins.add`, `pins.remove` and `pins.list`, and `assistant.threads.*`. Slack documents the scopes `pins:write`, `pins:read` and `assistant:write` for those methods. This pass did not measure them against a live workspace.
3. Under **Event Subscriptions**, enable events and set the Request URL to `https://<your-host>/slack/events`. proxyd handles TLS and forwards the request verbatim to slakd. Subscribe to these bot events: `message.channels`, `message.groups`, `message.im`, `message.mpim`, `app_mention`, `reaction_added` and `member_joined_channel`. For the assistant pane, also subscribe to `assistant_thread_started` and `assistant_thread_context_changed`. File attachments arrive with the `message.*` events, so do not subscribe to `file_shared`.
4. Under **Interactivity & Shortcuts**, enable interactivity and set the Request URL to `https://<your-host>/slack/interactivity`. A button press lands there. Without it the buttons draw and do nothing.
5. Under **Basic Information**, copy the _Signing Secret_ into `SLACK_SIGNING_SECRET`.
6. Install the app to the workspace. Copy the _Bot User OAuth Token_, which starts with `xoxb-`, into `SLACK_BOT_TOKEN`.
7. Invite the bot to channels with `/invite @<botname>`.
8. Run `arizuko create <inst> && arizuko run <inst>`. Check that `GET /health` returns 200 once `auth.test` succeeds.

## Health signal

`GET /health` returns 503 when `auth.test` has not succeeded, for example when the token is revoked, the network is down or Slack is down. The body is `{"status":"disconnected"}`. `auth.test` is the honest liveness signal. A `healthProbe` goroutine runs it every 60 s, and the watchdog runs it whenever inbound is stale. The result flips `isConnected()`, and with it the 503. A silently paused event subscription that leaves the token valid is caught the same way, as the watchdog below describes.

**Inbound staleness is informational and is not a 503.** `GET /health` reports `{"status":"stale","stale_seconds":N}` with status 200 when the last inbound message is older than 5 minutes. That threshold is the `defaultStaleThreshold` of `chanlib` and `SLAKD_STALE_SECONDS` does not change it. A quiet workspace is normal and must not be marked unhealthy. A 503 on stale-but-connected would restart the container of a workspace that is only quiet. Death detection lives entirely in `auth.test` and never in silence.

A watchdog goroutine runs on the `SLAKD_WATCHDOG_SECONDS` cadence. When inbound is older than `SLAKD_STALE_SECONDS`, it runs `auth.test` again. That is the only recovery that slakd can perform itself, because there is no socket to reconnect. Silence alone never restarts the container. Stale inbound with a passing `auth.test` counts as merely quiet. A restart would drop the POSTs that Slack has in flight. Slack disables a subscription after repeated failed deliveries, so a restart on silence would cause the failure that it tries to cure. The watchdog calls `os.Exit(1)` only when the channel is stale and `auth.test` fails for `SLAKD_STALE_FAIL_LIMIT` consecutive ticks. The policy `restart: on-failure` in `template/services/slakd.yml` then re-creates the container. This is the backstop for a dead subscription. A single inbound message, or a single passing `auth.test`, resets the streak. The watchdog logs a warning only when the state changes to quiet and when it changes back. So a quiet workspace does not fill the log.

## Threat model

| Surface                | Verifier                                            | Failure mode                                                 |
| ---------------------- | --------------------------------------------------- | ------------------------------------------------------------ |
| `/slack/events`        | `X-Slack-Signature` HMAC (signing secret)           | proxyd re-marshals the body, the HMAC breaks, 401            |
| `/slack/interactivity` | the same HMAC                                       | a press is chat text; the operator gate is routd's           |
| `/send`, and others    | `chanlib.Auth` (`service:routd` ES256 token)        | a compromised router gets no further isolation               |
| `/files/<id>`          | `chanlib.Auth` (same bearer) and an opaque cache id | the id is hex of a sha256 prefix, so it cannot be enumerated |
| upstream fetch         | `Authorization: Bearer $SLACK_BOT_TOKEN`            | `url_private` URLs are unauthorized without the token        |

proxyd must forward `X-Slack-Signature`, `X-Slack-Request-Timestamp` and the raw body unmodified. Any re-marshal invalidates the HMAC.

## Files

- `main.go` loads the env and wires `chanlib.Run`. It sets `b.files = srv.files` before `b.start` to avoid a race with inbound events.
- `bot.go` dispatches Events API events, verifies signatures, holds the Slack Web API client and the TTL caches.
- `server.go` serves `/slack/events`, `/slack/interactivity`, the `/files/<id>` proxy, the pane endpoints and the chanlib adapter mux.
- `jid.go` parses and formats `slack:<workspace>/<kind>/<id>` strictly.

## Out of scope

- OAuth install. The install is manual, as the runbook above shows.
- Socket Mode. slakd supports HTTP webhooks through proxyd only.
- Enterprise Grid, slash commands, modals, the home tab, Block Kit beyond the buttons of `send_text`, and user tokens.
- A custom emoji as a dislike. That needs a per-workspace `emoji.list` mapping. An unknown emoji falls through to the `like` default of `ClassifyEmoji`, and the agent gets the raw name on `Reaction`.

## Related docs

- [`chanlib/README.md`](../chanlib/README.md) describes the channel protocol that every adapter speaks.
