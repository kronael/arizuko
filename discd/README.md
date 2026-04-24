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
- Router registration: `discord:` prefix, caps `send_text`, `send_file`, `typing`, `fetch_history`.

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
