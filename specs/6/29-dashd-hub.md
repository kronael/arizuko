---
status: partial
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

1. **The retained pages still read the DB directly — and there is a BLOCKER
   nobody had written down.**

   The obvious migration is to swap `store.New(d.adminDB())` for
   `d.bearerCall(routdURL, …)` per page. For the READS that is clean. For the
   WRITES it is an audit regression, measured 2026-08-30:
   - `dashd/route_tokens.go:79` revokes via
     `store.New(d.adminDB()).AsUser(sub).RevokeRouteToken(...)`, and
     `Store.AsUser` (`store/store.go:44`) puts the OPERATOR's sub on the
     `audit_log` row.
   - `bearerCall` (`dashd/engagement_page.go:186`) sets exactly one auth header:
     `Authorization: Bearer <dashd's service token>`. It forwards no `X-User-*`.

   So moving a control onto the owning daemon's `/v1` today attributes every
   operator mutation to `service:dashd`. The audit trail would stop naming who
   did it, and nothing would fail — the write succeeds, the row is just wrong.
   That affects every retained page that has a control: tasks, routes, groups,
   grants and route tokens.

   **This is decided before any page moves**, and there are two shapes:
   - **Forward the operator identity.** dashd re-stamps `X-User-Sub`/`-Groups`
     on the call and the owning daemon trusts them on the strength of dashd's
     service bearer — the same trust-stamp pattern `auth.ProxydTransit` already
     implements for proxyd. This is the smaller change and it keeps one audit
     shape, but it widens who may stamp identity headers from one daemon to two.
   - **Give the owning daemon an operator-authenticated face.** dashd forwards
     the operator's own credential rather than acting for them. Cleaner
     attribution, larger change, and it needs a token dashd does not hold.

   **Correction 2026-08-30 — the READS are not blocked, and saying they were
   was wrong.** A SELECT writes no audit row, so attribution cannot be lost by
   moving one. The earlier claim that a page's reads may not move without its
   writes ("two paths into one table") mixed up two different rules: "one
   renderer, many sinks" forbids two PRODUCERS of one output, and one reader
   plus one writer is not that. The read half is exactly what `6/28`'s
   "Read-path: /v1 only, no direct DB" asks for, and it can land alone.

   So reads migrate now, per page; writes wait for the decision above.
   `route_tokens` is done (2026-08-30): the list comes from routd's
   `GET /v1/route_tokens` typed as `resources.RouteTokensRow` — the same struct
   routd's handler serves and `/openapi.json` emits a schema from, so the page
   cannot render a field the API lacks. Its revoke still writes through
   `Store.AsUser` and says so in a comment naming this decision. An unreachable
   routd renders an error, because a blank token list reads as "this folder has
   no tokens", which is the opposite of the truth.

   **The remaining five are NOT one blocked group** — an earlier note here said
   they were, and that was too broad. Measured 2026-08-30 against their routes
   and their owners' `/v1`:

   | page           | reads                  | blocked by                                                                                                                                                  |
   | -------------- | ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
   | `groups_admin` | routd `/v1/groups`     | nothing — `/dash/groups/{folder...}` puts the folder in the ROUTE, so it authorizes before it reads, exactly like `route_tokens`                            |
   | `grants_admin` | authd `/v1`            | nothing — same folder-keyed route                                                                                                                           |
   | `routes_admin` | routd `/v1/routes`     | nothing — instance-wide and operator-gated first, so no per-folder ordering exists                                                                          |
   | `usage_page`   | —                      | a MISSING endpoint: `GET /v1/cost` is per-TURN (`turn_id` required); the page needs the per-folder aggregate `6/30` already lists as `GET /v1/cost/summary` |
   | `tasks_admin`  | routd `/v1/tasks/{id}` | the authorize-before-read ordering above — it is keyed by task id, so the owner is only known after the read                                                |

   So three are mechanical, one needs a small new routd endpoint that this
   phase already specified, and exactly ONE needs the ordering decision.

   **The test scaffolding they all needed now exists.** `dashd/stub_routd_test.go`
   answers routd's read endpoints from the same `routd.db` a test seeds, and all
   three dashd harnesses (`isoEnv`, `newRWDashServer`, `splitAdminDash`) carry a
   `routdURL` — production always has one, so a dash without it was never a real
   state. The stub is deliberately a thin projection running the owning daemon's
   query and marshalling its struct: one that invented its own shape would let
   these tests pass against a page that cannot read the real thing.

   Two pages are migrated (`route_tokens`, `routes_admin`), and each renders an
   unreachable routd as an ERROR rather than an empty list — "this instance
   routes nothing" and "this folder has no tokens" are both false, and both are
   the kind of thing an operator acts on during an incident.

   **`tasks_admin` was attempted and reverted 2026-08-30, and the reason is a
   finding, not a stumble.** The code change is straightforward — routd serves
   `GET /v1/tasks/{id}` and `/v1/tasks/{id}/runs`, and the page's two reads swap
   cleanly. What broke was `TestIso_PerFolderGET_AliceForbiddenOnSales`: the
   task-detail case went from **403 to 500**.

   The cause is an ordering this page has always had and the DB read hid.
   Authorization needs the task's OWNER, and the owner is only known after the
   row is read — so the page reads, THEN authorizes. Against a local DB the read
   cannot fail, so the 403 always arrived. Against `/v1` an unreachable routd
   fails first, and a cross-tenant request that must be REFUSED becomes an
   error instead. 500 and 403 are not interchangeable on an isolation path.

   So this page needs one of:
   - **Authorize before reading.** Requires the task→folder mapping to be
     available without fetching the task — a `GET /v1/tasks/{id}/owner`, or
     carrying the folder in the URL as the group pages already do
     (`/dash/groups/{folder}/...`). This is the better shape and it is a route
     change.
   - **Make the failure explicit.** Keep the order, and render an unreachable
     routd distinctly from a refusal, so the isolation test can assert 403 for
     the refusal and something else for the outage.

   Either way the isolation tests need a stub routd, since `isoEnv` builds a
   dash with no `routdURL`. `route_tokens` did not hit this: its folder is in
   the URL, so it authorizes before it reads.

   The debt itself: the direct-DB reads `6/28` set out to retire are NOT on the
   per-daemon pages — those are gone — they are concentrated in exactly the
   pages this spec KEEPS: `tasks_admin`, `routes_admin`, `groups_admin`,
   `grants_admin`, `usage_page` and `route_tokens`, plus `authz.go`. dashd
   already holds the client and a URL per owner (`bearerCall`, `routdURL`,
   `onbodURL`, `authdURL`), so once the attribution question above is answered
   this is per-page work, not a new mechanism.

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
