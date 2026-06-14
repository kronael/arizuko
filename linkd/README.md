# linkd

LinkedIn channel adapter (partial native).

## Purpose

LinkedIn adapter built on the v2 community-management API. Provides
native UGC posting, liking, deleting, resharing, and commenting (via
the existing `Send`/comment path). Verbs without a clean LinkedIn
primitive return structured `Unsupported` errors with concrete
alternatives.

## Verb coverage

| Verb            | Native? | Endpoint / behaviour                                                                                                                    |
| --------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `send`          | yes     | `POST /v2/socialActions/{urn}/comments` when ChatJID is a post URN; else `POST /v2/ugcPosts` (gated by `LINKEDIN_AUTO_PUBLISH=true`)    |
| `reply`         | yes     | Same as `send` with `parentComment` set (within a post's comments)                                                                      |
| `post`          | yes     | `POST /v2/ugcPosts` with `ShareContent`                                                                                                 |
| `like`          | yes     | `POST /v2/socialActions/{urn}/likes`                                                                                                    |
| `delete`        | yes     | `DELETE /v2/ugcPosts/{urn}` (own posts)                                                                                                 |
| `repost`        | yes     | `POST /v2/ugcPosts` with `referenceUgcPost`                                                                                             |
| `fetch_history` | yes     | Comments on a post URN                                                                                                                  |
| `send_file`     | hint    | UGC media upload (`/assets registerUpload` + binary PUT) is not wired up; suggests using a URL in `post` content for auto link previews |
| `forward`       | hint    | LinkedIn DM forward needs partner-only messaging permissions; suggests `repost` or `post` with permalink                                |
| `quote`         | hint    | LinkedIn has no distinct quote primitive — `repost` is the share-with-commentary verb; suggests using `repost` or `post` with permalink |
| `dislike`       | hint    | LinkedIn has no native downvote; suggests `reply`                                                                                       |
| `edit`          | hint    | UGC post edit requires the versioned `/rest/posts` PARTIAL_UPDATE flow (not wired up); suggests `delete` + `post`                       |

## Responsibilities

- Register as `linkedin:` prefix.
- Caps: `send_text`, `fetch_history`, `post`, `like`, `delete`, `repost`.
- Poll own shares (`/v2/shares?q=owners`) + comments on each post
  (`/v2/socialActions/{urn}/comments`) at `LINKEDIN_POLL_INTERVAL`
  (default `300s`).
- Publish via the UGC endpoint when `LINKEDIN_AUTO_PUBLISH=true`.
- Dedup inbound comments via `DATA_DIR/linkd-state-<name>.json` (seen
  map capped at 5000 entries).

## Entry points

- Binary: `linkd/main.go`
- Listen: `$LISTEN_ADDR` (code default `:9010`, container `:8080`)
- Listen URL: `$LISTEN_URL` (code default `http://linkd:9010`, container `http://linkd:8080`)
- Router registration: `linkedin:` prefix.

## Dependencies

- `chanlib`

## Configuration

- `CHANNEL_NAME` (default `linkedin`)
- `LINKEDIN_CLIENT_ID`, `LINKEDIN_CLIENT_SECRET` (required)
- `LINKEDIN_ACCESS_TOKEN`, `LINKEDIN_REFRESH_TOKEN` (optional — persisted in `DATA_DIR/linkd-state-<name>.json`)
- `LINKEDIN_API_BASE` (default `https://api.linkedin.com`)
- `LINKEDIN_OAUTH_BASE` (default `https://www.linkedin.com`)
- `LINKEDIN_POLL_INTERVAL` (default `300s`)
- `LINKEDIN_AUTO_PUBLISH` (default `false` — gate for new posts/reshares)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`, `DATA_DIR`

OAuth scopes required: `r_liteprofile`, `w_member_social`. Reshares
require the same `w_member_social` scope as posts. Company-page
posting and DM messaging both need separate LinkedIn partner
approvals and are intentionally out of scope here.

## Endpoints

Standard adapter surface (via `chanlib.NewAdapterMux`):

- `GET /health` — 503 when OAuth authentication fails (initial
  token/refresh failure or subsequent refresh failure). Refresh-on-401
  retry is automatic on every API call via `lc.do(...)`.
- `POST /dispatch` — outbound verb dispatch (authenticated via
  `CHANNEL_SECRET`).
- `GET /caps` — capability list.

## Files

- `main.go`, `client.go`, `server.go`

## Related docs

- `specs/4/1-channel-protocol.md`
