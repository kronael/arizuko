---
status: partial
depends: [5/17-openapi-mcp, 5/7-proxyd-standalone, 5/1-auth-standalone]
---

# Operator cockpit — architecture + contract

**Every daemon serves its own dashboard from its own
`/dash/<daemon>/` HTMX namespace, rendering its own source, reading
and writing only through its own `/v1` surface. A lean `dashd` hub
probes and links them — an AWS-Console "Services" home, much leaner.**
One renderer per daemon, one hub. This spec is the anchor; `6/29`,
`6/30`, `6/31` and `6/32` point back here for architecture, routing,
auth, theme, and non-goals, and add only their own page lists +
show/control matrices.

## The shape

- **Hub** — `dashd` owns `GET /dash/services/`: a tile grid, one tile
  per daemon, AWS-Console style. Each tile probes `<daemon>:8080/health`,
  shows `ok|warn|err` + a one-line summary, and links to
  `/dash/<daemon>/`. The hub renders no daemon-specific runtime view
  and holds no duplicate control renderer. Cross-cutting operator pages
  that span daemons stay in `dashd` (`6/29`).
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
`routd.db`/`onbod.db`/`runed.db`. (`messages.db` is the retired monolith and
is already refused at boot, `dashd/main.go:479`.)

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
- Cross-cutting `dashd` pages (`6/29`) call the **owning daemon's `/v1`**
  over HTTP, presenting dashd's own authd-minted service bearer (the
  owning daemon verifies it as a service principal), never the DB.
- If a dashboard needs a datum no `/v1` endpoint exposes: first extend
  the existing `/v1/<resource>`; failing that, add a minimal
  daemon-owned read endpoint (`/v1/<thing>/status`). `6/30` and `6/32`
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
`/dash/<daemon>/` entry in a `template/services/<daemon>.yaml`
array beside their `.yml` compose fragment; compose collects the
survivors after `gated_by` evaluation
([`compose/compose.go`](../../compose/compose.go)
`collectProxydRoutes`) and `arizuko generate` writes them into
`proxyd_routes` — no edit to `proxyd/main.go`. Core daemons
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

## Build state (2026-08-29)

Signed off and started. The contract above is settled; what exists:

- **Auth (both gates)** — shipped. `auth/dashauth.go` is the one operator
  decision every daemon dashboard applies: `RequireOperator` / `RequireWrite`,
  the themed 403, and the same-origin CSRF check for writes. It replaced three
  drifted copies (timed rendered the themed page, onbod rendered the bare
  string `forbidden`, dashd carried the `**` shortcut inside `callerScope`);
  timed and onbod now call it. dashd's `requireAdmin` stays separate on purpose
  — it answers folder-scoped admin, not operator.
- **Routing** — shipped for the daemons that have a page. `coreProxydRoutes`
  carries `/dash/onbod/`, `/dash/routd/` and `/dash/timed/` ahead of the
  `/dash/` catch-all, and `TestDaemonDashRoutesPrecedeTheCatchAll`
  (`compose/reserved_routes_test.go`) pins that ordering, the `user` auth mode
  and the "not backed by dashd" rule for every route added later.
- **Per-daemon dashboards — the core set is COMPLETE.** Eight of nine serve
  their own: onbod (admission queue), timed (overview), routd (overview + retry
  - replay), runed (capacity + run table + kill), authd (keys + sessions +
    revoke), proxyd (route table + create/delete), webd (SSE hub) and ttsd
    (backend + probe). davd is the deliberate ninth — no `/health`, nothing to
    render — and stays a hub tile probed at `GET /`. routd is the
    reference the rest copy: handlers in the owning daemon, gated by
    `RequireOperator` (reads) and `RequireWrite` (mutations), reading the same
    in-process readers its `/v1` handlers read.
- **Four migrations done** — routd's, runed's, authd's and proxyd's pages have
  LEFT dashd. `dashd/routd_page.go`
  is deleted, and routd owns the two controls it carried, over its own
  `ClearErrored` writer instead of dashd's raw `UPDATE` on `routd.db`. This is
  the shape the rest follow, and it carries a trap worth stating: proxyd matches
  longest-prefix, so routing `/dash/<daemon>/` moves every POST under it too —
  the replacement must answer the controls BEFORE the route lands, or the
  operator gets dead buttons.
