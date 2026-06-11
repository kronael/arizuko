---
status: draft
depends: [1-cockpit-index]
---

# dashd — the hub + retained cross-cutting pages

`dashd` stops being the monolith that renders every operator view. It
becomes the **cockpit hub** plus the home for **cross-cutting pages
that span daemons** — and nothing else. Per-daemon runtime views move
to the owning daemon (`6/3`–`6/14`). Architecture, read-path, auth, and
theme are defined in [`6/1`](1-cockpit-index.md); this spec lists only
what dashd keeps.

## The hub — `GET /dash/services/`

The AWS-Console "Services" home. A tile grid, one tile per daemon that
ships a `/dash/`. Each tile request-time probes `<daemon>:8080/health`
(500ms timeout, `6/1` "Hub probing"), shows `ok|warn|err` + version +
a one-line health summary, and links to `/dash/<daemon>/`. The daemon
list comes from the proxyd `/dash/<daemon>/` route entries — no
registry, no autodiscovery.

The hub renders **no** daemon-specific runtime view and holds **no**
duplicate control renderer. When a per-daemon dashboard subsumes an old
dashd page (e.g. proxyd's route editor `6/6`), the migrating PR deletes
the dashd version and repoints the link — never two renderers
(CLAUDE.md "One renderer, many sinks").

## Retained cross-cutting pages

These span multiple daemons or are operator-global, so they stay in
dashd. **All of them read + write through the owning daemon's `/v1`
(service-token transit), never a DB directly** (`6/1` read-path).

| Page                                                   | Owning daemon `/v1`     | Migration note                       |
| ------------------------------------------------------ | ----------------------- | ------------------------------------ |
| services hub                                           | — (probes `/health`)    | new (`6/1`)                          |
| global status                                          | all (`/health` fan-out) | from `3/d` Status                    |
| activity                                               | routd `/v1`             | was direct `messages.db` read        |
| groups (+settings/model/skills/delete)                 | routd `/v1` groups      | was direct `routd.db` read           |
| grants / ACL (rows, edges, roles, principal-effective) | authd `/v1` acl         | absorbs `4/V`; was direct read       |
| routes / route-tokens                                  | routd `/v1` routes      | until/if moved to routd dash (`6/3`) |
| memory (view/edit MEMORY.md, CLAUDE.md, diary/facts)   | routd `/v1` group files | absorbs `4/Q`; was direct FS/DB      |
| profile / me-secrets / invites                         | authd + onbod `/v1`     | invites cross to onbod (`6/7`)       |

Migration rule (from `6/1`): a page primarily about ONE daemon's
runtime or owned resources moves out to that daemon; a page that spans
daemons or is operator-global stays here. `routes`/`route-tokens` are
the ambiguous pair — they edit `routd`-owned tables; keep them here
until `6/3` is built, then decide per "One renderer, many sinks".

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
when building; most map to existing `specs/5/5` resources (groups, acl,
routes, group-files). Add a minimal endpoint only where none exists.

## Auth

Per `6/1`: proxyd `auth: "user"` transit + `auth/dashauth.go`
operator gate. All hub + cross-cutting pages are operator-only.

## Non-goals

Per `6/1`. Specifically: the hub is navigation, not a global status
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
