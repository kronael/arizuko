---
status: planned
---

# Cross-channel identity — open

Link a single user across multiple channels and auth providers.
Prerequisite: `3/H-jid-format.md` (platform-prefixed sender JIDs).

## Identity record

A canonical identity links multiple subs (sender JIDs) belonging
to the same person:

```sql
CREATE TABLE identities (
  id          TEXT PRIMARY KEY,   -- uuid4, the canonical user id
  name        TEXT NOT NULL,      -- display name (last seen)
  created_at  TEXT NOT NULL
);

CREATE TABLE identity_claims (
  sub         TEXT PRIMARY KEY,   -- e.g. "telegram:123456", "local:<uuid>"
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

## In-group claim

A user can claim their web identity inside a channel group by
sending a one-time code:

1. `/auth/link-code` → returns a short-lived code (e.g. `LINK-ABCD`, 10min).
2. User sends the code in Telegram/Discord/WhatsApp.
3. Gateway matches code → links the channel sender to the JWT sub.

This bridges `local:<uuid>` (web password login) to `telegram:123456`
(Telegram sender) without requiring OAuth.

## Scope

- Identity resolution is advisory — agents can query it but does not
  enforce automatically.
- No identity merging UI (CLI only via `kanipi identity list/link/unlink`).
- Conflict resolution (two identities claim the same sub): last-write wins,
  log warning.

## Current state

Auth (`src/auth.ts`) is shipped: local accounts, argon2id, JWT,
refresh cookie. `auth_users.sub` is `local:<uuid>` — already a
valid claim sub in this model.

**What exists:**

- `auth_users` + `auth_sessions` tables in `db.ts`
- `messages.sender` stores platform-prefixed sender per message

**What is missing:**

- `identities` + `identity_claims` tables
- `GET /auth/link-code` endpoint
- Link-code detection in channel message handlers
- `kanipi identity` CLI subcommands
