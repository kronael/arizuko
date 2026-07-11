---
status: shipped
depends: [5/41-ext-mcp, 5/42-credentials, 5/1-auth-standalone]
---

# specs/5/43 — surrogate OAuth

## Status — GitHub pilot shipped

Shipped (this pilot):

- `secrets` gains `provider`/`refresh_val`/`expires_at`/`scope_list`
  (routd migration `0017`; PAT rows leave them NULL).
- `auth/surrogate/` — a standalone registry-driven engine
  (`AuthorizeURL`/`Exchange`/`Refresh`/`Revoke`) with one built-in provider
  (`providers/github.toml`, `go:embed`). It is DISTINCT from identity's login
  OAuth: it reuses only the low-level `auth` primitives — `auth.PostForm`
  (added, exported), `auth.WritePKCE`/`ConsumePKCE` (the shipped S256 PKCE),
  `auth.SignState`/`VerifyState` (the shipped CSRF-state cookie). PKCE stays at
  the dashd layer (cookie stash), so the engine's `AuthorizeURL` takes the
  challenge rather than returning a verifier-stash — the callback is already
  authenticated (X-User-Sub), so state is CSRF-only, sub read from the header.
- dashd `/dash/me/connections/` — `POST /<p>/start`, `GET /<p>/callback`,
  `DELETE /<p>`, plus a list page beside `/dash/me/secrets`.
- Broker near-expiry refresh in `routd` `ConnectorSecrets` (signature became
  `(map, error)`): refresh when `expires_at−now < 60s`; a revoked refresh nulls
  `expires_at`+`refresh_val` and returns a "reconnect" error the agent sees.
  `SURROGATE_GITHUB_CLIENT_ID`/`_SECRET` in `core.Config`.

Deferred (follow-ups, not this pilot):

- Generalizing identity's per-provider `exchangeGitHub`/`exchangeGoogle` into
  the registry-driven engine ("the one refactor" below) — DEFERRED; it risks
  the shipped login path. Surrogate carries its own engine for now.
- Spawn-time env-profile refresh (Anthropic/ChatGPT via `FolderSecretsForUser`)
  — the refresh check fires only at the per-call broker path today.
- `cmd/arizuko/surrogate.go` operator inspector; providers beyond github.

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
-- <next free migration>-surrogate-oauth.sql (pick the next free number at
-- implementation time; 0049 is taken by cost-log)
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
dashd handlers, the four `secrets` columns, the broker's near-expiry refresh check,
one built-in provider + e2e test, and a `cmd/arizuko/surrogate.go` operator
inspector.

## Acceptance

1. **Connect** — "Connect GitHub" round-trips; row lands with `provider`,
   non-NULL `expires_at` + `refresh_val`.
2. **Fresh-token call** — an agent tool call resolves the row, sees
   `expires_at` in the future, calls GitHub with `value`.
3. **Refresh on near-expiry** — force `expires_at=now+30s`; the next call
   hits the refresh endpoint, updates the row, calls with the new token.

## Shared OAuth core

Identity and surrogate are two callers of one provider-agnostic
authorization-code engine in `auth/` — the same code that already backs
login, generalized so both drive it:

- `AuthorizeURL(provider, redirect, state)` — build the authorize URL + S256
  `code_challenge`, stash the `code_verifier` keyed by `state`.
- `Exchange(provider, code, verifier)` → `{access, refresh, expires, scope}`.
- `Refresh(provider, refresh)` → same shape.

The engine is provider-agnostic; per-provider URLs, scopes, and field quirks
live in the registry. The two callers differ only in their registry and
their **sink** for the resulting token:

| caller    | registry      | sink                                  |
| --------- | ------------- | ------------------------------------- |
| identity  | login IdPs    | mint arizuko JWT + authd refresh row  |
| surrogate | API providers | write a `secrets` capability-cred row |

Today identity's exchange is per-provider (`auth/oauth.go` google/github/
discord). Generalizing those into the registry-driven `Exchange` is the one
refactor; surrogate then adds only its registry + the write-secret sink.
**DEFERRED (pilot):** the refactor risks the shipped login path, so surrogate
ships with its OWN engine (`auth/surrogate/`) reusing only the low-level `auth`
primitives; identity's per-provider exchange is untouched.

## Usage is not special-cased

Once written, the token is a **normal capability credential**
([`5/42`](42-credentials.md)). Every consumer resolves it through 42's
standard path — shape 2 env render, shape 3 bearer, or a future HTTP-MCP
client transport ([`5/41`](41-ext-mcp.md)). Surrogate owns the **write +
refresh** only; nothing downstream knows the row came from OAuth rather than
a paste, except the refresh check on `expires_at`.

## Also covers env-profile keys (Anthropic, OpenAI)

`CLAUDE_CODE_OAUTH_TOKEN` is already an OAuth token, pasted by hand today;
ChatGPT/codex is the same. Surrogate OAuth automates both — a "Connect
Anthropic" / "Connect ChatGPT" button, the OAuth sibling of `/dash/me/env`.

The write is identical; only injection differs. These are **env-profile
keys** ([`5/42`](42-credentials.md) type 1) — resolved by
`FolderSecretsForUser` and injected into the container env at spawn, not
call-time-brokered. So the refresh check (`expires_at − now < 60s`) fires at
that spawn-time resolution point, not the per-call broker path. Same new
`secrets` columns, same registry, same dance — only the sink call site moves.

Static API keys (`sk-ant-…`, `sk-…`) stay manual paste; there is no dance to
run for a non-OAuth key.

## Grants stay orthogonal

Connecting a provider does **not** grant anything. The token enables the
capability; a separate ACL grant (`mcp:<p>:*` / `ext:<p>:*`) still decides
whether the agent may call the tool — identical to a pasted PAT. Credential
ownership and grant are orthogonal (42).

## Out of scope (v1)

- Multi-account per provider — single `(user, provider)` row.
- Public clients — confidential client_secret providers only; PKCE always
  on, client_secret still required.
- Multi-instance CSRF state — in-memory, single-process dashd; add a
  `surrogate_oauth_state` table if dashd goes multi-process.
- Auto-opening crackbox egress to `allowed_domain` on connect — its own
  future feature spec; here `allowed_domain` is an informational hint only,
  egress stays a manual `network_rules` step.
- Background refresh; provider revocation on internal cleanup.

## Cross-references

- [`5/41`](41-ext-mcp.md) — the broker that reads these rows.
- [`5/42`](42-credentials.md) — the credential model; surrogate is one write path.
- [`5/1`](1-auth-standalone.md) — the OAuth primitives surrogate reuses.
