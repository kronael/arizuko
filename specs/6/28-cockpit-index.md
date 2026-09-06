---
status: shipped
depends: [5/17-openapi-mcp, 5/7-proxyd-standalone, 5/1-auth-standalone]
---

# Operator cockpit — architecture + contract

**Every daemon serves its own dashboard from its own
`/dash/<daemon>/` HTMX namespace, rendering its own source, reading
and writing only through its own `/v1` surface. A lean `dashd` hub
probes and links them — an AWS-Console "Services" home, much leaner.**
One renderer per daemon, one hub. This spec is the anchor; `6/29`,
`6/30`, `6/31` and `6/32` point back here for architecture, routing,
auth and theme, and add only their own page lists + show/control
matrices.

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

**Every datum a dashboard shows about ANOTHER daemon flows through that
daemon's `/v1` surface. No dashboard reads another daemon's SQLite file.**

**Exception: a read the owner REFUSES to serve.** The rule
assumes the owner will serve everything a dashboard shows. One table breaks that
assumption on purpose — `secrets`. routd's secrets resource is write-only by
design (`routd/secrets_resource.go:43`: "Write-only: no read twin, no agent MCP
tool"), and resreg gives REST and MCP from one declaration, so adding the read
face this rule would demand means shipping an API that LISTS SECRETS to satisfy
a dashboard-hygiene rule. That is strictly worse than the direct read it would
replace.

So `/dash/me/secrets` and `/dash/me/env` read `secrets` directly, and that is
correct rather than tolerated. The test is not "does the dashboard read another
daemon's file" but "would the owner serve this if asked": where the owner
refuses on purpose, the refusal is the design and the direct read stays. Any
future exception carries the same burden — a named, deliberate refusal in the
owner's code, not an endpoint nobody got round to writing.

A daemon's own dashboard reading its OWN DB is the topology, not a leak: the
daemon owns that file, migrates it, and is the only writer. Requiring it to
call its own `/v1` to render its own page would be a second reader of one
table for no gain. The rule bites exactly where ownership ends.

**WRITES are governed by CLAUDE.md, not by this spec.** Its
§"Split write-discipline": _FS-mounted daemons (dashd, onbod, CLI) write owned
tables directly to the owner's DB; non-mounted daemons write via the owner's
HTTP API + service token._ dashd is FS-mounted on `routd.db`, so its writes go
direct BY RULE.

The measurement behind the rule: `store.AsUser` (`store/store.go:44`) puts the OPERATOR's sub on the
`audit_log` row, and `dashd`'s `bearerCall` carries only its own service bearer.
Routing an operator mutation through `/v1` would attribute it to
`service:dashd` — the write would succeed and the audit trail would quietly
stop naming who acted. `auth.ProxydTransit` cannot help: it accepts
`sub.Sub == ProxydTransitSub` exactly, so admitting dashd would widen
identity-stamping from one daemon to two.

So the read path federates and the write path does not, deliberately. A
per-daemon dashboard writing its OWN daemon's tables in-process (routd's
retry/replay, proxyd's route CRUD, authd's revoke) is the same rule read from
the other side — those daemons own what they write.

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
(`specs/5/1-auth-standalone.md`, CLAUDE.md "Auth is a uniform
middleware"):

1. **proxyd transit gate** on `/dash/<daemon>/` — `auth: "user"`. proxyd
   authenticates the operator, strips any client-supplied identity
   headers, re-stamps `X-User-Sub` / `X-User-Groups`, and attaches an
   authd-minted `service:proxyd` ES256 bearer as transit proof.
2. **Daemon-side gate** — the daemon verifies the `service:proxyd`
   bearer with `auth.ProxydTransit(r, ks)` (`auth/middleware.go`) and
   only then trusts the stamped `X-User-*`; the shared `auth/dashauth.go`
   helper then runs the one operator/scope decision over `X-User-Groups`. CSRF for
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
probe loop; add caching only if render latency becomes a problem. Tile
content: daemon name, `ok|warn|err`, one-line health summary if the
payload carries one, link to `/dash/<daemon>/`.

## What exists (2026-08-30)

- **Auth (both gates).** `auth/dashauth.go` is the one operator decision
  every daemon dashboard applies: `RequireOperator` / `RequireWrite`, the
  themed 403, and the same-origin CSRF check for writes; timed and onbod call
  it. dashd's `requireAdmin` stays separate on purpose — it answers
  folder-scoped admin, not operator.
- **Routing.** `coreProxydRoutes` carries `/dash/onbod/`, `/dash/routd/` and
  `/dash/timed/` ahead of the `/dash/` catch-all, and
  `TestDaemonDashRoutesPrecedeTheCatchAll` (`compose/reserved_routes_test.go`)
  pins that ordering, the `user` auth mode and the "not backed by dashd" rule
  for every route added later.
- **Per-daemon dashboards — the core set is COMPLETE.** Eight of nine serve
  their own: onbod (admission queue), timed (overview), routd (overview + retry
  - replay), runed (capacity + run table + kill), authd (keys + sessions +
    revoke), proxyd (route table + create/delete), webd (SSE hub) and ttsd
    (backend + probe). davd is the deliberate ninth — no `/health`, nothing to
    render — and stays a hub tile probed at `GET /`. routd is the
    reference the rest copy: handlers in the owning daemon, gated by
    `RequireOperator` (reads) and `RequireWrite` (mutations), reading the same
    in-process readers its `/v1` handlers read, and writing its two controls
    over its own `ClearErrored` writer.
- **Hub.** `/dash/services/` probes every daemon concurrently with a 500ms
  per-tile timeout and links what is served, read from the live proxyd route
  table (`6/29`).
- **Adapters.** The shared renderer `chanlib/dash.go` is mounted inside
  `NewAdapterMux`, so all eight Go adapters serve `/dash/<adapter>/` with no
  per-adapter code, each registered by its own `template/services/<name>.yaml`
  route gated on the adapter's credential (`6/31`).

**Status: shipped 2026-08-30.** The hub exists, the per-daemon set is complete
(eight dashboards plus davd's recorded abstention), all ten channel adapters
serve the shared contract, and no dashboard reads another daemon's SQLite file.
The write
axis is governed by CLAUDE.md's split write-discipline, above.

## The spec set

| Spec   | Covers                                                          |
| ------ | --------------------------------------------------------------- |
| `6/28` | this — architecture, read-path, routing, auth, theme            |
| `6/29` | dashd hub + retained cross-cutting operator pages               |
| `6/30` | the nine core/edge daemon dashboards, one table + the residue   |
| `6/31` | adapter dashboard contract — shared page grammar + health model |
| `6/32` | the ten channel adapters, instantiating that contract           |

A per-daemon section carries only: pages, the show/control matrix, the
`/v1` additions it needs, and reasoning that does not generalise. No
repeated architecture text, no implementation tutorial, no CSS prose
beyond "use theme" — point back here.

## Reusable pieces

- `theme` — tiles (hub) vs tables/detail (daemon), status dots, banner,
  crumbs, theme-toggle, `.btn-danger`.
- `auth/dashauth.go` — the operator gate + same-origin CSRF for writes.
- `chanreg/health.go` — canonical adapter liveness semantics (`6/31`).
- `onbod/dash.go` + `timed/dash.go` — copy their operator gate and their
  "one writer per resource, two faces" shape rather than inventing a
  third.
- existing `GET /health` + `GET /openapi.json` on every daemon.
- existing `/v1` handlers (`specs/5/17`).

## Code pointers

- [`dashd/main.go`](../../dashd/main.go) — the hub + cross-cutting pages.
- [`dashd/authz.go`](../../dashd/authz.go) — `requireAdmin`, the
  folder-scoped admin gate (distinct from `auth/dashauth.go`'s operator
  gate).
- [`theme/theme.go`](../../theme/theme.go) — `Head()` / `Page()`.
- [`auth/middleware.go`](../../auth/middleware.go) — `ProxydTransit`
  (service-bearer transit proof; gate the daemon-side dash check on it).
- [`chanreg/health.go`](../../chanreg/health.go) — adapter health.
- [`specs/5/17-openapi-mcp.md`](../5/17-openapi-mcp.md) — the
  HTML page is a third face on the same `/v1` handler set.
- [`specs/5/7-proxyd-standalone.md`](../5/7-proxyd-standalone.md) —
  route field semantics and `gated_by` evaluation. Registration is a
  `services/<daemon>.yaml` manifest or `coreProxydRoutes` (root
  `CLAUDE.md` "Adding a channel adapter").
