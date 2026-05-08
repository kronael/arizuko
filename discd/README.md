# discd

Discord channel adapter.

## Purpose

Bridges Discord gateway/websocket events to the router. Receives messages
via discordgo websocket; sends via REST. Uses direct Discord CDN URLs for
attachments (cached via `chanlib.URLCache`).

## Responsibilities

- Authenticate with `DISCORD_BOT_TOKEN`, open gateway websocket.
- Post inbound to router with `discord:<channel_id>` JIDs.
- Emit synthetic `like` / `dislike` inbound events on
  `MessageReactionAdd` (raw emoji on `InboundMsg.Reaction`,
  classified via `chanlib.ClassifyEmoji`).
- Handle `/send`, `/send-file`, `/typing`, `/like`, `/edit`,
  `/delete`, `/quote`, `/v1/history` (others are 501-with-hint via
  `chanlib.UnsupportedError`).
- Proxy CDN file fetches through short-token URLs (shared
  `chanlib.URLCache`).

## Entry points

- Binary: `discd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9002`)
- Router registration: `discord:` prefix, caps `send_text`,
  `send_file`, `typing`, `fetch_history`, `like`, `edit`, `delete`,
  `quote`. Hint-only verbs (`forward`, `post`, `repost`, `dislike`)
  are still served so the agent reaches the structured hint instead of
  a generic "capability not advertised".

## Verb support

| Verb        | Status | Notes                                                                       |
| ----------- | ------ | --------------------------------------------------------------------------- |
| `send`      | native | `POST /channels/{ch}/messages`; chunks at 2000 chars; honours `reply_to`    |
| `send_file` | native | multipart upload                                                            |
| `reply`     | native | via `send` with `reply_to` (message_reference)                              |
| `edit`      | native | `PATCH /channels/{ch}/messages/{id}`                                        |
| `delete`    | native | `DELETE /channels/{ch}/messages/{id}`                                       |
| `like`      | native | emoji reaction (default 👍)                                                 |
| `quote`     | native | reply with own commentary (`message_reference` to source)                   |
| `dislike`   | hint   | redirects to `like(emoji='👎')` — Discord has one reaction primitive        |
| `forward`   | hint   | redirects to `send` with quoted text — no native forward in arizuko's model |
| `post`      | hint   | redirects to `send` — Discord channels are the post surface, no broadcast   |
| `repost`    | hint   | redirects to `send` — no retweet equivalent                                 |

## Dependencies

- `chanlib`

## Configuration

- `DISCORD_BOT_TOKEN` or `DISCORD_USER_TOKEN` (one required), `ROUTER_URL`, `CHANNEL_SECRET`
- `LISTEN_ADDR`, `LISTEN_URL`, `CHANNEL_NAME`
- `MEDIA_MAX_FILE_BYTES`, `ASSISTANT_NAME`

User mode (`DISCORD_USER_TOKEN`): authenticates as a Discord user account instead of a bot.
Skips gateway intents (not available to user tokens). Bot mode is preferred; user mode is
for self-bot use cases where a bot account cannot be created.

## Health signal

`GET /health` returns 503 when websocket disconnected. Reconnect is
handled by discordgo; persistent failure means token revoked or Discord
outage.

## Files

- `main.go` — wiring; note `b.files` must be assigned before `b.start` (documented in source).
- `bot.go` — websocket event loop, send helpers
- `server.go` — adapter handlers

## Message routing

Guild channels (`discord:<guild>/<channel>`) use verb `mention` when the bot
is @mentioned (checked via `m.Mentions` or `m.ReferencedMessage.Author`). All
other guild messages use verb `message`.

DMs (`discord:dm/<channel>`) always use verb `message`.

When `group add` registers a guild JID, it sets the route's `ImpulseConfig`
to `{"weights":{"message":0}}`. Non-mention messages accumulate as context
but never fire the agent; only `mention` (weight 100, the default) fires.

To change a guild channel to fire on every message:

```bash
# via gateway route update — set impulse_config to {}
```

## Related docs

- `specs/4/1-channel-protocol.md`
