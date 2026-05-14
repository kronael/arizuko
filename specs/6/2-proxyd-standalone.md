---
status: shipped
shipped: phase-1 (per-daemon TOML routes) in v0.35.0; phase-3 (runtime /v1/routes + MCP) in v0.36.0
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
- 3 active `folder` references in `proxyd/main.go` (L102, L442, L446)
  - 1 comment (L450). Route table for arizuko backends hardcoded in
    `proxyd/main.go:368-511` (`route` method, paths `/dash/`, `/dav/`,
    `/slink/`, `/onboard`, `/slack/`, `/pub/`, `/api/`, etc.). Backend
    addresses injected as env vars from `compose/compose.go:451-486`
    (`DASH_ADDR`, `WEBD_ADDR`, `DAV_ADDR`, `ONBOD_ADDR`, `SLAKD_ADDR`).
    Slink token paths, WebDAV write-block, dashd path quirks all baked
    into the route method.
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

## Per-daemon route declarations (v1 ship target)

The "Target shape" `[[route]]` table above is the **eventual** proxyd
config. The Phase-1 ship is narrower: routes live in each adapter's
`template/services/<name>.toml` as `[[proxyd_route]]` blocks; the
compose generator extracts them at boot. **One renderer, many sinks**:
each daemon owns its own routing entry next to its env, and proxyd's
table is derived, never hand-edited.

### Schema

```toml
# template/services/slakd.toml

image = "arizuko:latest"
entrypoint = ["slakd"]

[environment]
ROUTER_URL = "http://gated:8080"
SLACK_BOT_TOKEN = "${SLACK_BOT_TOKEN}"
SLACK_SIGNING_SECRET = "${SLACK_SIGNING_SECRET}"
LISTEN_ADDR = ":8080"
CHANNEL_SECRET = "${CHANNEL_SECRET}"

[[proxyd_route]]
path = "/slack/"                            # leading slash; trailing
                                            # slash = longest-prefix
                                            # match; bare path = exact
backend = "http://slakd:8080"               # full URL; DNS name + :8080
auth = "public"                              # "public" | "user" | "operator"
gated_by = "SLACK_BOT_TOKEN"                 # optional; drop route if env unset
preserve_headers = [                         # optional; verbatim-pass these
  "X-Slack-Signature",
  "X-Slack-Request-Timestamp",
]
strip_prefix = false                         # optional; default false
```

### Field semantics

- **`path`** — exact match if no trailing slash; longest-prefix match
  if trailing slash. No glob, no regex. Among prefix matches, longest
  wins; ties resolved by load order (filename sort).
- **`backend`** — full URL. Daemon DNS name + `:8080` per the
  unified-port convention (CLAUDE.md "## Build & Test" and
  `compose/compose.go:130-134` healthcheck baseline). No service-mesh
  resolution.
- **`auth`** — one of:
  - `public` — proxyd does no auth; the daemon itself verifies
    (Slack HMAC over raw body, webhook signatures, etc.). Matches
    today's `/slack/` handler at `proxyd/main.go:469-476`.
  - `user` — proxyd requires a valid user session (Bearer JWT or
    `refresh_token` cookie via `tryAuth`, `proxyd/main.go:608-634`);
    injects signed identity headers `X-User-Sub` / `X-User-Name` /
    `X-User-Groups` / `X-User-Sig` per `setUserHeaders`
    (`proxyd/main.go:590-604`). Matches today's `/dash/` and `/api/`.
  - `operator` — proxyd requires the operator/admin role.
    Implementation note: today's `auth.MatchGroups` over `X-User-Groups`
    with `**` is the operator marker; the spec-level check is "scope
    `proxyd:operator` or equivalent grant." Until the capability-token
    work in `1-auth-standalone.md` lands, `operator` may resolve to
    `user` + a grant check at the daemon — record as a known gap.
- **`gated_by`** — single env-var name. If unset or empty at compose-
  generate time, the route is omitted from proxyd's table entirely
  (`/<path>` 404s). Multiple gating env vars → out of scope v1.
  Replaces the today's `if envOr(env, "SLACK_BOT_TOKEN", "") != ""`
  block at `compose/compose.go:464-466`.
- **`preserve_headers`** — explicit allowlist of inbound headers that
  proxyd MUST pass through unmodified. `httputil.ReverseProxy`
  already preserves headers, but proxyd's `stripClientHeaders`
  (`proxyd/main.go:99-106`) deletes proxyd-owned headers on entry;
  `preserve_headers` is the contract that webhook-signing headers
  survive any future filtering pass. proxyd otherwise rewrites the
  `Host` header to the backend's hostname.
- **`strip_prefix`** — default `false`. When `true`, strip the path
  prefix before forwarding (mirrors today's `davProxy` at
  `proxyd/main.go:117-129`). Webhook URLs default to keeping the prefix.

### Out of scope (v1)

No verb allowlist (WebDAV write-block stays a davd-specific in-proxyd
check, `proxyd/main.go:572-588`); no per-route `rate_limit` (slink's
`10/min/ip` at `proxyd/main.go:257` stays hardcoded); no `token_param`
(slink URL-token resolution at `proxyd/main.go:427-453` stays bespoke);
single env-var `gated_by` only (no compound gates).

### Loader model

`compose/compose.go` already iterates `template/services/*.toml`
(`compose.go:198-227`). The loader extension collects every
`[[proxyd_route]]` entry, evaluates `gated_by` against the operator's
`.env`, drops disabled routes, and emits the survivors as one env var
on proxyd:

```
PROXYD_ROUTES_JSON=[{"path":"/slack/","backend":"http://slakd:8080","auth":"public",...},...]
```

