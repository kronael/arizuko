---
status: draft
---

# LinkedIn Channel (`linkd`)

## Problem

LinkedIn is where professional conversations, articles, and post engagement
happen. An agent that can author posts, track mentions, and reply to comments
on your behalf closes the loop between chat-based interaction and public
professional presence.

## Scope

**In scope:**

- Inbound: feed items, mentions, comments on own posts → delivered to agent
- Outbound: publish posts, reply to comments, publish articles
- Content authoring: agent drafts → human approves → publishes

**Out of scope (requires LinkedIn partner program):**

- Direct messages API
- InMail

## Open Questions

1. **OAuth flow** — proxyd `/auth/linkedin` callback already supports generic
   OAuth2. Does it need changes or does linkd handle its own token refresh?

2. **Polling vs webhook** — LinkedIn doesn't offer webhooks for personal feed.
   Poll interval: how aggressive? 5min? 15min? Rate limits are strict (100
   req/day on free tier).

3. **Content approval gate** — should agent-authored posts go through an
   approval step (deliver draft to user first) or publish directly when
   instructed? Probably configurable: `LINKEDIN_AUTO_PUBLISH=false`.

4. **Article vs post** — LinkedIn Articles (long-form) vs Posts (short, with
   media) are different API endpoints. Spec both or start with posts only?

5. **Organization pages** — personal profile only, or also org pages the user
   manages? Scopes differ (`w_organization_social`).

6. **JID format** — personal profile: `linkedin:<urn:li:person:xxx>` or
   `linkedin:<vanityName>`? Stable URN is better.

## Design

### Daemon: `linkd`

Go daemon, same pattern as `mastd`/`bskyd`. Polls LinkedIn API, registers
with gated as channel `linkedin`.

```
linkd/main.go
template/services/linkd.toml
```

### Auth

OAuth2 PKCE flow. Scopes: `r_liteprofile`, `w_member_social`,
`r_organization_social` (optional). Token stored in data dir. proxyd
`/auth/linkedin` handles callback, writes token to file, linkd reads it.

### Inbound flow

Poll `/v2/shares?q=owners&owners=<urn>` for own posts, then
`/v2/socialActions/<post-urn>/comments` for new comments. Deliver each
comment as a message to gateway with `verb=comment`, `chat_jid=linkedin:<urn>`.

Also poll `/v2/networkUpdates` or LinkedIn notifications endpoint for mentions.

### Outbound flow

`POST /send` → `linkd` → `POST /v2/ugcPosts` (publish post) or
`POST /v2/socialActions/<urn>/comments` (reply to comment).

`/send-file` → attach image/video to UGC post via LinkedIn Assets API
(two-step: register upload → upload bytes → reference in post).

### Service template

```toml
# template/services/linkd.toml
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

## Code pointers (when built)

- `linkd/main.go` — poll loop, OAuth token refresh, channel registration
- `auth/` — token storage pattern (reuse existing JWT/OAuth primitives)
- `proxyd/` — `/auth/linkedin` callback route to add
- `template/services/linkd.toml` — compose service definition
