# discd

Discord channel adapter.

## Purpose

Bridges Discord gateway/websocket events to the router. Receives messages
via discordgo websocket; sends via REST. Uses direct Discord CDN URLs for
attachments (cached via `chanlib.URLCache`).

## Responsibilities

- Authenticate with `DISCORD_BOT_TOKEN`, open gateway websocket.
- Post inbound to router with `discord:<channel_id>` JIDs.
- Handle `/send`, `/send-file`, `/typing`, `/v1/history`.
- Proxy CDN file fetches through short-token URLs.

## Entry points

- Binary: `discd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9002`)
- Router registration: `discord:` prefix, caps `send_text`, `send_file`, `typing`, `fetch_history`, `edit`, `quote`.

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

- `DISCORD_BOT_TOKEN`, `ROUTER_URL`, `CHANNEL_SECRET`
- `LISTEN_ADDR`, `LISTEN_URL`, `CHANNEL_NAME`
- `MEDIA_MAX_FILE_BYTES`, `ASSISTANT_NAME`

## Health signal

`GET /health` returns 503 when websocket disconnected. Reconnect is
handled by discordgo; persistent failure means token revoked or Discord
outage.

## Files

- `main.go` — wiring; note `b.files` must be assigned before `b.start` (documented in source).
- `bot.go` — websocket event loop, send helpers
- `server.go` — adapter handlers

## Related docs

- `specs/4/1-channel-protocol.md`
