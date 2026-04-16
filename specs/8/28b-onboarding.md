---
status: partial
phase: next
depends: [28-acl]
---

# User Onboarding

## Implementation status (v0.28.0)

Done:

- Token-based auth link flow (GET/POST /onboard, token consume, 24h TTL).
- OAuth callback → dashboard. New user gets username picker; existing
  user goes straight to dashboard.
- `linkJID` on cookie landing; idempotent; rejects re-claim by different
  sub.
- Second-JID auto-link: when a user who already has a world messages
  from a new platform, the dashboard handler auto-routes the new JID
  into the existing folder and skips the username picker. See
  `existingWorld`, `autoRoute` in `onbod/main.go`.

Pending:

- Dashboard editing of routes (currently read-only).
- Group invitations.
- Username changes.

User-centric onboarding. The user is the identity — JIDs are
claimed devices, groups are workspaces, routes are user-configured.

Chat is only the entry point (sends auth link). All setup happens
on the web dashboard.

## Data model

```
user (OAuth sub → username)
  ├── jids: telegram:123, whatsapp:420...  (proved ownership)
  ├── groups: alice/, krons/support        (access grants)
  └── routes: telegram:123 → alice/        (user-managed)
```

## New table: user_jids

```sql
CREATE TABLE user_jids (
  user_sub TEXT NOT NULL,
  jid      TEXT NOT NULL,
  claimed  TEXT NOT NULL,  -- ISO8601
  PRIMARY KEY (user_sub, jid)
);
```

JID ownership proof:

- Telegram: OAuth widget proves telegram user ID
- WhatsApp/other: user messages from JID during onboarding,
  then authenticates via OAuth — system links the two

## Chat-side flow

Minimal. Chat only sends the link, nothing else.

1. Unknown JID messages bot
2. Onbod replies with auth link: `https://<host>/onboard?token=<hex>`
3. Token is one-time, 24h TTL, tied to the JID
4. Done. Everything else is web.

The `onboarding` table gets:

- `token TEXT` — one-time token (32-byte hex, 24h TTL)
- `token_expires TEXT` — ISO8601
- `user_sub TEXT` — set after OAuth, links JID to user

No chat-based state machine beyond `unknown_jid → prompted`.

## Web dashboard

Served by onbod at `/onboard`. All setup happens here.

### Token landing page (unauthenticated)

User clicks link from chat → token validated → redirect to OAuth.
Token consumed on first use.

### After OAuth — new user (no world)

1. Username picker (validates: `^[a-z][a-z0-9-]{2,29}$`)
2. System creates world `<username>/`, grants `user_groups` access
3. JID auto-routed to `<username>/`
4. JID recorded in `user_jids`
5. Redirect to dashboard

### After OAuth — existing user

System recognizes OAuth sub → straight to dashboard.
JID auto-recorded in `user_jids` if not already claimed.

### Dashboard (authenticated)

```
My JIDs:     telegram:123, whatsapp:420   [+ Add platform]
My groups:   alice/, krons/support
Routing:     telegram:123 → alice/ ✓, krons/support ✓
             whatsapp:420 → alice/ ✓
             [edit]
```

Simple HTML, same style as existing auth pages. No framework.

## Route authorization

User must have `user_groups` access to the target group to
create a route. Route creation checks this. Operator manages
`user_groups` directly (SQL/CLI).

## MCP tools

No user-management MCP tools (add_route for self, claim_jid, etc.)
in chat. Agents cannot perform user authorization operations.
All user identity management is web-only.

Existing `add_route`/`set_routes` MCP tools remain for operator
use (tier 0 only).

## Not in scope

- Email/password auth (OAuth only)
- Chat-based username picker or route management
- Token expiry UI
- Group invitations
- Username changes
- Slink token unification (separate spec)
