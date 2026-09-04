---
status: shipped
depends: [28-cockpit-index]
---

# dashd — the hub + retained cross-cutting pages

`dashd` stops being the monolith that renders every operator view. It
becomes the **cockpit hub** plus the home for **cross-cutting pages
that span daemons** — and nothing else. Per-daemon runtime views move
to the owning daemon (`6/30`, `6/32`). Architecture, read-path, auth, and
theme are defined in [`6/28`](28-cockpit-index.md); this spec lists only
what dashd keeps.

## The hub — `GET /dash/services/`

The AWS-Console "Services" home. A tile grid, one tile per daemon that
ships a `/dash/`. Each tile request-time probes `<daemon>:8080/health`
(500ms timeout, `6/28` "Hub probing"), shows `ok|warn|err` + version +
a one-line health summary, and links to `/dash/<daemon>/`. The daemon
list comes from the proxyd `/dash/<daemon>/` route entries — no
registry, no autodiscovery.

The hub renders **no** daemon-specific runtime view and holds **no**
duplicate control renderer. When a per-daemon dashboard subsumes an old
dashd page (e.g. proxyd's route editor, `6/30`), the migrating PR deletes
the dashd version and repoints the link — never two renderers
(CLAUDE.md "One renderer, many sinks").

## Build state (2026-08-30)

**The hub half is real.** `GET /dash/services/` ships, probes every daemon
concurrently at 500ms per tile, and — since the `6/30` migrations — renders **no
per-daemon runtime view at all**: dashd's routd, runed, authd and proxyd pages
are deleted, so the "never two renderers" rule now holds by absence rather than
by discipline.

**Two things are NOT done, and they are the ones this spec is about.**

1. ~~The tile list is still hand-kept.~~ **DONE 2026-08-30.** `shouldLink` reads
   the live route table, fetched once per render from proxyd's own `/v1` — the
   read path `6/28` requires of a cross-cutting page, and the only reader that
   knows both registration paths (core routes AND the per-package manifests
   every adapter ships). Any failure degrades to the core routes compiled in, so
   nothing links at a path that may not exist during the incident where proxyd
   is the broken thing. The `Built` bool is deleted.

1. **(historical)** This spec says "the daemon list comes
   from the proxyd `/dash/<daemon>/` route entries — no registry, no
   autodiscovery", and `dashd/services.go` still carries a literal list with a
   `Built` bool beside it. That bool has already drifted from the tree once, in
   five of eight rows and in BOTH directions, which is why
   `TestServicesBuiltFlagMatchesMountedRoutes` exists — it reads the union of
   the core routes and the package manifests and fails when the bool disagrees.
   The test makes the drift loud; it does not remove the second source.

   The fix is to derive the link from proxyd's `GET /v1/proxyd_routes`, which is
   the only reader that knows both registration paths and is also the read path
   `6/28` requires of a cross-cutting dashd page. Attempted 2026-08-30 and
   backed out: it is a bigger edit than it looks — the `Built` field feeds the
   link AND the "coming soon" label AND three tests — and it makes the hub's
   render depend on proxyd answering, which needs a deliberate fallback (core
   routes only, adapters read as "coming soon") rather than an accidental one.

1. ~~The retained pages still read the DB directly.~~ **READS DONE 2026-08-30 —
   all five.** `route_tokens`, `routes_admin`, `grants_admin`, `usage_page`,
   `groups_admin` and `tasks_admin` now read through their owner's `/v1`. No
   dashboard reads another daemon's SQLite file, which was `6/28`'s core rule
   and this spec's reason to exist.

   Two needed API work this phase had already specified, not decisions:
   `GET /v1/cost/summary` (the spend axis — `since` required, folder-narrowed,
   `SQLSubtree` not `LIKE`), and `open` + the two observe windows added to the
   group row as read-only projections of the `config` column, safe because
   `PutGroup` never writes that column and pinned by a round-trip test.

   Every migrated page renders an unreachable owner as an ERROR, never as empty
   data: "no routes", "no tokens", "no permissions", "costs nothing" and "no
   such task" are all false statements an operator acts on. `tasks_admin`
   distinguishes 404 from 503 for the same reason.

   Scaffolding: `dashd/stub_routd_test.go` answers routd's read endpoints from
   the `routd.db` a test seeds, and every dashd harness carries a `routdURL`.
   That fixture gap — not the authorize-before-read ordering this section once
   recorded — was what blocked `tasks_admin`; with a `routdURL` present its
   isolation 403 arrives exactly as in production.

1. **The WRITES stay on the DB, by rule.** This section previously called them
   blocked on an identity decision. They are not blocked; they are already
   correct. CLAUDE.md §"Split write-discipline" states that FS-mounted daemons
   — dashd, onbod, the CLI — write owned tables directly to the owner's DB,
   while non-mounted daemons go through the owner's HTTP API. dashd is
   FS-mounted on `routd.db`.

   The audit measurement is WHY that rule exists rather than an argument
   against it: `Store.AsUser` puts the OPERATOR on the `audit_log` row and a
   service bearer cannot, so routing an operator mutation through `/v1` would
   attribute it to `service:dashd`. `auth.ProxydTransit` accepts
   `sub.Sub == ProxydTransitSub` exactly, so admitting dashd would widen
   identity-stamping from one daemon to two — a cost with no benefit, since the
   direct write is sanctioned AND keeps the attribution.

   `6/28`'s read-path rule read "shows or mutates"; that clause contradicted
   CLAUDE.md and has been corrected there. **Nothing further is outstanding on
   this axis.**

## Retained cross-cutting pages

These span multiple daemons or are operator-global, so they stay in
dashd. **All of them read + write through the owning daemon's `/v1`
(service-token transit), never a DB directly** (`6/28` read-path).

| Page                                                   | Owning daemon `/v1`     | Migration note                        |
| ------------------------------------------------------ | ----------------------- | ------------------------------------- |
| services hub                                           | — (probes `/health`)    | new (`6/28`)                          |
| global status                                          | all (`/health` fan-out) | from `3/d` Status                     |
| activity                                               | routd `/v1`             | was direct `messages.db` read         |
| groups (+settings/model/skills/delete)                 | routd `/v1` groups      | was direct `routd.db` read            |
| grants / ACL (rows, edges, roles, principal-effective) | authd `/v1` acl         | absorbs `4/V`; was direct read        |
| routes / route-tokens                                  | routd `/v1` routes      | until/if moved to routd dash (`6/30`) |
| memory (view/edit MEMORY.md, CLAUDE.md, diary/facts)   | routd `/v1` group files | absorbs `4/Q`; was direct FS/DB       |
| profile / me-secrets / invites                         | authd + onbod `/v1`     | invites cross to onbod (`6/30`)       |

Migration rule (from `6/28`): a page primarily about ONE daemon's
runtime or owned resources moves out to that daemon; a page that spans
daemons or is operator-global stays here. `routes`/`route-tokens` are
the ambiguous pair — they edit `routd`-owned tables; keep them here
until routd's dashboard is built, then decide per "One renderer, many
sinks".

Two daemons already serve their own dashboard — onbod's admission queue
and timed's overview — so the hub has live tiles to link the day it
lands; their `/dash/` routes are already in `coreProxydRoutes`.

## Reconciliation

- `3/d-dashboards.md` (shipped) — its tile portal IS this hub; its
  Status/Activity/Groups/Memory pages are the retained set above.
- `4/Q-dash-memory.md` (shipped) — the memory browser, retained, now
  reading via routd `/v1` instead of direct FS/DB.
- `4/V-dashd-acl-ui.md` (shipped) — the ACL UI (rows/edges/roles/
  principal-effective), retained, now reading via authd `/v1`.

## Required `/v1` work

The migration's cost lands here: each retained page needs a `/v1` read
(and, for the editors, write) on the owning daemon to replace its
current direct DB/FS access. Enumerate against the page table above
when building; most map to existing `specs/5/17` resources (groups, acl,
routes, group-files). Add a minimal endpoint only where none exists.

## Auth

Per `6/28`: proxyd `auth: "user"` transit + `auth/dashauth.go`
operator gate. All hub + cross-cutting pages are operator-only.

## Non-goals

Per `6/28`. Specifically: the hub is navigation, not a global status
screen that inlines every daemon's detail; it links out, it does not
aggregate runtime state.

## Acceptance

- `/dash/services/` lists every daemon with a `/dash/` route, each tile
  reflecting a live `/health` probe; a down daemon shows `err`, a
  scope-denied daemon shows `warn`.
- No dashd handler opens a SQLite DB or reads a group's FS directly;
  every datum comes from an owning daemon's `/v1`.
- Each retained page renders and (where it had writes) persists via
  `/v1`; `4/Q` memory edits and `4/V` ACL writes still work.
