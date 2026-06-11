---
status: draft
depends: [1-cockpit-index]
---

# proxyd dashboard — live routes, denials, auth transit

Architecture, routing, auth, theme per [`6/1`](1-cockpit-index.md).
This spec adds only proxyd's pages + show/control matrix.

**What proxyd can actually mutate at runtime:** the `proxyd_routes`
table — full CRUD already exists (`GET/POST/PATCH/DELETE /v1/routes`,
`proxyd/resource.go:300` `routesResourceDecl`). Mutations are durable
and visible to the next request without restart: there is **no route
cache and therefore no "reload"** — every request reads the table
fresh (spec 5/36 no-cache, `proxyd/resource.go:62` `snapshot`).
Everything else (hand-wired surfaces `/auth/*` `/pub/*` `/priv/*`
`/dav/*` `/chat/` `/hook/`, `TRUSTED_PROXIES`, `PUB_REDIRECT_URL`,
vhost aliases) is boot config — shown read-only, not mutable here.

## Purpose

See what the front door is doing — effective route table, who is being
denied and why, whether identity transit is signed — and edit the one
thing proxyd owns at runtime: routes.

## Pages

| Page         | Route                  |
| ------------ | ---------------------- |
| overview     | `/dash/proxyd/`        |
| routes       | `/dash/proxyd/routes`  |
| denials      | `/dash/proxyd/denials` |
| auth transit | `/dash/proxyd/transit` |

## Show

**overview** — route count + boot source (db vs env-seed,
`loadInitialRoutes`, `proxyd/main.go:232`); denial counters since boot
(by class, see denials); transit one-liner (signed / unsigned);
upstream health summary (n of m backends answering).

**routes** — the effective table, straight from
`store.AllProxydRoutes`: path, backend, auth mode
(`public|user|operator` — `validAuth`, `proxyd/routes.go:12`),
strip_prefix, preserve_headers, gated_by (display-only: compose-time
metadata, never evaluated at runtime — `proxyd/routes.go:22`). Per
route: a live backend probe dot (see upstreams below). Match semantics
caption: longest-prefix, trailing-slash = prefix / bare = exact
(`MatchRoute`, `proxyd/routes.go:67`). A second read-only table lists
agent-registered `web_routes` (path_prefix, access, redirect_to —
`webSnapshot`, `proxyd/resource.go:94`); proxyd only honours these on
`/pub/*`, it does not own their mutation (agents write them via the
`set_web_route` MCP tool).

**denials** — recent denials with reason. Today these exist only as
slog warns + audit web events; the page needs the in-memory ring
(Required work). Denial classes, each tagged at its emit site:

- `auth_denied` — no valid credential / auth secret unset
  (`requireAuth`, `proxyd/main.go:855` and `:864`)
- `dav_forbidden` — group containment failed (`davRoute`,
  `proxyd/main.go:712`)
- `dav_blocked` — write to `logs/` / `.env` / `*.pem` / `.git`
  (`davAllow`, `proxyd/main.go:718`)
- `rate_limited` — anon chat DoS shield 429 (`dispatchRouteToken`,
  `proxyd/main.go:651`; limiter `proxyd/main.go:331`)

Row shape: ts, class, method, path, attempted sub (if any), remote IP.
Plus per-class counters since boot.

**auth transit** — live identity-path state: JWKS loaded (key count;
nil = HS256-only — `proxyd/main.go:907`), `service:proxyd` token
source present (nil = **identity forwarded unsigned**, the local-dev
warning at `proxyd/main.go:928` — render as a `warn` banner in prod),
HS256 `AUTH_SECRET` set, `TRUSTED_PROXIES` CIDRs, `HOSTING_DOMAIN` +
vhost aliases, `PUB_REDIRECT_URL` + its cached probe state
(`pubRedirect.reachable`, `proxyd/main.go:201`).

## Control

