---
status: draft
phase: next
depends: [28-acl]
---

# User Onboarding

User-centric onboarding. The user is the identity — JIDs are
claimed devices, groups are workspaces, routes are user-configured.

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

## Onboarding flow

Unknown JID messages bot → onbod sends auth link → user clicks.

### New user (no world)

1. OAuth authenticate
2. Pick username (validates: `^[a-z][a-z0-9-]{2,29}$`)
3. System creates world `<username>/`, grants `user_groups` access
4. JID auto-routed to `<username>/`
5. JID recorded in `user_jids`

### Existing user, new JID

1. OAuth authenticate
2. System recognizes OAuth sub → shows dashboard
3. User picks which groups this JID routes to
4. Routes created, JID recorded in `user_jids`

Same auth entry point. Branch after OAuth based on "user exists?".

## Onboarding state machine

Extend existing onbod states:

```
unknown_jid → prompted (auth link sent) → authenticated →
  → new_user: username picker → world created → done
  → existing_user: dashboard → routes configured → done
```

The `onboarding` table gets:

- `token TEXT` — one-time token (32-byte hex, 24h TTL)
- `token_expires TEXT` — ISO8601
- `user_sub TEXT` — set after OAuth, links JID to user

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

## Route authorization

User must have `user_groups` access to the target group to
create a route. Route creation checks this. Operator manages
`user_groups` directly (SQL/CLI).

## Not in scope

- Email/password auth (OAuth only)
- Token expiry UI
- Group invitations
- Username changes
- Slink token unification (separate spec)
