---
status: superseded
superseded_by: 6/1-cockpit-index
depends: [5/5-uniform-mcp-rest, 5/35-proxyd-standalone]
---

> **Superseded by [`specs/6/1-cockpit-index.md`](../6/1-cockpit-index.md)**
> and the phase-6 cockpit spec set. The routing/auth/theme/hub model
> below carries over almost verbatim; phase 6 adds the `/v1`-only
> read-path (this draft left dashd's direct-DB pages in place) and the
> per-daemon spec breakdown. Kept for history.

# Per-daemon dashboards + central index

**Each daemon owns its own `/dash/` subspace. Central `dashd` becomes
the index — an AWS-Console "Services" home page that probes and links
into every per-daemon dashboard.** One renderer per daemon, one hub.

## Problem

`dashd` today owns every operator view (`dashd/main.go:124-164`): tasks,
activity, groups, routes, memory, profile, channels, secrets — all
served from one binary reading the shared `messages.db`. Three pains:

- **It doesn't scale.** Adding `timed`'s scheduled-task detail, `onbod`'s
  admission queue, `whapd`'s pairing status, `mastd`'s stream health
  all means a new handler in `dashd/main.go`. The file is ~850 LOC and
  growing per release.
- **It can't reach per-daemon state cleanly.** Live in-memory state
  (whapd's QR session, mastd's stream connection, timed's next-tick
  estimate) lives in the daemon process. `dashd` can only see what
  hits the DB. Probing over HTTP from `dashd` recreates each daemon's
  surface; the daemon already has it.
- **It violates "the daemon that owns the data owns the view".**
  `routes_admin.go` writes to `routes` table that `proxyd` reads; the
  routing UI sits in dashd instead of next to the daemon that uses it.
  Two paths to one consumer (operator + daemon) drift silently — see
  CLAUDE.md "One renderer, many sinks".

## What ships

1. **Per-daemon `/dash/`** — each daemon serves its own dashboard at
   `http://<daemon>:8080/dash/` (same port as everything else, per
   CLAUDE.md "Daemon HTTP port: `:8080`").
2. **proxyd route per daemon** — `/dash/<daemon>/` → `http://<daemon>:8080/dash/`
   added to `compose.go` defaults (`compose/compose.go:167-176`), all
   `auth: "user"` (proxyd OAuth gate identical to today's `/dash/`).
3. **Central `dashd` index** — new `GET /dash/services/` tiles every
   registered daemon, probes `<daemon>:8080/health`, links into each
   per-daemon dashboard.
4. **Theme inheritance** — each daemon imports
   `github.com/kronael/arizuko/theme` and uses `theme.Page()` /
   `theme.Head()` (`theme/theme.go:320-329`). Sticky topnav identifies
   which daemon you're inside; breadcrumb shows `hub › <daemon> › <page>`.

Existing `dashd` pages (groups, routes, activity, memory, profile)
stay at `/dash/*`. They're cross-cutting operator views, not per-daemon
state.

## Component breakdown

| Daemon   | `/dash/` shows                                                     | New LOC |
| -------- | ------------------------------------------------------------------ | ------- |
| `timed`  | scheduled_tasks (own + status), next ticks, last run log per task  | ~80     |
| `onbod`  | admission queue, recent invites, rate-limit counters               | ~80     |
| `whapd`  | session status, QR / pair code, last reconnect, message throughput | ~100    |
| `mastd`  | stream connection state, follow list, recent events                | ~60     |
| `teled`  | bot identity, webhook status, last update                          | ~50     |
| `discd`  | gateway connection, guild/channel list, latency                    | ~60     |
| `proxyd` | live route table, OAuth provider config, recent denials            | ~80     |
| `davd`   | active sessions, recent writes                                     | ~50     |
| `gated`  | message-loop stats, container spawn history, circuit-breaker state | ~100    |

Each daemon defines its own page set. No central registry of what each
shows — the daemon owns its surface.

## Auth model

Two gates in series:

1. **proxyd OAuth gate** (`proxyd/main.go:554-561`, `auth: "user"`).
   Unauthenticated traffic redirects to login. After login, proxyd
   stamps `X-User-Sub`, `X-User-Groups`, `X-User-Sig` per
   `proxyd/main.go:97`.
2. **Daemon-side scope check.** Each daemon's `/dash/` handler verifies
   signed headers (`auth/middleware.go:9-35`, `RequireSigned`), then
   applies per-page authorization. The canonical helper is
   `dashd/authz.go:18`'s `requireAdmin(w, r, scope)` — pattern lifts
   verbatim into each daemon (it's ~40 LOC; copy is fine until a third
   site appears, then promote to `auth/dashauth.go`).

A 403 from a deep page renders the standard theme error banner, NOT a
plain text response. The hub tile shows a yellow `warn` dot when probing
finds `/dash/` returns 403 for the current operator (means the daemon
is up but they lack scope) vs red `err` for connection failure.

## Theme + visual consistency

Every daemon imports `theme` (`theme/theme.go`). The hub looks like
AWS Console's "Services" page: tiles in a grid, each tile shows
daemon name, status dot (`ok|warn|err`), version, uptime. Click →
land on that daemon's `/dash/`.

Sticky topnav (added to `theme.Page()`):

```
[ arizuko hub ]  ⌄ services  |  timed › tasks
```

Breadcrumb is left-aligned, identifies current daemon. Clicking
"arizuko hub" returns to `/dash/services/`. The dropdown lists every
known daemon for fast pivot — same model as AWS Console's services
menu. Each daemon's `/dash/` handler passes its own name as
breadcrumb root.

Visual rule: the central hub uses tile grids; per-daemon dashboards
use tables + detail panes. Hub is overview, daemon is depth — same
distinction the CSS already encodes (`tiles` vs `table`).

## What this is NOT

- **Not a migration of dashd functionality.** Groups, routes, memory,
  activity, profile pages stay where they are. They're cross-cutting,
  not per-daemon.
- **Not a config DSL.** The daemon list is computed from
  `compose.defaultRoutes` (`compose/compose.go:167`) plus any
  `[[proxyd_route]]` entries with `path = "/dash/<name>/"`. No new
  config file.
- **Not metrics.** Tiles show binary health (up / warn / down) +
  uptime + version. Real metrics go to Prometheus or equivalent
  separately. The hub is navigation, not observability.
- **Not auto-discovery of dashboards.** If a daemon doesn't ship
  `/dash/`, the hub doesn't show a tile for it — the proxyd route
  entry is the registration.

## Code surface

| Site                                          | Change                                                      | LOC    |
| --------------------------------------------- | ----------------------------------------------------------- | ------ |
| `compose/compose.go:167` defaults             | Add `/dash/<daemon>/` route per daemon shipping a dashboard | ~20    |
| `dashd/main.go:124` registerRoutes            | Add `GET /dash/services/` handler                           | ~80    |
| `dashd/services.go` (new)                     | Hub handler: probe every `/dash/<daemon>/` route, render    | ~80    |
| `<daemon>/dash.go` (new, per daemon)          | `/dash/*` handler set using `theme.Page()`                  | 50-100 |
| `auth/dashauth.go` (when 3rd daemon needs it) | Promote `requireAdmin` from `dashd/authz.go:18`             | ~40    |
| `theme/theme.go`                              | Add breadcrumb topnav helper                                | ~30    |

Single ship-blocking constraint: probing strategy (see Open).

## Migration

Opt-in per daemon. Existing `dashd` keeps every page it has today.
Order of adoption:

1. `dashd/services.go` lands with empty tile list (no behavior change).
2. `timed/dash.go` lands; compose adds `/dash/timed/`; tile appears
   on hub.
3. Iterate per daemon. Each PR adds one tile.

Rolling out doesn't break old surfaces. When a daemon's page covers
what a `dashd` page does (e.g. `proxyd/dash.go` routes editor
duplicates `dashd/routes_admin.go`), the second PR deletes the dashd
version and updates the hub link — never silently leave two renderers
([CLAUDE.md "One renderer, many sinks"](../../CLAUDE.md)).

## Open questions

- **Probe at request time vs cached state?** Probing every daemon's
  `/health` on each hub render costs ~10 RTTs over the docker network
  (~5ms each, sub-100ms total — acceptable). Caching needs a refresher
  goroutine + staleness display. Lean: probe at request time, add cache
  only if it becomes slow. Failure mode: one slow daemon stalls the
  hub render → use `context.WithTimeout(500ms)` per probe.
- **Self-registration vs static config?** Static (compose route entries)
  matches today's pattern, no new mechanism. Self-registration (daemon
  POSTs to `dashd` on start) adds a runtime dependency dashd doesn't
  have today. Lean: static. Revisit only if dynamic daemons appear.
- **Operator-only daemons vs user-visible?** `gated`'s spawn history
  is operator-only; `timed`'s own-task list is user-visible. Per-daemon
  scope check handles this — but the hub tile visibility needs the
  same check. Cheapest path: hub renders all tiles, daemon's `/dash/`
  returns 403 to non-scoped callers, tile shows yellow dot. Alternative:
  hub asks each daemon "is current caller allowed?" via a `HEAD` to
  `/dash/`. Lean: render all, let the deep page 403.
- **Webd's `/chat/`, `/api/`, `/x/`, `/mcp` surfaces.** Webd has many
  non-dashboard routes (`compose/compose.go:168-172`). Does webd need
  a `/dash/` for its own slink/widget state? Probably yes — separate
  it from chat traffic. Out of scope here.

## Code pointers

- [`dashd/main.go:124-164`](../../dashd/main.go) — current
  monolithic route table; the source of the scaling pain.
- [`dashd/authz.go:18`](../../dashd/authz.go) — canonical operator
  gate, lifts into per-daemon dashboards.
- [`compose/compose.go:167-176`](../../compose/compose.go) —
  defaultRoutes; where per-daemon `/dash/<name>/` entries land.
- [`proxyd/routes.go:18-21`](../../proxyd/routes.go) — `Route{Path,
Backend, Auth}` shape; no schema change needed.
- [`theme/theme.go:320-329`](../../theme/theme.go) — `Head()` and
  `Page()` helpers every daemon imports.
- [`auth/middleware.go:9-35`](../../auth/middleware.go) —
  `RequireSigned` / `StripUnsigned` verify identity headers.
- [`specs/5/5-uniform-mcp-rest.md`](../5/5-uniform-mcp-rest.md) —
  resource handlers expose REST + MCP; this spec adds an HTML face
  ON THE SAME DAEMON, owned by the same handler set. The hub is a
  third face (navigation), not a fourth surface.
