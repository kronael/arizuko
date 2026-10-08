---
status: shipped
depends: [28-cockpit-index]
---

# dashd — the hub + retained cross-cutting pages

`dashd` is the **cockpit hub** plus the home for **cross-cutting pages
that span daemons** — and nothing else. Per-daemon runtime views live in
the owning daemon (`6/30`, `6/32`). Architecture, read-path, auth, and
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
duplicate control renderer (CLAUDE.md "One renderer, many sinks"). It is
navigation: it links out, it does not aggregate runtime state.

## What ships (2026-08-30)

**The hub.** `GET /dash/services/` probes every daemon concurrently at 500ms
per tile and renders no per-daemon runtime view at all, so the "never two
renderers" rule holds by absence rather than by discipline.

**The tile list is the live route table.** `shouldLink` reads it, fetched once
per render from proxyd's own `/v1` — the read path `6/28` requires of a
cross-cutting page, and the only reader that knows both registration paths
(core routes AND the per-package manifests every adapter ships). Any failure
degrades to the core routes compiled in, so nothing links at a path that may
not exist during the incident where proxyd is the broken thing.

**The retained pages read through their owner's `/v1`** — all of them:
`route_tokens`, `routes_admin`, `grants_admin`, `usage_page`, `groups_admin`
and `tasks_admin`. No dashboard reads another daemon's SQLite file, which is
`6/28`'s core rule and this spec's reason to exist.

Two `/v1` additions carry it: `GET /v1/cost/summary` (the spend axis — `since`
required, folder-narrowed, `SQLSubtree` not `LIKE`), and `open` + the two
observe windows on the group row as read-only projections of the `config`
column, safe because `PutGroup` never writes that column and pinned by a
round-trip test.

Every page renders an unreachable owner as an ERROR, never as empty data: "no
routes", "no tokens", "no permissions", "costs nothing" and "no such task" are
all false statements an operator acts on. `tasks_admin` distinguishes 404 from
503 for the same reason.

Scaffolding: `dashd/stub_routd_test.go` answers routd's read endpoints from
the `routd.db` a test seeds, and every dashd harness carries a `routdURL`, so
`tasks_admin`'s isolation 403 arrives exactly as in production.

**The WRITES stay on the DB, by rule.** CLAUDE.md §"Split write-discipline"
states that FS-mounted daemons — dashd, onbod, the CLI — write owned tables
directly to the owner's DB, while non-mounted daemons go through the owner's
HTTP API. dashd is FS-mounted on `routd.db`.

The audit measurement is WHY that rule exists rather than an argument against
it: `Store.AsUser` puts the OPERATOR on the `audit_log` row and a service
bearer cannot, so routing an operator mutation through `/v1` would attribute
it to `service:dashd`. `auth.ProxydTransit` accepts
`sub.Sub == ProxydTransitSub` exactly, so admitting dashd would widen
identity-stamping from one daemon to two — a cost with no benefit, since the
direct write is sanctioned AND keeps the attribution.

## Retained cross-cutting pages

These span multiple daemons or are operator-global, so they stay in
dashd. **All of them read through the owning daemon's `/v1`
(service-token transit), never a DB directly** (`6/28` read-path); writes
go direct by the rule above.

| Page                                                   | Owning daemon `/v1`     |
| ------------------------------------------------------ | ----------------------- |
| services hub                                           | — (probes `/health`)    |
| global status                                          | all (`/health` fan-out) |
| activity                                               | routd `/v1`             |
| groups (+settings/model/skills/delete)                 | routd `/v1` groups      |
| grants / ACL (rows, edges, roles, principal-effective) | authd `/v1` acl         |
| routes / route-tokens                                  | routd `/v1` routes      |
| memory (view/edit MEMORY.md, CLAUDE.md, diary/facts)   | routd `/v1` group files |
| profile / me-secrets / invites                         | authd + onbod `/v1`     |

The rule (`6/28`): a page primarily about ONE daemon's runtime or owned
resources lives in that daemon; a page that spans daemons or is
operator-global lives here. `routes`/`route-tokens` edit `routd`-owned tables
and stay here.

## Auth

Per `6/28`: proxyd `auth: "user"` transit + `auth/dashauth.go`
operator gate. All hub + cross-cutting pages are operator-only.

## Acceptance — and the two criteria that do NOT hold

Checked against the code 2026-10-08. A criteria list that quietly loses its open items
is a list that reports itself met.

- ⚠ `/dash/services/` lists every daemon with a `/dash/` route, each tile reflecting a
  live `/health` probe; a down daemon shows `err`, **a scope-denied daemon shows
  `warn`**. The listing and the probe hold — 18 tiles, and `gitd` is correctly absent
  because it mounts no `/dash/` route. The `warn` state does **not exist**:
  `dashd/services.go:78-80` declares three, `ok`, `err` and `unknown`, so a
  scope-denied daemon is indistinguishable from one that did not answer.
- ❌ **No dashd handler opens another daemon's store** or reads a group's FS directly;
  every datum comes from an owning daemon's `/v1`. `dashd/main.go:189` calls
  `store.OpenOwner(store.OwnerRoutd)` and `authz.go`, `usage_page.go`,
  `proactive_page.go`, `profile.go`, `tools_admin.go` and `dashstore/pages.go` read
  through it; the memory edits write the filesystem. This is the cut `6/36` S1 names
  and nobody has taken.
- ✅ Each retained page renders via `/v1`; `4/Q` memory edits and `4/V` ACL writes still
  work.
