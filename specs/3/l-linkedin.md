---
status: shipped
---

# LinkedIn Channel (`linkd`)

Inbound feed items / mentions / comments on own posts. Outbound
publishing (posts, comment replies, articles).

DMs and InMail require LinkedIn partner program — out of scope.

## Daemon

Go daemon, same pattern as `mastd`/`bskyd`. Polls LinkedIn API,
registers with gated as channel `linkedin`.

- `linkd/main.go`
- `template/services/linkd.toml`

## Auth

OAuth2 PKCE. Scopes: `r_liteprofile`, `w_member_social`,
`r_organization_social` (optional). Token in data dir. proxyd
`/auth/linkedin` handles callback, writes to file, linkd reads.

## Flow

Inbound: poll `/v2/shares?q=owners&owners=<urn>` for own posts, then
`/v2/socialActions/<post-urn>/comments` for new comments. Also poll
`/v2/networkUpdates` for mentions. Deliver each comment with `verb=comment`,
`chat_jid=linkedin:<urn>`.

Outbound: `POST /send` → `linkd` → `POST /v2/ugcPosts` or
`POST /v2/socialActions/<urn>/comments`. `/send-file` uses Assets API
(register upload → upload bytes → reference in post).

## Service template

```toml
image = "arizuko:latest"
entrypoint = ["linkd"]

[environment]
ROUTER_URL = "http://gated:${API_PORT}"
CHANNEL_SECRET = "${CHANNEL_SECRET}"
ASSISTANT_NAME = "${ASSISTANT_NAME}"
LISTEN_ADDR = ":8080"
LISTEN_URL = "http://linkd:8080"
LINKEDIN_CLIENT_ID = "${LINKEDIN_CLIENT_ID}"
LINKEDIN_CLIENT_SECRET = "${LINKEDIN_CLIENT_SECRET}"
LINKEDIN_POLL_INTERVAL = "300s"
LINKEDIN_AUTO_PUBLISH = "false"
```

## Open

- Poll interval (5 vs 15 min; free tier rate limit 100 req/day)
- Content approval gate: draft to user first, or publish directly?
  `LINKEDIN_AUTO_PUBLISH=false` default.
- Articles vs Posts (different endpoints — start with posts)
- Personal profile vs org pages (different scopes)
- JID format: `linkedin:<urn:li:person:xxx>` (stable) vs vanityName

## Files

- `linkd/main.go` — poll loop, OAuth refresh, channel registration
- `auth/` — token storage (reuse existing primitives)
- `proxyd/` — `/auth/linkedin` callback
- `template/services/linkd.toml`
