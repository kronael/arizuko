# reditd

Reddit channel adapter.

## Purpose

Polls configured subreddits + inbox for new posts/mentions via the Reddit
API, posts inbound to the router. Outbound uses Reddit's comment/submit
API.

## Responsibilities

- Script-app OAuth with `REDDIT_CLIENT_ID/SECRET` + username/password.
- Poll subreddits in `REDDIT_SUBREDDITS` + inbox; cursors persisted in `cursors.json`.
- Post inbound as `reddit:subreddit/<sr>` or `reddit:user/<username>` JIDs.
- Handle `/send`, `/post`, `/like`, `/dislike`, `/delete`, `/edit`, `/v1/history`, `/files/<id>`.

## Entry points

- Binary: `reditd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9006`)
- Router registration: `reddit:` prefix, caps `send_text`, `fetch_history`,
  `post`, `like`, `dislike`, `delete`, `edit`.

## Verb coverage

| Verb        | Reddit primitive                                                | Status                                                 |
| ----------- | --------------------------------------------------------------- | ------------------------------------------------------ |
| `send`      | `/api/comment` (with reply); else `/api/submit` to user profile | native                                                 |
| `send_file` | media-asset upload (3-step)                                     | not implemented                                        |
| `reply`     | `/api/comment thing_id=...`                                     | native (via `send` with `reply_to`)                    |
| `post`      | `/api/submit kind=self`                                         | native                                                 |
| `like`      | `/api/vote dir=1`                                               | native                                                 |
| `dislike`   | `/api/vote dir=-1`                                              | native (only platform with true downvote)              |
| `delete`    | `/api/del`                                                      | native                                                 |
| `forward`   | none                                                            | hint → use `post` with attribution                     |
| `quote`     | none (markdown `> ` only)                                       | hint → `post(content="...\n\n> <quote>")`              |
| `repost`    | crosspost API (unstable)                                        | hint → `post(content=..., url=<original>)`             |
| `edit`      | `/api/editusertext`                                             | native (self-posts + comments; link submissions error) |

## Dependencies

- `chanlib`

## Configuration

- `REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET`, `REDDIT_USERNAME`, `REDDIT_PASSWORD` (required)
- `REDDIT_SUBREDDITS` (comma-separated, optional)
- `REDDIT_USER_AGENT` (default `arizuko/1.0`)
- `REDDIT_POLL_INTERVAL` (default `5m`)
- `CHANNEL_NAME` (default `reddit`)
- `MEDIA_MAX_FILE_BYTES` (default `20971520` / 20 MiB)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR` (default `:9006`), `LISTEN_URL` (default `http://reditd:9006`), `DATA_DIR` (default `/srv/data/reditd`)

## Limitations

- Inbound upvotes/downvotes are not mapped to the `like` verb. Reddit's
  API does not surface individual vote events: `/message/inbox.json`
  delivers replies/mentions/PMs only, and `/r/<sr>/new.json` delivers new
  submissions. Vote state is only exposed as aggregate `score`/`ups`/`likes`
  counts on polled things, not discrete events. Inbound verbs remain
  `message`, `reply`, or `post`.
- Outbound file/media upload is not implemented. `send_file` returns
  `Unsupported` hint.
- Outbound `like`/`dislike` map to `/api/vote dir=±1` on the supplied thing
  name (`t1_<id>` for comments, `t3_<id>` for posts).
- First poll after fresh start or new subreddit skips delivery (advances
  cursor only) to avoid replaying history.

## Health signal

`GET /health` returns 503 when the last successful poll is older than 15
minutes (`pollStaleAfter`). Fresh instance is healthy after initial OAuth
succeeds.

## Files

- `main.go`, `client.go`, `server.go`
- `cursors.json` — per-subreddit poll cursors (read/written at runtime)

## Related docs

- `specs/4/1-channel-protocol.md`
