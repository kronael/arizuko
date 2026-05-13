---
status: spec
depends: [9/11-crackbox-secrets, 6/1-auth-standalone]
---

# Surrogate OAuth — fill the broker's `secrets` table via OAuth flows

> The user clicks "Connect GitHub" in their dashboard; arizuko does
> the OAuth dance; the resulting access + refresh tokens land in the
> `secrets` table that [`9/11`](11-crackbox-secrets.md)'s broker
> already reads at tool-call time. The agent and the MCP server stay
> unchanged — the only thing that changes is _how_ the token row got
> written.

Distinct from **identity OAuth** (`auth/oauth.go`, shipped) which
authenticates the user _to_ arizuko. Surrogate OAuth authenticates
arizuko-the-bot _to_ a third party, as if it were the user.

## Why a separate spec

[`9/11`](11-crackbox-secrets.md) ships a broker that injects whatever
sits in `secrets.value` into MCP-subprocess env on each call. The
writer side is initially just `/dash/me/secrets` (manual paste) —
which is fully useful with **PATs** (GitHub fine-grained tokens,
Linear PATs, OpenAI keys). PAT-only takes a deployment from zero to
per-user `github_*` tools end-to-end with no OAuth code.

OAuth is the **upgrade**: short-lived scoped tokens, auto-refresh,
"Connect GitHub" button instead of "go to settings/tokens, paste this
opaque string". Different code path, same destination row. Splitting
lets 9/11 ship without OAuth blocking it, and lets 9/14 ship against
an already-tested broker.

## Orthogonality split

| Concern                                                                | Owner                                |
| ---------------------------------------------------------------------- | ------------------------------------ |
| OAuth primitives (URL build, code exchange, refresh via refresh_token) | `auth/surrogate.go` (library)        |
| Surrogate OAuth dance (start, callback, persist)                       | dashd `/dash/me/connections/`        |
| Token storage (access + refresh + expires_at)                          | gated `secrets` table                |
| Refresh on call (401 retry, near-expiry pre-emptive)                   | gated broker wrapper (from 9/11 M6)  |
| Using the token for outbound HTTP                                      | MCP subprocess (unchanged from 9/11) |

The MCP server never knows OAuth exists. It reads
`GITHUB_PERSONAL_ACCESS_TOKEN` on startup, uses whatever string is
there. Broker hands it a fresh access_token at every spawn; refresh
is invisible to the subprocess.

## Schema additions

The existing `secrets` table from
[`0034-secrets.sql`](../../store/migrations/0034-secrets.sql) gains
optional columns for OAuth-sourced rows:

```sql
-- 0049-surrogate-oauth.sql
ALTER TABLE secrets ADD COLUMN provider    TEXT;        -- "github" | "linear" | …; NULL for PAT
ALTER TABLE secrets ADD COLUMN refresh_val BLOB;        -- refresh_token; NULL for PAT
ALTER TABLE secrets ADD COLUMN expires_at  DATETIME;    -- access_token expiry; NULL for non-expiring
ALTER TABLE secrets ADD COLUMN scope_list  TEXT;        -- granted scopes, CSV
```

PAT rows leave the new columns NULL; the broker reads `value` and
injects unchanged. OAuth rows populate all four; the broker checks
`expires_at` before each call.

One row per `(scope_kind='user', scope_id=user_sub, key=<provider-env-name>)`,
so an OAuth-completed row at `key='GITHUB_TOKEN'` shadows a paste-PAT
row at the same key. User-set still wins over folder.

## Provider registry

```toml
# auth/surrogate/providers/github.toml
auth_url       = "https://github.com/login/oauth/authorize"
token_url      = "https://github.com/login/oauth/access_token"
revoke_url     = "https://api.github.com/applications/{client_id}/token"   # DELETE
scopes         = ["repo", "read:user"]
secret_key     = "GITHUB_TOKEN"      # the `secrets.key` to write
allowed_domain = "api.github.com"    # egress allowlist hint for the connector
header_format  = "Bearer {token}"    # informational; MCP server controls actual format
```

Client ID/secret operator-owned in `.env` as
`SURROGATE_<PROVIDER>_CLIENT_ID` / `SURROGATE_<PROVIDER>_CLIENT_SECRET`.
Public clients (no client_secret, PKCE-only) are a v2 concern.

## The dance

`dashd` exposes three handlers under `/dash/me/connections/`:

1. **`GET /<provider>/start`**
   - Generate `state=<csrf>` keyed to `caller.sub`, store in-memory
     with 10-min TTL.
   - 302 to `{auth_url}?response_type=code&client_id=…&redirect_uri=…
&scope=…&state=<csrf>&access_type=offline` (for providers that
     require `access_type=offline` to get a refresh_token — google,
     microsoft).

2. **`GET /<provider>/callback?code=…&state=…`**
   - Validate `state` against in-memory store; reject on mismatch.
   - POST `{token_url}` with `grant_type=authorization_code&code=…
&client_id=…&client_secret=…&redirect_uri=…`.
   - Persist response: `value=access_token`, `refresh_val=refresh_token`,
     `expires_at=now()+expires_in`, `scope_list=scope`, `provider=<p>`.
   - Redirect to `/dash/me/connections/`.

