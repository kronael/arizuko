---
status: shipped
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
7. If gates configured: user enters queue (status: queued)
8. Admission job admits user when daily limit allows
9. User picks a username (validated: `^[a-z][a-z0-9-]{2,29}$`)
10. System creates world `<username>/`, route, user_groups grant
11. User sends next message → routes to their world

If no gates configured, steps 7-8 are skipped (legacy behavior).

## Second-platform auto-link

When a user who already has a world messages from a new platform:

1. New JID enters onboarding flow as normal
2. User clicks token link → authenticates
3. System recognizes existing OAuth sub → links new JID
4. Auto-creates route to existing world
5. Dashboard renders (no username picker)

## Gates

Gates filter who can onboard and how many per day. Configured via
`ONBOARDING_GATES` env var, comma-separated.

Format: `type:param=value:limit/day`

```
github:org=mycompany:10/day    # GitHub users, 10/day
google:domain=company.com:20/day  # Google Workspace domain, 20/day
email:domain=example.com:5/day    # email domain, 5/day
*:50/day                          # catch-all, 50/day
```

Gate matching is provider-prefix based:

- `github:` — matches any sub starting with `github:`
- `google:domain=X` — matches `google:` subs where email domain = X
- `email:domain=X` — matches subs ending with `@X`
- `*` — matches everything

First matching gate wins. If no gate matches, user is rejected
(stays in token_used, no queue entry).

## Admission queue

When gates are configured, after OAuth the user enters `queued` state
instead of `approved`. An admission job runs every ~1 minute:

1. For each gate, count today's admissions (queued_at date = today)
2. remaining = limitPerDay - today's count
3. Admit oldest queued users up to remaining
4. Admitted users see the username picker on next dashboard visit

Dashboard for queued users shows position and estimated wait time.
Auto-refreshes every 30 seconds.

## Onboarding states

```
awaiting_message, prompted_at IS NULL:
  → generate token, send link
  → SET prompted_at = now, token = <token>

awaiting_message, prompted_at IS NOT NULL:
  → link sent; user hasn't bound identity yet
  → token expires after 24h (new token on next message)

token_used:
  → user_sub bound (OAuth complete), token cleared

queued (gates only):
  → user passed OAuth, waiting for admission
  → gate and queued_at recorded

approved:
  → done — JID linked, world exists, route active
```

## Token lifecycle

The link is **replay-safe**. Token presentation and identity-bind are
two separate steps:

- **Presentation** (`GET /onboard?token=X`) is idempotent. Validates
  the token, sets `onboard_jid` cookie, redirects to OAuth. Status
  stays `awaiting_message`. Token stays set. Re-clickable.
- **Identity-bind** (post-OAuth `GET /onboard` with `X-User-Sub`) is
  the single-shot. Atomic UPDATE binds `user_sub`, sets status to
  `token_used`, clears the token. After this, `?token=X` replays
  fail validation (token gone).

If the user clicks the link, bails on OAuth, and returns: the link
still works. If 30 minutes pass with `status=token_used` AND
`user_sub IS NULL` (which can happen if onboarding rows ever land in
that state without binding) AND the user re-messages the bot, the
row is reset to `awaiting_message` and a fresh token is minted on
the next `promptUnprompted` cycle. The 30-minute cool-down prevents
thrashing under repeated click-and-bail.

## Schema

```sql
-- onboarding table (columns added to existing)
ALTER TABLE onboarding ADD COLUMN token TEXT;
ALTER TABLE onboarding ADD COLUMN token_expires TEXT;  -- ISO8601
ALTER TABLE onboarding ADD COLUMN user_sub TEXT;       -- set after OAuth
ALTER TABLE onboarding ADD COLUMN gate TEXT;           -- gate key (e.g. "github:org=co")
ALTER TABLE onboarding ADD COLUMN queued_at TEXT;      -- ISO8601, when queued

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

Token landing. Validates token (does NOT consume it), sets
`onboard_jid` cookie, redirects to OAuth. Idempotent — re-clickable
until the user completes OAuth and the dashboard binds `user_sub`.
See "Token lifecycle" above.

### GET /onboard (authenticated)

Dashboard. Behavior depends on state:

- **queued**: shows queue position and estimated wait (auto-refresh 30s)
- **no world**: username picker
- **has world**: shows linked JIDs, groups, routes

Second-JID auto-link: if `onboard_jid` cookie present and status
is `token_used`, links JID to user and auto-routes to existing world.

### POST /onboard (action=create_world)

Creates world folder via `container.SetupGroup`, inserts group and
user_groups rows, creates routes for all linked JIDs.

## Service boundaries

- **onbod**: token generation, link sending, web dashboard, world creation, admission queue
- **proxyd**: routes /onboard to onbod (optionalAuth middleware)
- **gateway**: inserts onboarding row when no route found. No commands.
- **auth/**: existing OAuth providers, JWT/refresh_token
- **store/**: migrations for schema additions

## Config

```
ONBOARDING_ENABLED=true          # master switch; onbod exits if false
ONBOARDING_GREETING=             # optional greeting prepended to token link
ONBOARDING_GATES=                # comma-separated gate definitions (empty = no queue)
AUTH_BASE_URL=                   # web base URL for constructing links
```

Prototype files: place them in `groups/<parent>/prototype/` (for root-level
worlds: `groups/prototype/`). onbod copies the prototype directory into the
new world on creation. No env var needed.

## Pending features

- Dashboard route editing (currently read-only)
- Group invitations
- Username changes after creation
- GitHub org membership API check (currently provider-prefix only)

## Not in scope

- Email/password auth (OAuth only)
- World deletion / account deactivation
- Payment / subscription gating
- Chat-based username picker or route management
