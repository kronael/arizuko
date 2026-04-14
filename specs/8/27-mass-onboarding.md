---
status: draft
phase: next
---

# Mass Onboarding

Supersedes "Not in scope" section of [4/21-onboarding.md](../4/21-onboarding.md).

## Problem

Current onboarding is 1-by-1 admin approval. Doesn't scale to mass
audience. Each user needs a personal world, but the admin can't
manually `/approve` hundreds of users. Platform JIDs are opaque
identifiers — users need a real identity (username) that works
across platforms.

## Model

Username = world. One user, one world, multiple platform JIDs.

```
telegram:1234567  ─┐
whatsapp:420...   ─┤─→  alice/
discord:9876...   ─┘
```

Web auth is the gate, not admin approval. The bot issues a token link;
the user authenticates via OAuth, picks a username, and completes
onboarding themselves.

## User journey

1. User sends "hello" on telegram
2. Gateway: no route → inserts onboarding row (existing behavior)
3. Onbod: picks up the row, generates a one-time token, replies:
   "Welcome! Set up your account: https://<host>/onboard?t=<token>"
4. User clicks link → proxyd serves onboarding page
5. User authenticates (GitHub / Google / Discord OAuth — existing auth)
6. User picks a username (validated: lowercase alnum + dash, 3-30 chars)
7. System creates world `<username>/`, route `telegram:1234567 → <username>/`
8. Onboarding status → approved, welcome system message enqueued
9. User sends next message on telegram → routes to their world
10. Later: user visits web UI, adds whatsapp account → second route
    `whatsapp:420... → <username>/` added to same world

## Onboarding states

Extends existing state machine with new states:

```
awaiting_message, prompted_at IS NULL:
  → generate token, send link
  → SET prompted_at = now, token = <token>

awaiting_message, prompted_at IS NOT NULL:
  → user hasn't clicked link yet
  → on new message: resend link (with same token)
  → token expires after 24h (new token on next message)

completing:
  → user clicked link, auth in progress
  → web endpoint handles this state

approved:
  → done (existing behavior)

rejected:
  → silence (existing behavior)
```

## Token

One-time token stored in onboarding table. New column:

```sql
ALTER TABLE onboarding ADD COLUMN token TEXT;
ALTER TABLE onboarding ADD COLUMN token_expires TEXT;  -- ISO8601
```

Token: 32-byte hex string (same as chanreg genToken pattern).
Expires after 24h. Regenerated on expiry if user sends new message.

No new table — onboarding table already has one row per JID.

## Web endpoint

New route in proxyd (or onbod HTTP server):

```
GET /onboard?t=<token>
```

Serves a page with:

1. Token validation (expired? already used? → error)
2. OAuth login (reuses existing auth/ OAuth flow)
3. Username picker (check availability against groups table)
4. Submit → creates world

No new web framework. Minimal HTML, same style as existing auth
pages in proxyd.

### POST /onboard/complete

Called after OAuth + username selection:

```json
{
  "token": "<onboarding_token>",
  "username": "<chosen_username>",
  "oauth_provider": "github",
  "oauth_id": "<provider_user_id>"
}
```

Validates:

- Token valid and not expired
- Username available (no existing group with that folder name)
- Username format: `^[a-z][a-z0-9-]{2,29}$`
- OAuth identity verified (session from auth flow)

On success:

- Creates world folder: `groups/<username>/`
- Copies prototype (existing ONBOARDING_PROTOTYPE logic)
- Inserts group in DB (tier 1)
- Adds route: JID → `<username>/`
- Sets onboarding status = approved, world_name = `<username>/`
- Enqueues welcome system message
- Redirects to web UI or shows success page

## Adding platforms

After onboarding, user can add more platform JIDs to their world.
Web UI page at `/<username>/settings/platforms`:

1. User clicks "Add Telegram" → gets a code
2. User sends code to the telegram bot
3. Bot verifies code, adds route: `telegram:<new_jid> → <username>/`
4. Or: reverse — user sends message to bot from new platform,
   bot sends link, user authenticates (same as initial onboard),
   system adds route to existing world instead of creating new one

