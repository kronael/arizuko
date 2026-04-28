---
status: shipped
---

# Cross-channel identity

Link multiple subs (platform senders) to a canonical user identity.

Schema:

```sql
CREATE TABLE identities (id TEXT PK, name TEXT, created_at TEXT);
CREATE TABLE identity_claims (sub TEXT PK, identity_id TEXT, claimed_at TEXT);
```

Claiming: authenticated user completes new-provider auth, gateway
merges. In-chat: `/auth/link-code` mints short-lived code, user sends
it in channel, gateway links sender ↔ JWT sub.

Rationale: `auth_users.sub` already valid as a claim sub; need the
merge layer and in-group claim flow. Advisory only — agents query,
never enforce.

Unblockers: `identities`/`identity_claims` tables, `/auth/link-code`
endpoint, link-code detection in channel adapters, `arizuko identity
list/link/unlink` CLI. Depends on
[S-jid-format.md](S-jid-format.md).
