---
status: draft
depends: [5/41-ext-mcp, 5/42-credentials, 5/1-auth-standalone]
supersedes: [specs/11/14-surrogate-oauth]
---

# specs/5/43 — surrogate OAuth

> The user clicks "Connect GitHub" in their dashboard; arizuko runs the
> OAuth dance and writes the access + refresh token into the `secrets`
> table the [`5/41`](41-ext-mcp.md) broker reads at call time. Same
> destination row a pasted PAT lands in — only the writer changes.

**Surrogate** = arizuko authenticates _as the user_ to a third party.
Distinct from **identity OAuth** (`auth/oauth.go`, shipped) which
authenticates the user _to_ arizuko. The two are inbound and outbound
callers of one OAuth core: surrogate reuses identity's
authorize/exchange/refresh primitives, including its S256 PKCE
(`auth/oauth.go:432`, `authd/oauth.go:118`).

## Where it sits

This is the write path for a **capability credential**
([`5/42`](42-credentials.md)). `/dash/me/secrets` manual PAT paste is the
floor — zero OAuth, works today with fine-grained PATs. Surrogate is the
upgrade: short-lived scoped tokens, auto-refresh, a "Connect" button
instead of "paste this opaque string". Resolution, scope, and injection
belong to 42 — this spec covers only how the row gets written and kept
fresh.

## Schema

`secrets` (from `0034-secrets.sql`) gains four optional columns:

```sql
-- 0049-surrogate-oauth.sql
ALTER TABLE secrets ADD COLUMN provider    TEXT;      -- "github" | "linear"; NULL for PAT
ALTER TABLE secrets ADD COLUMN refresh_val BLOB;      -- refresh_token, sealed; NULL for PAT
ALTER TABLE secrets ADD COLUMN expires_at  DATETIME;  -- access_token expiry; NULL = non-expiring
ALTER TABLE secrets ADD COLUMN scope_list  TEXT;      -- granted scopes, CSV
```

PAT rows leave all four NULL — the broker reads `value` unchanged. OAuth
rows populate all four; the broker checks `expires_at` before each call.
`refresh_val` is sealed with the same AES-256-GCM as `value`. One row per
`(scope_kind='user', scope_id=user_sub, key=<provider-env-name>)`, so an
OAuth row at `key='GITHUB_TOKEN'` shadows a pasted PAT at the same key;
user-scope still wins over folder.

## Provider registry

```toml
# auth/surrogate/providers/github.toml
auth_url       = "https://github.com/login/oauth/authorize"
token_url      = "https://github.com/login/oauth/access_token"
revoke_url     = "https://api.github.com/applications/{client_id}/token"
scopes         = ["repo", "read:user"]
secret_key     = "GITHUB_TOKEN"       # the secrets.key to write
allowed_domain = "api.github.com"     # egress hint for the connector
```

Client id/secret operator-owned in `.env` as
`SURROGATE_<PROVIDER>_CLIENT_ID` / `_CLIENT_SECRET`.

## The dance — dashd `/dash/me/connections/`

1. **`POST /<provider>/start`** — mint `state=<csrf>` keyed to `caller.sub`
   with a 10-min TTL; 302 to `{auth_url}` with `response_type=code`, the
   scopes, the S256 `code_challenge`, and `state`. Add `access_type=offline`
   for providers that need it to return a refresh_token (google, microsoft).
2. **`GET /<provider>/callback?code&state`** — validate `state`; POST
   `{token_url}` with `grant_type=authorization_code`, the `code_verifier`,
   and client creds; persist `value=access_token`, `refresh_val`,
   `expires_at`, `scope_list`, `provider`; redirect back.
3. **`DELETE /<provider>`** — delete the row; if `revoke_url` is set, POST a
   best-effort revocation.

The surface sits beside `/dash/me/secrets`: "OAuth connections" here,
"Pasted tokens" from 42. Distinguisher = whether the provider has a
registered surrogate config.

## Refresh — at call time, not a worker

The broker refreshes when `expires_at − now < 60s`: POST `token_url` with
`grant_type=refresh_token`, update the row, then make the outbound call
with the new token. Applies to both broker call paths — shape 2 subprocess
(token rendered into env) and shape 3 REST (attached as bearer). A reactive
retry-once on a 401 covers providers with sloppy `expires_in`. No background
goroutine — most users touch most providers never.

Refresh failure (revoked refresh_token → 400) nulls `expires_at` +
`refresh_val` and surfaces a structured "reconnect" error to the agent.

## Build

Reuse `auth/`'s existing authorize/exchange/refresh + S256 PKCE — no
from-scratch OAuth core. New work: a provider-registry loader, the three
dashd handlers, the `0049` columns, the broker's near-expiry refresh check,
one built-in provider + e2e test, and a `cmd/arizuko/surrogate.go` operator
inspector.

## Acceptance

1. **Connect** — "Connect GitHub" round-trips; row lands with `provider`,
   non-NULL `expires_at` + `refresh_val`.
2. **Fresh-token call** — an agent tool call resolves the row, sees
   `expires_at` in the future, calls GitHub with `value`.
3. **Refresh on near-expiry** — force `expires_at=now+30s`; the next call
   hits the refresh endpoint, updates the row, calls with the new token.

## Out of scope (v1)

- Multi-account per provider — single `(user, provider)` row.
- Public clients — confidential client_secret providers only; PKCE is
  always on, but a client_secret is still required.
- Multi-instance CSRF state — in-memory, single-process dashd; add a
  `surrogate_oauth_state` table if dashd goes multi-process.
- Background refresh; provider revocation on internal cleanup.

## Open — decide before build

1. **Grant coupling** — does "Connect GitHub" imply the `mcp:github:*` /
   `ext:github:*` grant, or is the grant a separate self/operator action?
2. **Egress coupling** — does connecting auto-open crackbox egress to
   `allowed_domain`, or stay a manual `network_rules` step?
3. **Reuse boundary** — identity's token-exchange fns are per-provider
   (google/github/discord). Extract a shared core, or copy the ~30 lines?
4. **Hosted HTTP-MCP** — surrogate mints the token; the HTTP-MCP client
   transport that consumes it is a `5/41` add. Confirm the seam.

## Cross-references

- [`5/41`](41-ext-mcp.md) — the broker that reads these rows.
- [`5/42`](42-credentials.md) — the credential model; surrogate is one write path.
- [`5/1`](1-auth-standalone.md) — the OAuth primitives surrogate reuses.