| Affordance            | `/v1` verb                 | Status     | Danger                                                                                                                                          |
| --------------------- | -------------------------- | ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| create route          | `POST /v1/routes`          | exists     | **`.btn-danger`** — a bad route shadows a daemon's surface                                                                                      |
| edit route            | `PATCH /v1/routes/{path}`  | exists     | **`.btn-danger`** — flipping `auth` to `public` exposes a backend                                                                               |
| delete route          | `DELETE /v1/routes/{path}` | exists     | **`.btn-danger`** — 404s a live surface; idempotent (204)                                                                                       |
| reload routes         | —                          | —          | **non-goal**: nothing to reload — per-request DB read (spec 5/36) means every mutation is already live                                          |
| enable/disable route  | —                          | —          | **non-goal**: no `enabled` column exists; the honest disable is DELETE (the route row IS the registration, per `6/1`). Don't invent a soft flag |
| clear denial counters | `DELETE /v1/denials`       | **to add** | no — in-memory ring + counters only, nothing durable                                                                                            |
| edit web_routes       | —                          | —          | **non-goal**: agent-owned via `set_web_route`; proxyd is a reader                                                                               |

Route mutations reuse the existing handler + validation verbatim
(`routesResource.handle`, `proxyd/resource.go:178`; `validateRoute`,
`proxyd/resource.go:141`) — same path-conflict 409, same auth-value
check, same in-tx audit row.

## Required `/v1` work

- **Denial ring**: a fixed-size in-memory ring buffer (e.g. last 200)
  - per-class counters, appended at the four emit sites above. Exposed
    as `GET /v1/denials` (rows + counters) and `DELETE /v1/denials`
    (reset). Live process state — exactly the `6/1` rationale for
    `/v1`-not-DB; lost on restart by design (the audit web events remain
    the durable record).
- **Upstream probe**: `GET /v1/upstreams` — for each **distinct**
  route backend, a request-time `GET <backend>/health` probe
  (`context.WithTimeout` 500ms each, run concurrently, mirroring
  `6/1` hub probing). Response: backend, ok/err, latency, the routes
  pointing at it. No probe loop, no caching.
- **Transit status**: `GET /v1/transit` — the auth-transit page's
  datum set (jwks key count, service-token present, hs256 set,
  trusted-proxy CIDRs, hosting domain, pub-redirect probe state).
  Read-only snapshot of boot config + live nils.

No new dashboard API beyond these; the routes face already exists.

## Auth

Per `6/1`: proxyd carries its own `/dash/proxyd/` route (`auth:
"user"`) and gates with `auth/dashauth.go` operator + same-origin CSRF
on writes. Note the existing `/v1/routes` REST face is reachable only
via webd's transit (caller pinned to `service:webd`,
`channelTrusted`, `proxyd/resource.go:357`) — the dash pages do NOT
go through that gate; they invoke the **same** registered `/v1/routes`
resource handler (`routesResource.handle`) and the new ring/probe
helpers in-process — same validation, same semantics, one handler with
two faces (`6/1` read-path), not a parallel non-`/v1` path — leaving
the webd pin untouched.

## HTMX fragments

- `GET /dash/proxyd/x/routes` — route table rows (+ probe dots).
- `GET /dash/proxyd/x/denials?class=` — denial rows, filterable,
  poll-refreshed.
- `GET /dash/proxyd/x/upstreams` — probe results (this fragment IS the
  slow call; the page shell renders instantly and swaps it in).
- `POST /dash/proxyd/x/routes` / `PATCH|DELETE
/dash/proxyd/x/routes/{path}` — mutate + return refreshed rows.

## Non-goals

Per `6/1`, plus: no access-log browser (journald/audit owns request
logs; the ring holds denials only); no rate-limiter tuning UI
(`CHAT_ANON_DOS_RPM` is env); no TLS/cert anything; no editing of
hand-wired surfaces (`/pub/`, `/dav/` policy, vhost derivation — code

- env, not data).

## Acceptance

- `/dash/proxyd/` route registered in `compose.go`'s default route
  list (proxyd is a core daemon, no service TOML — `6/1` Routing);
  hub tile probes `GET /health` (`proxyd/main.go:494`).
- Routes page matches `GET /v1/routes` exactly (same handler, third
  face — spec 5/5); creating a route from the page makes the next
  request through proxyd honour it with **no restart**.
- Editing a route to `auth: public` and deleting a route both demand
  the danger confirm.
- A 401 redirect-to-login, a dav write-block, and an anon-chat 429
  each produce one denial row with the right class within one
  fragment refresh; `DELETE /v1/denials` zeroes the counters.
- With `AUTHD_SERVICE_KEY` unset, the transit page (and overview)
  shows the unsigned-identity warning banner.
- A stopped backend flips its upstream probe to `err` without
  affecting page render time beyond the 500ms probe bound.
- Non-operator caller gets the theme 403 banner on every page.
