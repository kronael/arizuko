---
status: draft
phase: next
---

# User Identity & Auth

User-centric auth model. The user is the identity — JIDs are
claimed devices, groups are workspaces, routes are user-configured.

## Data model

```
user (OAuth sub → username)
  ├── jids: telegram:123, whatsapp:420...  (proved ownership)
  ├── groups: alice/, krons/support        (access grants)
  └── routes: telegram:123 → alice/        (user-managed)
               telegram:123 → krons/support
```

- `user_groups`: ACL. No rows = no access. `*` = operator.
- `routes`: JID → group. User must have `user_groups` access to
  the target group to create a route.
- A single JID can route to multiple groups.

## Onboarding flow

Unknown JID messages bot → onbod sends auth link → user clicks.

### New user (no world)

1. OAuth authenticate
2. Pick username (validates: `^[a-z][a-z0-9-]{2,29}$`)
3. System creates world `<username>/`, grants `user_groups` access
4. JID auto-routed to `<username>/`

### Existing user, new JID

1. OAuth authenticate
2. System recognizes OAuth sub → shows user dashboard
3. User picks which groups this JID routes to
4. Routes created

Same auth entry point. Branch after OAuth based on "user exists?".

## User dashboard

Served by onbod at `/onboard` (auth-gated via proxyd).

**No world yet**: username picker → create world → done.

**Has a world**:

```
My JIDs:     telegram:123, whatsapp:420   [+ Add platform]
My groups:   alice/, krons/support
Routing:     telegram:123 → alice/ ✓, krons/support ✓
             whatsapp:420 → alice/ ✓
             [edit]
```

Simple HTML, same style as existing auth pages. No framework.

## Slink → scoped auth token

A slink token is a refresh token with anonymous identity, scoped
to specific groups. Stored in `auth_sessions`.

- Issued by bot via MCP tool
- Revoked by deleting from `auth_sessions`
- Eliminates separate slink_token column and codepath

## ACL enforcement

- `store/auth.go`: `UserGroups` — no rows = no access, `*` = operator
- `proxyd`: sets `X-User-Groups` from JWT/session
- `webd`: `requireFolder` checks `X-User-Groups`
- Route creation: check `user_groups` before inserting

## Not in scope

- Email/password auth (OAuth only)
- Token expiry UI
- Group invitations (operator manages `user_groups` directly)
- Username changes
