---
status: deferred
depends: [9/11-crackbox-secrets]
---

# Surrogate OAuth — third-party tokens for the bot to use

Per-user OAuth where a teammate logs into a third-party site (Jira,
GitHub, Linear, Notion, Google Calendar) **as themselves**; access +
refresh tokens persist in arizuko's `secrets` table for the bot to use
on their turns. The agent never sees them; egred substitutes at
egress (inherits [`9/11-crackbox-secrets`](../9/11-crackbox-secrets.md)).

Distinct from **identity OAuth** (`auth/oauth.go`, which authenticates
the user _to_ arizuko). Surrogate OAuth authenticates arizuko-the-bot
_to_ a third party, as if it were the user.

Why deferred: spec 11's manual-PAT path (Phase A) covers the basic
case. Surrogate OAuth is the right primitive for sustained team use
(short-lived, scoped, revokable, auto-refresh) but not on the critical
path until a deployment asks.

## Must-have shape

- **Provider registry** — TOML per provider under
  `auth/surrogate/providers/<name>.toml`: `auth_url`, `token_url`,
  `scopes`, default domain + env name + header format. Client
  ID/secret operator-owned in `.env`
  (`SURROGATE_<PROVIDER>_CLIENT_ID/SECRET`).
- **OAuth dance** — `GET /auth/surrogate/<p>/start` → 302 to provider
  with `state=<csrf>`; `GET /auth/surrogate/<p>/callback` validates
  state, exchanges code, writes two rows to `secrets`
  (`SURROGATE_<P>_ACCESS`, `SURROGATE_<P>_REFRESH`) under
  `scope_kind='user'`. CSRF state in-memory, 10-min TTL.
- **Refresh worker** — background goroutine (or `timed` task) refreshes
  before `expires_at`. Not lazy-on-use; avoids per-turn latency.
- **Dashboard** — `/dash/me/connections` lists providers + state;
  `DELETE /dash/me/connections/<p>` revokes both rows and calls
  provider revocation endpoint if supported.

Reuses the `secrets` table from migration `0034-secrets.sql`. No new
table. Confidential clients only in v1 (no PKCE). Single account per
(user, provider).

## Touches

`auth/surrogate.go`, `auth/surrogate/providers/*.toml`,
`auth/surrogate/refresh.go`, `dashd/me_connections.go`,
`cmd/arizuko/surrogate.go`.

Estimate: ≈ two daemon-weeks for the framework + N days per provider.