3. **`DELETE /<provider>`**
   - DELETE the `secrets` row.
   - If `revoke_url` set, POST a revocation request (best-effort;
     log + continue on failure).

CSRF state is in-memory because the TTL is short and the dance is
single-process per dashd instance. Multi-instance dashd would push
this to a `surrogate_oauth_state` table; out of scope for v1.

## Refresh

Two refresh strategies, picked by the broker on each call:

1. **Proactive** (preferred): `expires_at − now() < 60s`. Broker
   POSTs to `token_url` with `grant_type=refresh_token&refresh_token=…
&client_id=…&client_secret=…`, updates row, then spawns the
   subprocess with the new `access_token`. ~30 LOC.

2. **Reactive on 401**: if the subprocess returns a `tools/call`
   error containing a `401` shape, broker refreshes and retries
   once. Bounded retry: never twice. Costs one wasted subprocess
   spawn per refresh but covers providers with sloppy `expires_in`
   reporting.

v1 ships proactive only; reactive added if a provider misbehaves.

Refresh runs **at call time, not in a background worker**. Background
refresh wastes cycles (most users never use most providers per turn)
and adds a second source of truth for "the latest access_token".

## User-facing surface

```
/dash/me/connections/
  └── GET  /                    list connected providers + status
      • "GitHub: connected (read repo, read user) — expires in 3h, refresh on use"
      • "Linear: not connected"  → [Connect Linear]
      POST /<provider>/start    (form post; 302 to provider)
      DELETE /<provider>        (form delete; revoke at provider; remove row)
```

The same surface that `/dash/me/secrets` (9/11 M3) uses; the
distinguisher is whether the provider has a registered surrogate
config. UI lists both groups: "OAuth connections" (this spec) and
"Pasted tokens" (9/11 M3).

## Acceptance

1. **Connect**: Alice clicks "Connect GitHub" on `/dash/me/connections/`.
   Round-trip returns her to the page; GitHub row in `secrets` with
   `provider='github'`, non-NULL `expires_at`, non-NULL `refresh_val`.
2. **Tool call uses fresh token**: Alice invokes `github_create_pr`
   (via 9/11 broker). Broker resolves her GitHub row, sees
   `expires_at` is in the future, spawns github-mcp with current
   `value`. PR is created.
3. **Refresh on near-expiry**: time-travel `expires_at` to `now+30s`.
   Alice invokes `github_create_pr` again. Broker hits refresh
   endpoint, updates row, spawns with new token. Audit row records
   `refresh='ok'`.
4. **Refresh failure**: revoke Alice's refresh_token at github.
   Alice invokes `github_create_pr`. Refresh returns 400. Broker
   surfaces a structured error to the agent; row marked
   `expires_at=NULL,refresh_val=NULL` so the agent gets "no token,
   reconnect" on subsequent calls.
5. **Per-user isolation**: Bob's `github_create_pr` in the same Slack
   channel spawns a different subprocess with Bob's token. Neither
   sees the other's. (Inherits from 9/11.)
6. **Revoke**: Alice's `DELETE /dash/me/connections/github` removes
   the row; github's revocation endpoint receives the POST; next
   tool call errors "no token".

## Implementation plan

| M   | Work                                                                              | LOC  |
| --- | --------------------------------------------------------------------------------- | ---- |
| M0  | `auth/surrogate.go` — provider registry loader + `Authorize`/`Exchange`/`Refresh` | ~150 |
| M1  | Schema: `0049-surrogate-oauth.sql` (additive columns)                             | ~10  |
| M2  | dashd: `/dash/me/connections/` handlers (start, callback, list, delete)           | ~200 |
| M3  | broker refresh wrapper in gated (proactive, ~60s margin); update row on success   | ~80  |
| M4  | First provider: `auth/surrogate/providers/github.toml` + acceptance test e2e      | ~30  |
| M5  | `cmd/arizuko/surrogate.go` — operator inspector/revoker                           | ~80  |
| M6  | Release: CHANGELOG, migration, version bump                                       | —    |

Total ≈ 550 LOC + ~30/provider. Two daemon-weeks for the framework,
N days per new provider thereafter.

## Out of scope

- **PKCE / public clients** — confidential clients only in v1.
- **Multi-account per provider** — single (user, provider) row;
  "github work" vs "github personal" deferred.
- **Background refresh** — call-time only; no goroutine churn.
- **Provider revocation when refresh succeeds but row deleted** —
  best-effort POST on user-initiated revoke; not on internal cleanup.
- **OAuth-inside-MCP-server pattern** (MCP 2025-03-26 spec) — out
  of scope; arizuko owns the dance for the consistency it gives.

## Cross-references

- [`specs/9/11-crackbox-secrets.md`](11-crackbox-secrets.md) — the
  broker that reads the rows this spec writes.
- [`specs/6/1-auth-standalone.md`](../6/1-auth-standalone.md) — auth
  capability library; surrogate flows are a sibling of identity OAuth.
- [`specs/7/product-slack-team.md`](../7/product-slack-team.md) — the
  product that benefits first: "Alice asks @bot about her PRs" goes
  from PAT-paste to "Connect GitHub" button.
