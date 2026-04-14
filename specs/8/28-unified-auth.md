---
status: draft
phase: next
---

# Unified Auth

Unifies OAuth web login and slink (anonymous web chat) into one
auth layer. Replaces the separate slink token system.

## ACL

`user_groups` table. No rows = no access. `*` = operator.

Onboarding inserts a row when creating a world. Operators manage
rows via SQL or CLI (`arizuko grant <instance> <sub> <folder|*>`).

## Slink → scoped auth token

A slink token is a refresh token with anonymous identity, scoped
to one group. Stored in `auth_sessions` like OAuth refresh tokens.

- `sub`: `anon:<8-char-hex>` (derived from IP or random)
- `groups`: `[<folder>]`
- Issued by bot via MCP tool or by onboarding flow
- Revoked by deleting from `auth_sessions`

Eliminates: `slink_token` column on groups, `GroupBySlinkToken`,
separate rate limiter, separate proxyd codepath.

## Token flow

```
POST /slink/<token>  →  proxyd looks up auth_sessions
                     →  sets X-User-Sub, X-User-Groups
                     →  proxies to webd (same as OAuth)
```

## What changes

- `store/auth.go`: `UserGroups` — no rows = empty list (no access),
  `*` row = nil (operator). Add `GrantUserGroup`.
- `proxyd`: slink route uses `requireAuth` instead of custom lookup
- `webd/slink.go`: receives user headers from proxyd, no token lookup
- `store/groups.go`: drop `slink_token` auto-generation
- MCP tool: `issue_slink` — creates auth session scoped to group

## JID claiming

A JID (telegram:123, whatsapp:420...) is linked to a world by
proving ownership: you sent a message from that JID, then
authenticated via OAuth.

### First contact

Message from unknown JID → onbod sends auth link → user
authenticates → picks username → world created → route added.
Single flow handles world creation + JID claim.

### Adding a platform

Message from unknown JID → onbod sends auth link → user
authenticates → system checks: does this OAuth sub already have
a `user_groups` row? If yes → add route to existing world (no
new world). If no → create new world.

Same code path. The only branch is "existing user?" check after
OAuth completes.

## Not in scope

- Per-token rate limits (use existing per-IP limiter)
- Token expiry UI (tokens last 30d like refresh tokens)
- Multiple groups per slink token