v1 ships as a plain JSON array. The envelope `{"routes":[...]}` was
originally specced for namespacing — dropped during implementation as
redundant; sole consumer is proxyd's `LoadRoutes`.

proxyd parses `PROXYD_ROUTES_JSON` at startup. Absence = empty
external table (proxyd's built-in `/auth/*`, `/health`, vhost handling
still serve). Profile-gated daemons (`profile != "minimal"`,
`WEBDAV_ENABLED`, `ONBOARDING_ENABLED` per `compose.go:241-251`)
contribute no routes when absent — their TOMLs aren't read or their
`gated_by` env is unset.

Single source of truth: the per-daemon TOML. Env-var carrier (vs file
mount) is `compose.go`'s call; lean env-var, no new volume.

### Migration

Code that disappears or shrinks:

| Today (file:lines)                                                                            | Change                                                                                                                                    |
| --------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `proxyd/main.go:31-43` `config.{dashAddr, webdAddr, davAddr, viteAddr, onbodAddr, slakdAddr}` | Replaced by `routes []Route` from `PROXYD_ROUTES_JSON`.                                                                                   |
| `proxyd/main.go:60-65` `loadConfig` `EnvOr("DASH_ADDR", ...)` etc.                            | Removed.                                                                                                                                  |
| `proxyd/main.go:251-276` `newServer` per-backend `*httputil.ReverseProxy` fields              | `map[string]*httputil.ReverseProxy` keyed by route path.                                                                                  |
| `proxyd/main.go:368-511` `route` method's hardcoded prefixes                                  | Longest-prefix match against loaded table. Slink, vhost, dav `davAllow`, pub-redirect stay as named handlers attached to matching routes. |
| `compose/compose.go:451-486` `proxydService` per-daemon env injection                         | Single `PROXYD_ROUTES_JSON` injection from collected `[[proxyd_route]]` blocks.                                                           |

TOML blocks to add: `template/services/slakd.toml` (`/slack/`,
`auth=public`, `gated_by=SLACK_BOT_TOKEN`, `preserve_headers=
[X-Slack-Signature, X-Slack-Request-Timestamp]`). Other channel
adapters (teled, discd, whapd, mastd, bskyd, reditd, emaid, linkd,
twitd) expose no inbound proxyd routes today; revisit per-adapter
when they do.

OPEN: core daemons rendered directly by `compose.go` (`dashdService`,
`webdService`, `davdService`, `onbodService`, `vitedService`) either
get `template/core-services/<name>.toml` files with `[[proxyd_route]]`
blocks (orthogonal, one more directory) or stay hardcoded as a static
slice in `proxydService` (smaller, arizuko-internal). Pick at
implementation time; both honour "proxyd reads `PROXYD_ROUTES_JSON`,
nothing else."

### Acceptance

Per-route behaviour tests (`proxyd/main_test.go` or
`tests/standalone/proxyd_test.go`):

- `route_present_when_gated_by_set` / `route_absent_when_gated_by_unset`
  — table presence tracks the `gated_by` env at compose-generate time.
- `auth_public_passes_through` — `auth=public` route receives body
  and `preserve_headers` verbatim (test-computed HMAC == backend-seen).
- `auth_user_required_redirects_to_login` — `auth=user` without a
  valid session: 303 to `/auth/login` + `auth_return` cookie (today's
  `requireAuth`, `proxyd/main.go:645-673`).
- `auth_user_injects_signed_headers` — with session, backend sees
  `X-User-{Sub,Name,Groups,Sig}` per `auth.UserSigMessage`.
- `preserve_headers_respected` — listed headers verbatim; others may
  be normalised by httputil.
- `strip_prefix_off_by_default` / `strip_prefix_on_strips` — `/slack/`
  - `events` arrives `/slack/events`; `/dav/` + `strip_prefix=true`
  - `/dav/x` arrives `/x` (mirrors `proxyd/main.go:117-129` davProxy).
- `longest_prefix_wins` — routes `/api/` and `/api/special/` both
  match; `/api/special/foo` hits `/api/special/` backend.

End-to-end: `make smoke` on `krons` keeps green; existing
dash/webd/slink/dav surfaces unchanged.

## Decisions

- **`[auth].mode`** locked to `library` for v1; `remote` (call `authd`)
  is a future hook left in the TOML schema but not implemented. Matches
  the bucket-index decision to defer `authd` (see `specs/6/index.md`
  open Q3 and `1-auth-standalone.md`).
- **Boot config vs runtime mutation**: boot routes via TOML
  (`[[proxyd_route]]` per-daemon + `PROXYD_ROUTES_JSON`). Runtime
  changes via `/v1/routes` only; no file-watch / hot-reload of TOMLs.
- **Rate-limit backend**: in-memory only. Today's slink limiter
  (`proxyd/main.go:278-317`) is the model; multi-instance horizontal
  scaling will trigger a redis-backed swap, not v1.
- **Audience enforcement**: `require_audience` on a route is the
  primary lever; default is "any aud signed by our key OK". Locked.

## Open (post-v1)

- **Slink-style URL tokens** — current behaviour
  (`proxyd/main.go:427-453`) stays bespoke in v1. Generalising to
  `token_param = "..."` is post-v1; the rate-limit + folder-resolution
  logic factors out cleanly but adds schema surface not needed for the
  Phase-1 ship.
- **Core-daemon TOML home** — whether dashd/webd/davd/onbod/vited get
  `template/core-services/<name>.toml` files with `[[proxyd_route]]`
  blocks, or stay hardcoded as a static slice in `compose.go`'s
  `proxydService`. Pick at implementation time; either way proxyd
  reads `PROXYD_ROUTES_JSON` only.
