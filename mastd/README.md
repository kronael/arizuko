# mastd

Mastodon channel adapter.

## Purpose

Streams notifications from a Mastodon instance via the user streaming API,
posts inbound to the router. Outbound uses the Mastodon REST API.

## Responsibilities

- Authenticate with `MASTODON_ACCESS_TOKEN` against `MASTODON_INSTANCE_URL`.
- Stream notifications; filter to mentions/DMs.
- Post inbound as `mastodon:<account>` JIDs.
- Handle `/send`, `/v1/history`.

## Entry points

- Binary: `mastd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9004`)
- Router registration: `mastodon:` prefix, caps `send_text`, `fetch_history`.

## Dependencies

- `chanlib`

## Configuration

- `MASTODON_INSTANCE_URL`, `MASTODON_ACCESS_TOKEN`
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`
- `MEDIA_MAX_FILE_BYTES`, `MASTODON_FILE_CACHE_SIZE` (default 1000)

## Limitations

- Outbound file/media upload is not implemented. `post`, `send_reply`,
  and `like` are wired; `send_file` is not.

## Health signal

`GET /health` flips to 503 when the notification stream drops. Mastodon
instances occasionally reset the stream — adapter reconnects, but
extended 503 means instance issues.

## Files

- `main.go` — wiring
- `client.go` — stream consumer, REST posting
- `server.go` — adapter handlers

## Related docs

- `specs/4/1-channel-protocol.md`
