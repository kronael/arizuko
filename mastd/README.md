# mastd

Mastodon channel adapter.

## Purpose

Streams notifications from a Mastodon instance via the user streaming API,
posts inbound to the router. Outbound uses the Mastodon REST API.

## Responsibilities

- Authenticate with `MASTODON_ACCESS_TOKEN` against `MASTODON_INSTANCE_URL`.
- Stream user notifications; handle mention, reply, favourite, reblog, follow.
- Post inbound as `mastodon:account/<id>` JIDs (legacy `mastodon:<id>` accepted).
- Mount `/send`, `/send-file`, `/send-voice`, `/typing`, `/post`, `/like`,
  `/delete`, `/forward`, `/quote`, `/repost`, `/dislike`, `/edit`, `/pin`,
  `/unpin`, `/health`, `/v1/history`, `/files/`.

## Entry points

- Binary: `mastd/main.go`
- Listen: `$LISTEN_ADDR` (default `:8080`)
- Router registration: `mastodon:` prefix, caps `send_text`,
  `fetch_history`, `post`, `like`, `delete`, `repost`, `edit`.

## Dependencies

- `chanlib`

## Configuration

- `MASTODON_INSTANCE_URL`, `MASTODON_ACCESS_TOKEN`
- `ROUTER_URL`, `LISTEN_ADDR` (default `:8080`), `LISTEN_URL` (default `http://mastd:8080`)
- `CHANNEL_NAME` (default `mastodon`)
- `MEDIA_MAX_FILE_BYTES` (default 20MB), `MASTODON_FILE_CACHE_SIZE` (default 1000)

## Verb support

| Verb      | Status      | Mastodon API / hint                                                     |
| --------- | ----------- | ----------------------------------------------------------------------- |
| send      | native      | `POST /api/v1/statuses` (currently public; see DM gap below)            |
| send_file | unsupported | media upload not implemented (`NoFileSender`)                           |
| reply     | native      | `POST /api/v1/statuses` with `in_reply_to_id` via `SendRequest.ReplyTo` |
| post      | native      | `POST /api/v1/statuses` (text-only; rejects `media_paths`)              |
| like      | native      | `POST /api/v1/statuses/{id}/favourite`                                  |
| delete    | native      | `DELETE /api/v1/statuses/{id}`                                          |
| repost    | native      | `POST /api/v1/statuses/{id}/reblog`                                     |
| edit      | native      | `PUT /api/v1/statuses/{id}`                                             |
| forward   | hint        | no primitive; suggests `repost` or text relay                           |
| quote     | hint        | rejected as anti-feature on Mastodon; suggests `post` with permalink    |
| dislike   | hint        | no native downvote; suggests textual `reply`                            |
| pin       | unsupported | not implemented                                                         |
| unpin     | unsupported | not implemented                                                         |

## Known gaps

- **DM semantics**: `Send` posts publicly. To DM the author of a
  `mastodon:<account_id>` JID, the adapter needs (a) the `acct` handle
  to @-mention and (b) `Toot.Visibility = "direct"`. JIDs currently
  store only the numeric account ID.
- **PostRequest fields**: Mastodon-specific knobs (content warning,
  language, visibility) are not on `chanlib.PostRequest`; the adapter
  silently drops anything beyond `content` + `media_paths`. Adding
  these is a cross-cutting change to the request struct — flagged
  for separate work.
- **Media upload on `post`**: returns an error if `media_paths` is
  set. No `POST /api/v2/media` flow yet.

## Health signal

`GET /health` flips to 503 when the notification stream drops. Mastodon
instances occasionally reset the stream — adapter reconnects, but
extended 503 means instance issues.

## Files

- `main.go` — wiring, config
- `client.go` — stream consumer, REST posting, verb handlers
- `server.go` — HTTP mux setup

## Related docs

- `specs/4/1-channel-protocol.md`