For the reverse flow, onbod checks: does this OAuth identity already
have a world? If yes → add route to existing world. If no → create
new world. Single code path handles both first-time and add-platform.

## Rate limiting

### Daily onboarding cap

Per-instance: `ONBOD_MAX_ONBOARDS_PER_DAY` (default 1, 0 = unlimited).
Counted by tokens issued today. Over limit → onbod replies:
"We're not accepting new users right now. Try again tomorrow."

No per-world or per-channel limits — the cap is on total new users
per day across the entire instance.

### Per-world spawn rate limit

Separate from onboarding. Prevents one world from monopolizing
containers. In-memory sliding window in gateway, keyed by world.

Config in `.env`:

```
RATE_LIMIT_SPAWNS_PER_MIN=3      # 0 = disabled (default)
RATE_LIMIT_COOLDOWN_SECS=5       # 0 = disabled (default)
```

Global defaults — apply to all worlds equally. Per-world overrides
deferred (not enough worlds to justify the complexity yet).

Implementation: `gateway/ratelimit.go`, check in `pollOnce()` after
impulse gate. Rate-limited messages: cursor advanced, agent not
spawned. Next window → agent sees full history.

## Access control

See [28-unified-auth.md](28-unified-auth.md).
Onboarding inserts `user_groups` row when creating the world.

```sql
-- onboarding table additions
ALTER TABLE onboarding ADD COLUMN token TEXT;
ALTER TABLE onboarding ADD COLUMN token_expires TEXT;

-- user identity (new table)
CREATE TABLE user_identities (
  username    TEXT NOT NULL,
  provider    TEXT NOT NULL,    -- github, google, discord
  provider_id TEXT NOT NULL,
  created     TEXT NOT NULL,
  PRIMARY KEY (provider, provider_id)
);
CREATE INDEX idx_user_identities_username ON user_identities(username);
```

`user_identities` maps OAuth identities to usernames. Used for:

- "Does this OAuth user already have a world?" check
- Add-platform flow (verify ownership)

## Config

New env vars:

```
ONBOD_MAX_ONBOARDS_PER_DAY=1     # daily cap, 0 = unlimited
RATE_LIMIT_SPAWNS_PER_MIN=0      # per-world spawn limit, 0 = disabled
RATE_LIMIT_COOLDOWN_SECS=0       # post-response cooldown, 0 = disabled
```

Existing env vars used:

- `ONBOARDING_ENABLED` — master switch
- `ONBOARDING_PROTOTYPE` — prototype dir to clone
- `ONBOARDING_GREETING` — ignored in mass mode (replaced by token link)
- `WEB_HOST` — for constructing onboard URL

## Service boundaries

- **onbod**: token generation, link sending, state machine extensions.
  New HTTP endpoint `/onboard/complete` for web callback.
- **proxyd**: serves `/onboard` page (static HTML + JS), proxies
  `/onboard/complete` to onbod. Reuses existing OAuth middleware.
- **gateway**: rate limiter only. No onboarding changes.
- **auth/**: existing OAuth providers, no changes needed.
- **store/**: migration for new columns + table.

## Not in scope

- Email/password auth (OAuth only for now)
- Username changes after creation
- World deletion / account deactivation
- Payment / subscription gating
- Invite codes or referral system
- Per-world or per-channel onboarding rate limits (global cap only)

## Open questions

1. Should the onboard page be served by proxyd or onbod? Proxyd
   already serves web content and has OAuth middleware. Onbod is
   the onboarding owner. Leaning proxyd for serving, onbod for
   backend logic.
2. What happens if a user onboards on telegram, then messages from
   whatsapp without adding it? Currently: new onboarding flow starts.
   Should it detect "same person" somehow, or treat as separate user?
   Recommendation: separate user until they link accounts via web UI.
3. Token in URL vs token sent separately? URL is simpler (one click).
   Risk: URL shared/leaked. Mitigation: token single-use, 24h expiry.