- **The hub links what is served.** Its `Built` flag is checked against the
  union of dashd's own mux and the proxyd route table; asking only about dashd's
  mux was the pre-federation question and it hid two working dashboards (onbod,
  timed) whose tiles rendered as plain text while the hub was their only nav
  path.
- **Hub** — `/dash/services/` exists and probes every daemon concurrently with
  a 500ms per-tile timeout, as specified. Its tile list and probe model are
  done; what remains is that six of its tiles still point at pages dashd
  renders.
- **Adapters — the shared renderer is SHIPPED** (`6/31`). `chanlib/dash.go` is
  mounted inside `NewAdapterMux`, so all seven Go adapters serve
  `/dash/<adapter>/` with no per-adapter code, each registered by its own
  `template/services/<name>.yaml` route gated on the adapter's credential.
- **What is left.** The per-adapter delta sections (`6/32`), the two TypeScript
  adapters that cannot import chanlib (whapd, twitd), the social/chat boundary
  (`6/33`), and `6/29`'s real debt — dashd's remaining direct-DB reads, which
  are on the CROSS-CUTTING pages (tasks, routes, groups, grants, usage, route
  tokens) that `6/29` keeps central and requires to call the owning daemon's
  `/v1` instead.

Status stays `partial` until the hub exists and the per-daemon set is complete
— a contract with three of nine implementations is not shipped.

## Reconciliation of prior specs

- The former `11/18-daemon-dashboards.md` — superseded by this spec and
  deleted 2026-08-02. Its routing/auth/theme/hub model carried over
  almost verbatim; the one change is the read-path (it left `dashd`'s
  direct-DB pages in place — this spec migrates them to `/v1`).
- `specs/3/d-dashboards.md` (shipped) — historical input; the tile
  portal it shipped becomes the hub (`6/29`).
- `specs/4/Q-dash-memory.md` (shipped) — the memory browser is a
  retained cross-cutting `dashd` page (`6/29`), re-homed to read via the
  owning daemon's `/v1`.
- `specs/4/V-dashd-acl-ui.md` (shipped) — the ACL UI is a retained
  cross-cutting `dashd` page (`6/29`), reading `authd`'s `/v1`.

## The spec set

| Spec   | Covers                                                          |
| ------ | --------------------------------------------------------------- |
| `6/28` | this — architecture, read-path, routing, auth, theme, non-goals |
| `6/29` | dashd hub + retained cross-cutting operator pages               |
| `6/30` | the ten core/edge daemon dashboards, one table + the residue    |
| `6/31` | adapter dashboard contract — shared page grammar + health model |
| `6/32` | the ten channel adapters, instantiating that contract           |

A per-daemon section carries only: pages, the show/control matrix, the
`/v1` additions it needs, and reasoning that does not generalise. No
repeated architecture text, no implementation tutorial, no CSS prose
beyond "use theme" — point back here.

## Build order

1. `6/28` (this) + `6/29` hub — land architecture + the empty tile grid.
2. `6/31` adapter contract — the repeated pattern, defined once.
3. Finish onbod (gates + invites pages) and timed (task pages): both
   already serve a dashboard and need no new `/v1` work.
4. runed, proxyd — operational depth.
5. routd, authd — the big control surfaces.
6. `6/32` adapters — repeated pattern work.
7. webd/ttsd, crackbox.

## Reusable pieces

- `theme` — tiles (hub) vs tables/detail (daemon), status dots, banner,
  crumbs, theme-toggle, `.btn-danger`.
- `auth/dashauth.go` — promoted `requireAdmin` + same-origin CSRF for
  writes.
- `chanreg/health.go` — canonical adapter liveness semantics (`6/31`).
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
  `services/<daemon>.yaml` manifest or `coreProxydRoutes`; the
  `.toml` converter it describes is retired (root `CLAUDE.md`
  "Adding a channel adapter" is current).
