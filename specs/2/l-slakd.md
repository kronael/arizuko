---
status: spec
---

# slakd — Slack channel adapter (bot-token, v1)

Slack workspace adapter. Same shape as `discd` / `teled`: registers
with `gated` via `chanlib.RouterClient`, exposes `/send`, `/like`,
`/delete`, `/upload`, `/health`. Lives at `slakd/`; goes in
`template/services/slakd.toml`.

## What ships in v1 (bot-token, single-workspace)

One `xoxb-` token, one workspace. Multi-workspace = future spec.

- HTTP Events API on `SLAKD_PORT` (internal). Public reachability via
  `proxyd` — same pattern as `onbod` and `webd`. proxyd config gets
  `SLAKD_ADDR=http://slakd:8080` + a `/slack/*` route forwarding to it;
  no other public-URL machinery in slakd itself.
- URL-verification handshake (`type=url_verification` → echo `challenge`).
- Signing: `X-Slack-Signature` HMAC of `v0:<ts>:<body>` using
  `SLACK_SIGNING_SECRET`; reject if `|ts - now| > 5 min`. proxyd MUST
  pass body bytes and `X-Slack-Signature` / `X-Slack-Request-Timestamp`
  headers verbatim (no re-marshal, no TLS re-sign) — else slakd can't verify.
- Inbound: `message.channels`, `message.groups`, `message.im`,
  `message.mpim`, `reaction_added`/`removed`, `member_joined_channel`
  (`verb=join`), `file_shared`. NOT `app_mention` — Slack fires it
  alongside `message.*`; mirror `discd/bot.go:143` and set
  `Verb="mention"` when the text contains `<@Uxxx>` matching
  `auth.test`'s `bot_user_id`; otherwise `Verb=""` (default).
- Outbound (Web API, `Bearer xoxb-...`): `chat.postMessage` (+`thread_ts`),
  `chat.update`, `chat.delete`, `reactions.add`/`remove`,
  `files.getUploadURLExternal` + `files.completeUploadExternal`
  (`files.upload` deprecated 2025-05).
- 429: respect `Retry-After` (same as discd / `ant/CLAUDE.md` tool
  discipline). Per-method tiers are constants — log, don't compute.

## JID and threading

JID: `slack:<workspace>/<kind>/<id>` — same kind-segment shape as
Telegram (`telegram:user/<id>`, `telegram:group/<id>`). Kind is `channel`
(public/private),
`dm` (direct message), or `group` (legacy mpim). Workspace ID and
channel ID kept verbatim from Slack (paste-back from Slack URLs works:
`T012ABCD`, `C0HJKL456`, etc.). Multi-workspace: one slakd daemon per
workspace, JID disambiguates. `IsGroup`: kind=`dm` → false; else →
true. Registered prefix: `slack:`. Examples:
`slack:T012ABCD/channel/C0HJKL456`, `slack:T012ABCD/dm/D0XY9876`.

**Threading**: slakd sets `Topic = thread_ts` on every inbound message
that's in a thread. Top-level messages get `Topic=""`. Reconstruction
matches the root by `ID` and replies by `Topic` — same shape as Telegram
forum topics and Discord threads, handled by `get_thread` without
slakd-specific code. Outbound: agent's `replyTo` resolves to the parent's
Topic (or ID if parent is the root); slakd passes that as `thread_ts` to
`chat.postMessage`. Topic is opaque — never compared across platforms.

## Verbs

| Verb        | Method                                        |
| ----------- | --------------------------------------------- |
| `send`      | `chat.postMessage`                            |
| `reply`     | `chat.postMessage` + `thread_ts`              |
| `like`      | `reactions.add` (emoji from `Reaction` field) |
| `dislike`   | `reactions.add` w/ `👎` per dislike-via-like  |
| `delete`    | `chat.delete`                                 |
| `edit`      | `chat.update`                                 |
| `send_file` | `files.getUploadURLExternal` + complete       |
| `post`      | maps to `send` on a channel JID               |

