---
status: draft
depends: [5/17-openapi-mcp, 5/7-proxyd-standalone, 1-auth-standalone]
---

# Operator cockpit — architecture + contract

**Every daemon serves its own dashboard from its own
`/dash/<daemon>/` HTMX namespace, rendering its own source, reading
and writing only through its own `/v1` surface. A lean `dashd` hub
probes and links them — an AWS-Console "Services" home, much leaner.**
One renderer per daemon, one hub. This spec is the anchor; `7/2`,
`7/3`, `7/11` and `7/12` point back here for architecture, routing,
auth, theme, and non-goals, and add only their own page lists +
show/control matrices.

## The shape

- **Hub** — `dashd` owns `GET /dash/services/`: a tile grid, one tile
  per daemon, AWS-Console style. Each tile probes `<daemon>:8080/health`,
  shows `ok|warn|err` + a one-line summary, and links to
  `/dash/<daemon>/`. The hub renders no daemon-specific runtime view
  and holds no duplicate control renderer. Cross-cutting operator pages
  that span daemons stay in `dashd` (`7/2`).
- **Per-daemon dashboard** — every daemon serves `/dash/` on `:8080`
  (the universal daemon port, per CLAUDE.md). It owns its runtime
  views, its resource detail pages, and its dangerous controls. Source
  (handlers + templates) lives in the daemon, beside the `/v1` handlers
  that already own the same data.
- **Public URL shape** — hub at `/dash/services/`; daemon at
  `/dash/<daemon>/...`; HTMX partials under `/dash/<daemon>/x/...`.

## Read-path: `/v1` only, no direct DB

**Every datum a dashboard shows or mutates flows through the owning
daemon's `/v1` surface. No dashboard — hub or per-daemon — reads a
SQLite DB directly.** This retires `dashd`'s current direct reads of
`routd.db`/`onbod.db`/`messages.db`.

Why strict, not hybrid: live process state (`runed` active runs,
`routd` breaker + queue depth, adapter session health, `timed`
next-tick, `proxyd` denials, `crackbox` blocked egress) exists only in
daemon memory — a DB snapshot can't see it, and two read paths to one
consumer drift silently (CLAUDE.md "One renderer, many sinks").
`specs/5/17` already establishes daemon ownership through `/v1`;
dashboards follow the same ownership line.

Consequences:

- A per-daemon dashboard reads via its own in-process handlers / `/v1`
  helpers — same struct the REST/MCP faces already serve.
- The hub tiles probe `GET /health` only.
- Cross-cutting `dashd` pages (`7/2`) call the **owning daemon's `/v1`**
  over HTTP, presenting dashd's own authd-minted service bearer (the
  owning daemon verifies it as a service principal), never the DB.
- If a dashboard needs a datum no `/v1` endpoint exposes: first extend
  the existing `/v1/<resource>`; failing that, add a minimal
  daemon-owned read endpoint (`/v1/<thing>/status`). `7/3` and `7/12`
  list the `/v1` additions each daemon requires.
- No new "dashboard API" separate from `/v1`. The HTML page is a third
  face on the same handler set (`specs/5/17`), not a fourth surface.

## Routing

proxyd carries one route per daemon dashboard, registered statically
(no autodiscovery), per `specs/5/7-proxyd-standalone.md`:

```
/dash/            -> dashd:8080         (hub + cross-cutting pages)
/dash/<daemon>/   -> <daemon>:8080/dash/
```

Packaged daemons (channel adapters, ttsd, crackbox) ship their
`/dash/<daemon>/` entry in a `template/services/<daemon>-routes.json`
array beside their `.yml` compose fragment; compose collects the
survivors after `gated_by` evaluation into `PROXYD_ROUTES_JSON`
([`compose/compose.go`](../../compose/compose.go)
`collectProxydRoutes`) — no edit to `proxyd/main.go`. Core daemons
(routd, runed, authd, proxyd, onbod, timed, dashd) register in
`coreProxydRoutes` in the same file; `/dash/onbod/` and `/dash/timed/`
are already there, ordered ahead of the `/dash/` catch-all because
matching is longest-prefix. Either way: a daemon with no route gets no
hub tile; the route entry IS the registration, and there is no
autodiscovery.

## Auth: two gates, shared helper

