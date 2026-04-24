# bskyd

Bluesky (AT Protocol) channel adapter.

## Purpose

Polls Bluesky for mentions and replies via xrpc, posts inbound to the
router. Outbound posts use `app.bsky.feed.post`.

## Responsibilities

- Authenticate with `BLUESKY_IDENTIFIER` + `BLUESKY_PASSWORD` (app password).
- Poll notifications / timeline; post inbound as `bluesky:<did>` JIDs.
- Handle `/send`, `/v1/history`.

## Entry points

- Binary: `bskyd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9005`)
- Router registration: `bluesky:` prefix, caps `send_text`, `fetch_history`.

## Dependencies

- `chanlib`

## Configuration

- `BLUESKY_IDENTIFIER`, `BLUESKY_PASSWORD`, `BLUESKY_SERVICE` (default `https://bsky.social`)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`
- `DATA_DIR`, `MEDIA_MAX_FILE_BYTES`

## Health signal

`GET /health` returns 503 when auth session is invalid or poll is failing.

## Files

- `main.go`, `client.go`, `server.go`

## Related docs

- `specs/4/1-channel-protocol.md`
