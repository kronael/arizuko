---
status: implemented
phase: shipped
supersedes: [4/21-onboarding.md]
---

# Self-Service Onboarding

Username = world. One user, one world, multiple platform JIDs.
Web auth is the gate. The bot issues a token link; the user
authenticates via OAuth, picks a username, and gets a world.

```
telegram:1234567  ─┐
whatsapp:420...   ─┤─→  alice/
discord:9876...   ─┘
```

## User journey

1. User sends "hello" on telegram
2. Gateway: no route → inserts onboarding row (status: awaiting_message)
3. Onbod: picks up the row, generates one-time token, replies with link
4. User clicks link → proxyd routes to onbod at /onboard
5. Token consumed, cookie set, redirect to OAuth
6. User authenticates (GitHub / Google / Discord / Telegram OAuth)
7. User picks a username (validated: `^[a-z][a-z0-9-]{2,29}$`)
8. System creates world `<username>/`, route, user_groups grant
9. User sends next message → routes to their world

## Second-platform auto-link

When a user who already has a world messages from a new platform:

1. New JID enters onboarding flow as normal
2. User clicks token link → authenticates
3. System recognizes existing OAuth sub → links new JID
4. Auto-creates route to existing world
5. Dashboard renders (no username picker)

## Onboarding states

```
awaiting_message, prompted_at IS NULL:
  → generate token, send link
  → SET prompted_at = now, token = <token>

awaiting_message, prompted_at IS NOT NULL:
  → user hasn't clicked yet; resend link on new message
  → token expires after 24h (new token on next message)

token_used:
  → user clicked link, OAuth in progress

approved:
  → done — JID linked, world exists, route active
```

## Schema

```sql
-- onboarding table (columns added to existing)
ALTER TABLE onboarding ADD COLUMN token TEXT;
ALTER TABLE onboarding ADD COLUMN token_expires TEXT;  -- ISO8601
ALTER TABLE onboarding ADD COLUMN user_sub TEXT;       -- set after OAuth

-- JID ownership
CREATE TABLE user_jids (
  user_sub TEXT NOT NULL,
  jid      TEXT NOT NULL,
  claimed  TEXT NOT NULL,  -- ISO8601
  PRIMARY KEY (user_sub, jid)
);
CREATE UNIQUE INDEX idx_user_jids_jid ON user_jids(jid);

-- group access grants
CREATE TABLE user_groups (
  user_sub TEXT NOT NULL,
  folder   TEXT NOT NULL,
  PRIMARY KEY (user_sub, folder)
);
```

## Web endpoints (served by onbod)

### GET /onboard?token=<hex>

Token landing. Validates token, consumes it (status → token_used),
sets `onboard_jid` cookie, redirects to OAuth.

### GET /onboard (authenticated)

Dashboard. If user has no world → username picker. If user has
world → shows linked JIDs, groups, routes.

Second-JID auto-link: if `onboard_jid` cookie present and status
is `token_used`, links JID to user and auto-routes to existing world.

### POST /onboard (action=create_world)

Creates world folder via `container.SetupGroup`, inserts group and
user_groups rows, creates routes for all linked JIDs.

## Service boundaries

- **onbod**: token generation, link sending, web dashboard, world creation
- **proxyd**: routes /onboard to onbod (optionalAuth middleware)
- **gateway**: inserts onboarding row when no route found. No commands.
- **auth/**: existing OAuth providers, JWT/refresh_token
- **store/**: migrations for schema additions

## Config

```
ONBOARDING_ENABLED=true          # master switch; onbod exits if false
ONBOARDING_PROTOTYPE=            # prototype dir to clone into new worlds
ONBOARDING_GREETING=             # optional greeting prepended to token link
AUTH_BASE_URL=                   # web base URL for constructing links
```

## Pending features

- `ONBOD_MAX_ONBOARDS_PER_DAY` rate limiting (daily cap on new users)
- Dashboard route editing (currently read-only)
- Group invitations
- Username changes after creation

## Not in scope

- Email/password auth (OAuth only)
- World deletion / account deactivation
- Payment / subscription gating
- Chat-based username picker or route management
