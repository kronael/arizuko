# linkd

LinkedIn channel adapter (stub).

## Purpose

Minimal LinkedIn adapter. LinkedIn's API is restrictive and most social
endpoints are not available without partner access — linkd is a
placeholder: it registers with the router, polls, and supports outbound
post publishing when `LINKEDIN_AUTO_PUBLISH=true`. Expect rough edges.

## Responsibilities

- Register as `linkedin:` prefix, caps `send_text`, `fetch_history`.
- Poll LinkedIn API (interval `LINKEDIN_POLL_INTERVAL`, default `300s`).
- Optionally publish posts via UGC endpoint.

## Entry points

- Binary: `linkd/main.go`
- Listen: `$LISTEN_ADDR` (default `:9006` — collides with reditd; override in compose)
- Router registration: `linkedin:` prefix.

## Dependencies

- `chanlib`

## Configuration

- `LINKEDIN_CLIENT_ID`, `LINKEDIN_CLIENT_SECRET`
- `LINKEDIN_ACCESS_TOKEN`, `LINKEDIN_REFRESH_TOKEN`
- `LINKEDIN_API_BASE` (default `https://api.linkedin.com`)
- `LINKEDIN_OAUTH_BASE` (default `https://www.linkedin.com`)
- `LINKEDIN_POLL_INTERVAL`, `LINKEDIN_AUTO_PUBLISH`
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`, `DATA_DIR`

## Health signal

`GET /health` returns 503 when the access token is expired and refresh
fails.

## Files

- `main.go`, `client.go`, `server.go`

## Related docs

- `specs/4/1-channel-protocol.md`
