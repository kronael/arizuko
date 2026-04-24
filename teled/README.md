# teled

Telegram channel adapter.

## Purpose

Bridges Telegram Bot API to the router. Long-polls Telegram for updates,
posts inbound messages to `/v1/messages`, handles `/send`, `/send-file`,
`/typing` for outbound. Serves `/files/<fileID>` as a CDN proxy because
Telegram bot-token URLs are short-lived.

## Responsibilities

- Authenticate with `TELEGRAM_BOT_TOKEN`, long-poll `getUpdates`.
- Post inbound to router with `telegram:<chat_id>` JIDs.
- Serve adapter surface via `chanlib.Run` + `NewAdapterMux`.
- Persist poll offset under `$DATA_DIR/teled-offset-<name>`.
- Proxy Telegram file downloads through `/files/<fileID>`.

## Entry points

- Binary: `teled/main.go`
- Listen: `$LISTEN_ADDR` (default `:9001`)
- Router registration: `telegram:` prefix, caps `send_text`, `send_file`, `typing`, `fetch_history`.

## Dependencies

- `chanlib` (Run, RouterClient, AdapterMux, URLCache, env helpers)

## Configuration

- `TELEGRAM_BOT_TOKEN`, `ROUTER_URL`, `CHANNEL_SECRET`
- `LISTEN_ADDR`, `LISTEN_URL`, `CHANNEL_NAME`
- `DATA_DIR`, `MEDIA_MAX_FILE_BYTES`, `ASSISTANT_NAME`

## Health signal

`GET /health` returns 200 when connected to Telegram AND inbound activity
in the last 5 min (`chanlib.handleHealth`); 503 `{status:"disconnected"}`
when long-poll is failing.

## Files

- `main.go` — config, wiring via `chanlib.Run`
- `bot.go` — long-poll, file download, connection state
- `server.go` — adapter handlers for send/typing/files

## Related docs

- `specs/4/1-channel-protocol.md`
- `EXTENDING.md` (adding a channel)