DMs (`message.im`) and non-mentioned channel messages emit `verb=""`
(default `message`); mentions and threads ride in `Topic` or text, not
the verb. Mirrors `discd/bot.go:147`.

## Reactions, files, caches

`reaction_added` → `InboundMsg{Verb: ClassifyEmoji(name), Content: name,
Reaction: name, ReplyTo: item.ts}`. Names arrive without colons
(`thumbsup`); `reaction_removed` not emitted in v1. Workspace-custom
emoji (`:partyparrot:`) lack Unicode codepoint, fall through
`ClassifyEmoji`'s unknown→like default; `Reaction` carries the NAME —
agent gets name + like verb, enough for most flows.

Files: standard `chanlib.URLCache` + `GET /files/<id>` proxy pattern
(same shape as `discd` and `teled` — `chanlib.Auth(ChannelSecret, …)`
middleware in front, `chanlib.ProxyFile` for streaming). Only Slack-
specific bit: upstream fetch adds `Authorization: Bearer $SLACK_BOT_TOKEN`
(Discord uses time-signed CDN URLs, Telegram embeds the token in the URL
path; Slack uses a request header). Agent fetches the stable
`/files/<id>` URL without credentials; Whisper path identical to other
adapters.

`users.info` (15 min TTL) → `SenderName`; `conversations.info` (15 min
TTL) → `ChatName`. `auth.test` returns `bot_user_id`; slakd skips
inbound where `event.user == bot_user_id` AND events with only `bot_id`
set. Liveness = `auth.test`; `/health` returns 503 on auth failure.

## Env vars

```
SLACK_BOT_TOKEN=xoxb-...     required
SLACK_SIGNING_SECRET=...     required
SLAKD_PORT=8080              internal HTTP listener (proxyd → /slack/*)
SLAKD_USERS_CACHE_TTL=900    seconds
```

proxyd env gets `SLAKD_ADDR=http://slakd:8080` and routes `/slack/*` to it
(parallel to `WEBD_ADDR`, `ONBOD_ADDR`). Slack's Event Subscription URL =
`https://<host>/slack/events`. No `SLAKD_PUBLIC_URL` needed — proxyd
handles the public surface.

## Acceptance

- Operator creates Slack App, sets bot token + signing secret in `.env`,
  subscribes events to `https://<host>/slack/events` (proxyd → slakd).
- `arizuko create slk && arizuko run slk`; `/health` 200; bot in `#test`;
  agent replies in-channel and in 1:1 DM.
- Thread round-trip: reply lands as `Topic`; `get_thread chat_jid:=slack:T<ws>/channel/<chan>
topic:=<thread_ts>` returns the slice; `send_reply` with `replyTo` of
  a thread message posts under the same thread.
- `:thumbsdown:` → inbound `verb="dislike"`, `Reaction="thumbsdown"`.
- File round-trip (inbound PDF via slakd `/files/` proxy; outbound PNG
  via `files.getUploadURLExternal` + complete). Forged signature → 401.

## Out of scope (deferred)

- **OAuth install** — manual install only; per-workspace bot install
  runbook lives in `slakd/README.md` (parallel to `teled/README.md`).
  Multi-workspace token store = separate spec.
- **Socket Mode** — not pursued; HTTP webhooks via proxyd is the
  permanent default (matches onbod / webd / dashd pattern, no second
  transport to maintain).
- **Enterprise Grid**, **slash commands / shortcuts / modals / home
  tab / Block Kit**, **user token** (`xoxp-`) — all separate specs.
- **Custom-emoji-as-dislike** — needs per-workspace `emoji.list`
  mapping; v1 falls through to like default.

## Decisions

- Signing-secret rotation: startup only (matches mastd). SIGHUP reload
  not pursued.
- File uploads post as the bot, not the agent persona — accepted for v1.
