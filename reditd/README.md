# reditd

Reddit channel adapter.

## Purpose

Polls configured subreddits + inbox for new posts/mentions via the Reddit
API, posts inbound to the router. Outbound uses Reddit's comment/submit
API.

## Responsibilities

- Script-app OAuth with `REDDIT_CLIENT_ID/SECRET` + username/password.
- Poll subreddits in `REDDIT_SUBREDDITS` + inbox; cursors persisted in `cursors.json`.
- Post inbound as `reddit:<thing_id>` JIDs.
- Handle `/send`, `/v1/history`.

## Entry points

- Binary: `reditd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9006`)
- Router registration: `reddit:` prefix, caps `send_text`, `fetch_history`.

## Dependencies

- `chanlib`

## Configuration

- `REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET`, `REDDIT_USERNAME`, `REDDIT_PASSWORD`
- `REDDIT_SUBREDDITS` (comma-separated)
- `REDDIT_USER_AGENT` (default `arizuko/1.0`)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`, `DATA_DIR`

## Limitations

- Inbound upvotes/downvotes are not mapped to the `like` verb. Reddit's
  API does not surface individual vote events via subscription: `/message/
inbox.json` delivers replies/mentions/PMs only, and `/r/<sr>/new.json`
  delivers new submissions. Vote state is only exposed as aggregate
  `score`/`ups`/`likes` counts on polled things, not as discrete events.
  Inbound verbs remain `message`, `reply`, or `post` (`client.go:340`).
- Outbound file/media upload is not implemented. `post`, `send_reply`,
  and `delete` are wired; `send_file` is not.
- Outbound `like` / `dislike` map to `/api/vote dir=±1` on the supplied
  thing name (`t1_<id>` for comments, `t3_<id>` for posts).

## Health signal

`GET /health` returns 503 when OAuth token refresh is failing or rate
limit is saturated.

## Files

- `main.go`, `client.go`, `server.go`
- `cursors.json` — per-subreddit poll cursors (read/written at runtime)

## Related docs

- `specs/4/1-channel-protocol.md`
