---
status: spec
---

# proxyd: standalone authenticating gateway

Make `proxyd` a fully generic, config-driven authenticating reverse
proxy. Operators describe their backends and OAuth providers in a
TOML file; proxyd handles login, mints capability tokens via the
shared `auth/` library (or `authd`), and forwards verified requests
to backends with the token attached.

The component should be droppable in front of any HTTP service stack
— arizuko or otherwise. Same blueprint as `auth`: clean library
boundary, HTTP API for management, MCP tool surface for agents that
need to introspect/manage routes.

This spec assumes [1-auth-standalone.md](1-auth-standalone.md) has
landed (or is landing in parallel). proxyd defers all token mint
and verify to the `auth/` library.

## Today's state

- ~600 LOC, single-binary Go service.
- OAuth login flow, JWT mint, reverse proxy, signed identity headers
  — all working.
- 4 references to `folder` in `main.go`; route table for arizuko
  backends hardcoded; some assumptions baked in (slink token paths,
  WebDAV write-block, dashd path).
- No external API for managing routes or sessions; everything is
  config + restart.

## Target shape

### 1. Generic configuration

```toml
listen     = ":8080"
secret_env = "AUTH_SECRET"
session_ttl = "24h"

[auth]
authd_url  = "http://authd:8080"   # or empty to use library mode
mode       = "remote"              # "remote" calls authd, "library" mints in-process

[[provider]]
id           = "google"
type         = "oidc"
client_id    = "..."
client_secret_env = "GOOGLE_OAUTH_SECRET"
discovery_url = "https://accounts.google.com/.well-known/openid-configuration"

[[route]]
prefix         = "/api/"
backend        = "http://backend:9000"
forward_token  = true                  # attach Authorization: Bearer <token>
require_scope  = "api:*"

[[route]]
prefix         = "/admin/"
backend        = "http://admin:9001"
require_scope  = "admin:*"
require_audience = "internal"

[[route]]
prefix         = "/pub/"
backend        = "http://static:9002"
public         = true                  # no auth required

[[route]]
prefix         = "/slink/"
backend        = "http://webd:8080"
token_param    = "token"               # path-segment token, not Bearer
rate_limit     = "30/min/ip"
```

`[auth]` configures whether proxyd uses `authd` over HTTP or the
`auth/` library directly. `[[provider]]` is OAuth config per the
auth spec. `[[route]]` is the actual reverse-proxy table.

### 2. HTTP API for management

```
GET  /v1/routes
GET  /v1/routes/{prefix}              # urlencoded
POST /v1/routes
PATCH /v1/routes/{prefix}
DELETE /v1/routes/{prefix}

GET  /v1/sessions                     # list active sessions (when persisted)
DELETE /v1/sessions/{sub}             # force-logout

GET  /health
GET  /healthz
```

All `/v1/*` require token with `proxyd:admin` scope. Routes can be
added at runtime (no restart) by the operator dashboard or an MCP
tool.

### 3. Per-route auth modes

| Mode                       | Auth required | Token attached          | Use case                              |
| -------------------------- | ------------- | ----------------------- | ------------------------------------- |
| `public = true`            | none          | none                    | static assets, /pub                   |
| default (no flags)         | session JWT   | none (just verified)    | dashboards reading own-user data      |
| `forward_token = true`     | session JWT   | `Authorization: Bearer` | backend services that re-verify       |
| `require_scope = "..."`    | session JWT   | optional                | scope-gated routes                    |
| `require_audience = "..."` | session JWT   | optional                | audience-scoped multi-app deployments |
| `token_param = "<name>"`   | URL token     | injected as Bearer      | slink-style anonymous tokens          |
| `rate_limit = "<spec>"`    | adds limiter  | passthrough             | abuse-resistant public surfaces       |

### 4. Login flow (delegated to auth/authd)

```
GET /auth/login        → redirect to authd or local OAuth flow
GET /auth/callback     → exchange code, set session cookie
POST /auth/logout      → clear cookie
GET /auth/me           → return identity
```

If `[auth].mode = "remote"`, proxyd 302s to `authd_url/auth/login`
with a return-to. If `mode = "library"`, proxyd handles the OAuth
flow itself using the `auth/` library. Operators choose based on
deployment shape.

### 5. MCP tool surface

For agents that need to manage gateway behaviour (operator agents,
dashboard agents, tooling agents):

