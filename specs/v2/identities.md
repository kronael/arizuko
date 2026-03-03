# Cross-channel identity (v2)

Link a single user across multiple channels and auth providers.

## Problem

In v1 each channel has an independent sender identity:

- Telegram: `tg:123456`
- Discord: `discord:789`
- Local password: `local:<uuid>`
- Anon slink: `anon:<cookie>`

The same person messaging on Telegram and via a slink link appears as two
different senders to the agent.

## Identity record

A canonical identity is a set of verified sub values that all belong to
the same person:

```sql
CREATE TABLE identities (
  id          TEXT PRIMARY KEY,   -- uuid4, the canonical user id
  name        TEXT NOT NULL,      -- display name (last seen)
  created_at  TEXT NOT NULL
);

CREATE TABLE identity_claims (
  sub         TEXT PRIMARY KEY,   -- e.g. "tg:123456", "local:<uuid>"
  identity_id TEXT NOT NULL REFERENCES identities(id),
  claimed_at  TEXT NOT NULL
);
```

`sender` in messages stays the original sub. Gateway resolves
`identity_id` for context injection if needed.

## Claiming

A user links a new sub to their canonical identity by:

1. Being authenticated (holding a valid JWT with an existing sub).
2. Completing auth for the new provider (OAuth flow or password login).
3. Gateway merges the new sub into the existing identity record.

Example: Alice is logged in as `tg:123456`. She clicks "Link GitHub".
After OAuth callback, `gh:alice` is added to her identity record.

## In-group claim

A user can also claim their web identity inside a channel group by
sending a one-time code:

1. `/auth/link-code` → returns a short-lived code (e.g. `LINK-ABCD`, 10min).
2. User sends the code in Telegram/Discord/WhatsApp.
3. Gateway matches code → links the channel sender to the JWT sub.

This bridges `local:<uuid>` (web password login) to `tg:123456`
(Telegram sender) without requiring OAuth.

## Scope

- Identity resolution is advisory — agents can query it but v2 does
  not enforce it anywhere automatically.
- No identity merging UI in v2 (CLI only via `kanipi identity list/link/unlink`).
- Conflict resolution (two identities claim the same sub): last-write wins,
  log warning.
