# bskyd

Bluesky (AT Protocol) channel adapter.

## Purpose

Polls Bluesky for mentions and replies via xrpc, posts inbound to the
router. Outbound writes use `app.bsky.feed.*` records on the user's PDS.

## Responsibilities

- Authenticate with `BLUESKY_IDENTIFIER` + `BLUESKY_PASSWORD` (app password).
- Poll notifications; deliver inbound as `bluesky:<did>` JIDs.
- Serve `/send`, `/send-file`, `/post`, `/like`, `/delete`, `/quote`,
  `/repost`, `/forward`, `/dislike`, `/edit`, `/v1/history`.
- Proxy blob fetches (`/files/<did>/<cid>`) for inbound image attachments.

## Entry points

- Binary: `bskyd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9005`)
- Router registration: `bluesky:` prefix.
- Caps: `send_text`, `send_file`, `fetch_history`, `post`, `like`,
  `delete`, `quote`, `repost`.

## Dependencies

- `chanlib`

## Configuration

- `BLUESKY_IDENTIFIER`, `BLUESKY_PASSWORD`, `BLUESKY_SERVICE` (default `https://bsky.social`)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`
- `DATA_DIR`, `MEDIA_MAX_FILE_BYTES`

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

`GET /health` returns 503 when auth session is invalid.

## Files

- `main.go`, `client.go`, `server.go`

## Related docs

- `specs/4/1-channel-protocol.md`