| Tool             | Purpose                               | Scope          |
| ---------------- | ------------------------------------- | -------------- |
| `list_routes`    | Read the current route table          | `proxyd:read`  |
| `get_route`      | Read one route by prefix              | `proxyd:read`  |
| `add_route`      | Add a route (write to runtime config) | `proxyd:write` |
| `update_route`   | Change auth mode, backend, scope      | `proxyd:write` |
| `delete_route`   | Remove a route                        | `proxyd:write` |
| `list_sessions`  | List active sessions (when persisted) | `proxyd:admin` |
| `revoke_session` | Force-logout a user                   | `proxyd:admin` |
| `list_providers` | Show configured OAuth providers       | `proxyd:read`  |

These tools are thin wrappers over the `/v1/*` HTTP API. Same
implementation, two surfaces.

## What changes from today

| Today                          | After                                                      |
| ------------------------------ | ---------------------------------------------------------- |
| Routes hardcoded in `main.go`  | Routes in TOML, hot-reloadable via `/v1/routes`            |
| Mints JWTs locally             | Calls `auth.Mint` (library) or `authd /v1/tokens` (remote) |
| 4 `folder` refs in `main.go`   | None — folder/tier handled at backend, proxyd is generic   |
| No management API              | `/v1/routes`, `/v1/sessions`, MCP tools                    |
| Slink, WebDAV, dashd hardcoded | All become `[[route]]` entries with the right auth mode    |
| Bound to arizuko backends      | Drop in front of any backend stack                         |

## What this spec is not

- Not a layer-7 firewall. proxyd does authn + simple authz + path
  routing; complex policy (rate limiting beyond per-IP, anomaly
  detection) lives elsewhere.
- Not a service mesh. No service discovery, no mTLS-internal —
  backends are static URLs.
- Not a TLS terminator (today). Reverse-proxy mode assumes someone
  upstream (caddy, nginx, k8s ingress) handles TLS. Adding TLS
  termination is a small addition if needed.
- Not a token issuer beyond what auth/authd provides. proxyd is the
  enforcement point; auth/authd is the authority.

## Implementation phases

1. **TOML route config** — replace hardcoded route table with TOML.
   Existing arizuko deployment ships with the same routes, just
   read from a config file. No behaviour change.

2. **Delegate token mint to auth/authd** — remove proxyd's local
   JWT minting; call `auth.Mint` (library) for now, switch to
   `authd /v1/tokens` once `authd` ships. Stays signed with the
   same `AUTH_SECRET`.

3. **`/v1/routes` HTTP API** — runtime route management. Routes
   added via API persist to a small SQLite (or file) so they
   survive restart, layered over the TOML config.

4. **`/v1/sessions`** — session list + revoke. Requires
   persisting issued tokens (or a revocation list); ties to the
   auth spec's revocation work.

5. **MCP tools** — `list_routes` / `add_route` / etc., wrapping
   `/v1/*`. Built-in MCP server in proxyd.

6. **Strip arizuko-specific code paths** — eliminate the 4 folder
   refs; move slink/WebDAV path-quirks into route flags or backend
   config.

7. **Document for non-arizuko deployment** — `proxyd/README.md`
   gets a "drop me in front of your stack" usage section. Example
   TOML for common deployments (single backend, multi-backend,
   scope-gated admin).

After step 1 + 2 the daemon is data-driven and authority-delegated.
After step 6 it's deployable as a standalone component.

## Code pointers

- `proxyd/main.go` — entry; hardcoded route table moves out.
- `proxyd/routes.go` (likely new) — TOML parser + runtime route store.
- `proxyd/oauth.go` — current OAuth flow; either delegates to authd
  or stays as library-mode handler.
- `proxyd/proxy.go` (or current equivalent) — reverse-proxy core;
  becomes config-driven.
- `auth/` — proxyd consumes mint + verify primitives.
- `proxyd/README.md` — overhauled for standalone usage.

## Open

- **Hot reload of TOML** — file-watch and reload, or only via
  `/v1/routes` API? Lean: API for changes; TOML for boot config.
- **Per-route rate limiting backend** — in-memory only, or
  pluggable (redis)? Lean: in-memory until a multi-instance
  deployment needs more.
- **Slink-style URL tokens** — current behaviour is somewhat
  bespoke; capturing it as `token_param = "..."` covers it but the
  rate-limit + tier logic might leak if not carefully factored.
- **Audience enforcement** — when does proxyd refuse a token
  because `aud` doesn't match? `require_audience` on a route is the
  primary lever; default is "any aud signed by our key OK".