Identical to today's `/dash/` gate, applied uniformly
(`specs/1-auth-standalone.md`, CLAUDE.md "Auth is a uniform
middleware"):

1. **proxyd transit gate** on `/dash/<daemon>/` — `auth: "user"`. proxyd
   authenticates the operator, strips any client-supplied identity
   headers, re-stamps `X-User-Sub` / `X-User-Groups`, and attaches an
   authd-minted `service:proxyd` ES256 bearer as transit proof.
2. **Daemon-side gate** — the daemon verifies the `service:proxyd`
   bearer with `auth.ProxydTransit(r, ks)` (`auth/middleware.go`) and
   only then trusts the stamped `X-User-*`; a shared `auth/dashauth.go`
   helper (promoted from `dashd/authz.go`'s `requireAdmin`) then runs
   the one operator/scope decision over `X-User-Groups`. CSRF for
   writes lives in the same helper (same-origin check). `ks==nil`
   (`AUTHD_URL` unset, local-dev) falls open.

Policy: all daemon dashboards are **operator-only** by default. If a
page later becomes non-operator, scope it per page inside the daemon —
never by splitting proxyd route topology. A deep-page 403 renders the
theme error banner (not plain text); the hub tile then shows `warn`
(daemon up, caller lacks scope) vs `err` (probe failed).

## Theme

Every daemon imports `github.com/kronael/arizuko/theme` and renders via
`theme.Page()` / `theme.Head()`. hub.css is the fixed visual identity —
borrow AWS Console's IA/structure only, never its look.

- **Hub = tiles.** Service grid, status dot, version, one-line summary.
- **Daemon = tables + detail panes.** Depth, not overview — the
  `tiles`-vs-`table` split the CSS already encodes.
- **Sticky topnav + breadcrumb** — `hub › <daemon> › <page>`; "arizuko
  hub" returns to `/dash/services/`; a dropdown lists known daemons for
  fast pivot. Each daemon passes its own name as breadcrumb root.

Specs reference theme by name only — no CSS prose, no px figures.

## Hub probing

Request-time probe to each `<daemon>:8080/health`, `context.WithTimeout`
500ms per probe so one slow daemon can't stall the render. No cached
probe loop initially; add caching only if render latency becomes a
problem. Tile content: daemon name, `ok|warn|err`, one-line health
summary if the payload carries one, link to `/dash/<daemon>/`.

## Non-goals

- Not a metrics system; not a Prometheus/Grafana replacement. Tiles
  show binary health + version, not time-series.
- No autodiscovery — the proxyd route entry is the registration.
- No config DSL, no SPA, no frontend build step. HTMX +
  server-rendered HTML only.
- No websocket/SSE requirement.
- No new dashboard API separate from `/v1`.
- No cross-daemon write orchestration in the hub.
- No single global status screen that unifies every page.

## Reconciliation of prior specs

- The former `11/18-daemon-dashboards.md` — superseded by this spec and
  deleted 2026-08-02. Its routing/auth/theme/hub model carried over
  almost verbatim; the one change is the read-path (it left `dashd`'s
  direct-DB pages in place — this spec migrates them to `/v1`).
- `specs/3/d-dashboards.md` (shipped) — historical input; the tile
  portal it shipped becomes the hub (`7/2`).
- `specs/4/Q-dash-memory.md` (shipped) — the memory browser is a
  retained cross-cutting `dashd` page (`7/2`), re-homed to read via the
  owning daemon's `/v1`.
- `specs/4/V-dashd-acl-ui.md` (shipped) — the ACL UI is a retained
  cross-cutting `dashd` page (`7/2`), reading `authd`'s `/v1`.

## The spec set

| Spec   | Covers                                                          |
| ------ | --------------------------------------------------------------- |
| `7/1`  | this — architecture, read-path, routing, auth, theme, non-goals |
| `7/2`  | dashd hub + retained cross-cutting operator pages               |
| `7/3`  | the ten core/edge daemon dashboards, one table + the residue    |
| `7/11` | adapter dashboard contract — shared page grammar + health model |
| `7/12` | the ten channel adapters, instantiating that contract           |

A per-daemon section carries only: pages, the show/control matrix, the
`/v1` additions it needs, and reasoning that does not generalise. No
repeated architecture text, no implementation tutorial, no CSS prose
beyond "use theme" — point back here.

## Build order

1. `7/1` (this) + `7/2` hub — land architecture + the empty tile grid.
2. `7/11` adapter contract — the repeated pattern, defined once.
3. Finish onbod (gates + invites pages) and timed (task pages): both
   already serve a dashboard and need no new `/v1` work.
4. runed, proxyd — operational depth.
5. routd, authd — the big control surfaces.
6. `7/12` adapters — repeated pattern work.
7. webd/ttsd, crackbox.

## Reusable pieces

- `theme` — tiles (hub) vs tables/detail (daemon), status dots, banner,
  crumbs, theme-toggle, `.btn-danger`.
- `auth/dashauth.go` — promoted `requireAdmin` + same-origin CSRF for
  writes.
- `chanreg/health.go` — canonical adapter liveness semantics (`7/11`).
- `onbod/dash.go` + `timed/dash.go` — the two shipped dashboards; copy
  their operator gate and their "one writer per resource, two faces"
  shape rather than inventing a third.
- existing `GET /health` + `GET /openapi.json` on every daemon.
- existing `/v1` handlers (`specs/5/17`).

## Code pointers

- [`dashd/main.go`](../../dashd/main.go) — current monolithic route
  table; becomes the hub + cross-cutting pages.
- [`dashd/authz.go`](../../dashd/authz.go) — `requireAdmin`, promoted to
  `auth/dashauth.go`.
- [`theme/theme.go`](../../theme/theme.go) — `Head()` / `Page()`.
- [`auth/middleware.go`](../../auth/middleware.go) — `ProxydTransit`
  (service-bearer transit proof; gate the daemon-side dash check on it).
- [`chanreg/health.go`](../../chanreg/health.go) — adapter health.
- [`specs/5/17-openapi-mcp.md`](../5/17-openapi-mcp.md) — the
  HTML page is a third face on the same `/v1` handler set.
- [`specs/5/7-proxyd-standalone.md`](../5/7-proxyd-standalone.md) —
  route field semantics and `gated_by` evaluation. Registration is a
  `services/<daemon>-routes.json` array or `coreProxydRoutes`; the
  `.toml` converter it describes is retired (root `CLAUDE.md`
  "Adding a channel adapter" is current).
