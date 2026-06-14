# bskyd

Bluesky (AT Protocol) channel adapter.

## Purpose

Polls Bluesky for mentions and replies via xrpc, posts inbound to the
router. Outbound writes use `app.bsky.feed.*` records on the user's PDS.

## Responsibilities

- Authenticate with `BLUESKY_IDENTIFIER` + `BLUESKY_PASSWORD` (app password).
- Poll `app.bsky.notification.listNotifications` for mentions/replies every 10s.
- Deliver inbound as `bluesky:user/<encoded-did>` JIDs (legacy `bluesky:<did>` accepted outbound).
- Serve `/send`, `/send-file`, `/post`, `/like`, `/delete`, `/quote`,
  `/repost`, `/forward`, `/dislike`, `/edit`, `/v1/history`.
- Proxy blob fetches (`GET /files/<did>/<cid>`) for inbound image attachments via `com.atproto.sync.getBlob`.

## Entry points

- Binary: `bskyd/main.go`
- Listen: `$LISTEN_ADDR` (default `:8080`)
- Router registration: `bluesky:` prefix.
- Caps: `send_text`, `send_file`, `fetch_history`, `post`, `like`,
  `delete`, `quote`, `repost`.

## Dependencies

- `chanlib`

## Configuration

- `BLUESKY_IDENTIFIER`, `BLUESKY_PASSWORD` (required; use an app password)
- `BLUESKY_SERVICE` (default `https://bsky.social`)
- `ROUTER_URL` (required)
- `CHANNEL_NAME` (default `bluesky`)
- `LISTEN_ADDR` (default `:8080`)
- `LISTEN_URL` (default `http://bluesky:8080`; used for attachment URLs)
- `DATA_DIR` (default `/srv/data/bskyd`; stores session token)
- `MEDIA_MAX_FILE_BYTES` (default `20971520` = 20MB)

## Verb support

| Verb        | Status | Notes                                                  |
| ----------- | ------ | ------------------------------------------------------ |
| `send`      | native | `app.bsky.feed.post`; with `reply_to` becomes a reply  |
| `send_file` | native | `uploadBlob` + `app.bsky.embed.images` (single image)  |
| `reply`     | native | `send` with `reply_to`                                 |
| `post`      | native | `app.bsky.feed.post`; first `media_paths[0]` embedded  |
| `like`      | native | `app.bsky.feed.like` record                            |
| `delete`    | native | `com.atproto.repo.deleteRecord`                        |
| `quote`     | native | `app.bsky.embed.record`                                |
| `repost`    | native | `app.bsky.feed.repost` record                          |
| `dislike`   | hint   | No native downvote; suggests `reply`                   |
| `forward`   | hint   | No DM forward; suggests `repost` or `quote`            |
| `edit`      | hint   | Appview rejects post edits; suggests `delete` + `post` |

`edit` stays a hint by design: `com.atproto.repo.putRecord` succeeds at
the PDS, but Bluesky's appview intentionally ignores updates to
`app.bsky.feed.post` records, so the edit never appears in the feed.
This is an application-level prohibition, not an SDK gap.

`send` posts publicly because there is no inbound DM polling pairing —
DMs would be one-way writes the agent can't follow up on. If/when DM
polling lands, switch to `chat.bsky.convo.sendMessage` proxied via
`did:web:api.bsky.chat`.

## Health signal

`GET /health` returns 503 when `authed` is false (session create/refresh both failed).

## Files

- `main.go`, `client.go`, `server.go`

## Related docs

- `specs/4/1-channel-protocol.md`
