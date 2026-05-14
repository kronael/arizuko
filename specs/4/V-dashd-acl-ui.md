---
status: spec
depends: [6/9-acl-unified.md, 4/Q-dash-memory.md]
---

# dashd ACL UI — operator surface for the unified authorization model

## Essence

The dashd ACL UI lets an operator inspect, query, and write `acl` and
`acl_membership` rows directly, plus manage `role:*` principals as
first-class objects. Every authorization question in arizuko goes
through one `Authorize` call against these two tables
(`specs/6/9-acl-unified.md:9-18`); this UI is the operator-facing
side of that same primitive. Four pages — rows, edges, roles,
principal-effective — over the existing dashd HTMX + partial pattern
(`dashd/main.go:309-320`). Read-only views first, then writes,
gated by `Authorize(caller, "admin", "**", ...)`.

## Routes

| Method | Path                                            | Purpose                                    | Returns                               |
| ------ | ----------------------------------------------- | ------------------------------------------ | ------------------------------------- |
| GET    | `/dash/acl`                                     | List + filter `acl` rows                   | Full page + `<tbody>` partial on HTMX |
| POST   | `/dash/acl`                                     | Insert one row                             | `<tr>` partial appended to list       |
| DELETE | `/dash/acl/{id}`                                | Delete row by composite key (form-encoded) | 204 + HTMX `hx-swap=delete`           |
| GET    | `/dash/acl/x/rows`                              | Filtered rows partial (HTMX target)        | `<tr>...` fragment                    |
| GET    | `/dash/membership`                              | List edges + transitive expander           | Full page                             |
| POST   | `/dash/membership`                              | Add `(child, parent)` edge                 | `<tr>` partial                        |
| DELETE | `/dash/membership/{id}`                         | Remove edge (composite key form)           | 204                                   |
| GET    | `/dash/membership/x/expand?p=<principal>`       | Recursive parent walk                      | `<ul>` partial                        |
| GET    | `/dash/roles`                                   | List `role:*` principals                   | Full page                             |
| GET    | `/dash/roles/{name}`                            | Role detail (members + permissions)        | Full page, two `<table>` partials     |
| GET    | `/dash/principals/{id}`                         | Effective ACL view for a principal         | Full page                             |
| GET    | `/dash/principals/x/effective?p=<id>&scope=<s>` | RenderACL output                           | `<table>` partial                     |

The composite-key DELETE form posts every PK column
(`principal, action, scope, params, predicate, effect` for `acl`;
`child, parent` for `acl_membership`) — schema lacks a surrogate
id (`specs/6/9-acl-unified.md:58, 70`).

## Pages

### `/dash/acl` — row table

Filter form (GET querystring): `principal`, `action`, `scope`,
`effect`, `granted_by`. Each is a glob applied client-side via the
filter form; backend uses `LIKE` with `%` substitution for `*` and
exact `IN` for explicit lists. Columns shown: principal, action,
scope, effect (badge), params, predicate, granted_by, granted_at,
delete-button. Sort default `granted_at DESC`.

Insert form (collapsible `<details>` above the table) with seven
fields plus a "validate" preview that calls
`/dash/acl/x/preview` (read-only `Authorize` dry-run vs the current
row set) before submit. Predicate and params fields are free text;
basic syntactic check (`key=value`, single conjunction per row,
matching `specs/6/9-acl-unified.md:284-286` lean) rejects obviously
malformed input — full grammar validation is a backend job in
`grants/`.

Effect column renders `allow` green, `deny` red. A small badge
next to each row indicates whether it overrides a tier default
(query `grants.DeriveRules` for the same principal/scope and flag
rows that shadow a derived default).

### `/dash/membership` — edge table + transitive expander

Two-column layout: edge table on the left, transitive expander on
the right. Edge table columns: child, parent, added_by, added_at,
delete-button. Add-edge form (top) takes `child` + `parent`, posts
to `POST /dash/membership`; server runs the cycle check
(`specs/6/9-acl-unified.md:157-159`) before insert.

Transitive expander: text input for a principal; on change it issues
`GET /dash/membership/x/expand?p=<id>` which returns a nested
`<ul>` rendering the closure of `acl_membership` reachable from that
principal. Same evaluation as step 1 of `Authorize`
(`specs/6/9-acl-unified.md:176-177`). Re-uses the lookup; does not
reimplement.

### `/dash/roles` — role index + detail

`/dash/roles` lists every distinct principal matching `role:%` from
both `acl.principal` and `acl_membership.parent`. Columns: name,
member count, permission count, click-through link.

`/dash/roles/{name}` shows two panels:

1. **Members** — rows from `acl_membership WHERE parent=?`. Add /
   remove via the membership endpoints (forms target
   `/dash/membership`); page is a view, not a separate write path.
2. **Permissions** — rows from `acl WHERE principal=?`. Add / remove
   via `/dash/acl` endpoints with the principal field pinned.

This is the IAM-shaped surface from
`template/web/pub/concepts/grants.html:77-86`.

### `/dash/principals/{id}` — effective grants

The "what can this principal actually do" page. Three sections:

1. **Direct grants** — `acl WHERE principal=?` (no transitive
   expansion).
2. **Role-derived grants** — for each role reached transitively via
   `acl_membership`, the rows on that role. Each row tagged with the
   role it came from.
