---
status: shipped
---

# LinkedIn channel (`linkd`)

Go daemon, same pattern as `mastd`/`bskyd`: polls the LinkedIn API,
registers with routd as channel `linkedin`, JID prefix `linkedin:`.
Ships as `template/services/linkd.yml`.

Scope is feed items, mentions, and comments on our own posts, plus
outbound publishing. **DMs and InMail are out of scope** — they require
the LinkedIn partner program, which is not obtainable for a
self-hosted deployment.

## Auth

OAuth2 PKCE with scopes `r_liteprofile`, `w_member_social`, and
optionally `r_organization_social`. proxyd handles the
`/auth/linkedin` callback and writes the token to the data dir; linkd
reads it. The callback lives in proxyd rather than linkd because proxyd
already owns the public port and every other OAuth callback — a second
public surface per adapter is the thing the proxy exists to prevent.

## Polling shape

Inbound needs two calls per cycle: `/v2/shares?q=owners` for our own
posts, then `/v2/socialActions/<urn>/comments` per post, plus
`/v2/networkUpdates` for mentions. That N+1 is why the poll interval is
conservative — the free tier allows ~100 requests/day, which a 5-minute
interval exhausts on its own.

`LINKEDIN_AUTO_PUBLISH=false` by default: publishing to a professional
identity is the one channel where a wrong agent post is expensive and
not quietly deletable, so the default is draft-to-user.

## Open

- Articles vs posts use different endpoints; only posts are wired.
- Personal profile vs organization pages need different scopes.
- JID stability: `linkedin:<urn:li:person:xxx>` is stable, a vanity name
  is not — the URN is used.