3. **Tier-default fallback** — for `folder:` principals, output of
   `grants.DeriveRules(folder)` filtered to `mcp:*` actions
   (`specs/6/9-acl-unified.md:184-187`); shown only when no
   explicit row covers that tool.

Optional `?scope=<s>` parameter narrows everything to rows whose
scope glob-matches `s`, producing the exact effective rule list
`RenderACL` returns (`specs/6/9-acl-unified.md:234-239`). When
absent, show all rows regardless of scope.

## Existing `/dash/groups` refresh

The current view (`dashd/main.go:427-480`) shows folders with their
routes via `writeGroupRoutes` (`dashd/main.go:482-519`). Update each
`<details>` block to show three sections instead of one:

1. **Routes** — existing route table (kept).
2. **ACL rows scoped to this folder** — `SELECT * FROM acl WHERE scope = ? OR ? GLOB scope` against the folder. Columns: principal, action, effect, params, predicate.
3. **Principals with effective access** — distinct principals from
   the ACL row query (expanded via `acl_membership` to surface
   role members). Each principal links to
   `/dash/principals/{id}?scope=<folder>`.

No new top-level page — the augmentation is in-place. The existing
`writeGroupRoutes` becomes one of three helpers called inside the
`<details>` body.

## Auth shape

All routes require an authenticated operator. Reuse the
`requireFolder` middleware pattern from `webd/server.go:128-139`
(per-request user check), but with a scope of `**`:

- **Reads** (`GET *`) gated by `Authorize(caller, "interact", "**", ...)`.
- **Writes** (`POST`, `DELETE`) gated by `Authorize(caller, "admin", "**", ...)`.

The `caller` principal is the canonical OAuth sub from the
proxyd-signed `X-User-Sub` header (verified by
`auth/middleware.go`). Failures return 403 with a small HTMX-aware
fragment so inline writes show an error banner rather than a hard
page swap. No bypass for the bootstrap operator at the UI layer —
they pass the check via the `role:operator` row seeded at
`arizuko create` (`specs/6/9-acl-unified.md:266-280`).

## HTMX patterns

Reuse the partial-rendering convention already established
(`dashd/main.go:144-145`: `/dash/tasks/x/list`,
`/dash/activity/x/recent`). Each list page has an `x/` sibling
endpoint returning the inner fragment only.

Partials:

- `acl/x/rows`: `<tr>...</tr>` repeated. Used for filter changes
  (form `hx-get="/dash/acl/x/rows" hx-trigger="change from:form"`)
  and post-insert append (`hx-target="tbody" hx-swap="beforeend"`).
- `membership/x/expand`: nested `<ul>` for the transitive closure.
- `principals/x/effective`: `<table>` for the effective grant list,
  re-rendered when the optional `scope` field changes.
- `roles/x/members` and `roles/x/permissions`: the two panels on the
  role detail page, each refreshable independently.

No SPA. No JSON endpoints from dashd. Same `theme.CSS` +
`theme.ThemeScript` + htmx CDN pattern as
`dashd/main.go:162-167`.

## Open questions

1. **Conditional grant rendering.** A row with non-empty `params`
   or `predicate` is conditional — rendered legibly how? Lean:
   two extra columns shown verbatim, with a tooltip on hover
   expanding them ("predicate `discord:guild=X` means: only when the
   user's JWT carries `discord:guild=X`"). Avoid pretty-printing
   in v1; the operator who wrote the row can read it.
2. **Principal pattern validation.** Should the insert form reject
   principals that don't match any known namespace prefix
   (`google:`, `folder:`, `telegram:`, `discord:`, `role:`, `**`,
   `*`)? Lean: warn but allow — new platform adapters will introduce
   new prefixes; reject only structural bugs (whitespace, empty).
3. **Bulk import.** A CSV upload for migrating from
   `user_groups`-style rows? Lean: defer — the migration path lives
   in the v0.40 release script, not the UI. UI does single-row CRUD
   only.
4. **`acl_use_log` audit view.** Deferred per docs commitment
   (`template/web/pub/concepts/grants.html:115` — "agents discover
   authorization at the failure site"). Add when the table fills
   with real data.
5. **Wildcard delete safety.** Deleting a row with `principal='**'`
   or `scope='**'` could lock everyone out. Add a confirm-input
   gate ("type the principal exactly"); deletion of the bootstrap
   `role:operator, *, **` row warns "this is the bootstrap row;
   continuing requires DB shell access". Lean: enforce the gate.

## Phases

- **M0 — read-only views.** `/dash/acl`, `/dash/membership`,
  `/dash/roles`, `/dash/principals/{id}` all GET only. No filter,
  no expander; plain tables. Refresh of `/dash/groups` to include
  ACL rows + principals (read-only).
- **M1 — row CRUD.** POST + DELETE on `/dash/acl`. Insert form,
  delete buttons, validation pass on predicate / params syntax.
  Filter form on `/dash/acl`.
- **M2 — membership UI.** POST + DELETE on `/dash/membership`;
  cycle check enforced. Transitive expander partial.
- **M3 — principal-effective + role detail.** Full
  `/dash/principals/{id}` with tier-default fallback; role detail
  with members + permissions panels. Wildcard-delete gate.

Each phase ships with `dashd/integration_test.go` coverage
(`dashd/integration_test.go:1-163` pattern) plus
`dashd/coverage_test.go` exercise of the new routes.
