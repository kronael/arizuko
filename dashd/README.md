# dashd

Operator dashboard daemon: HTMX views over `messages.db`, per-user secret
CRUD, and admin-gated CRUD over routes, groups, invites, and grants.

## Purpose

Standalone HTMX portal for operators and signed-in users. Reads most of
its data directly from the shared SQLite (`messages.db`) and the group
filesystem; TIER 1 write paths cover routes, groups, per-user secrets,
invites, grants, model selector, and skill toggles — all gated by
`auth.Authorize`.

## Tables owned

None. Schema and migrations live in gated. dashd holds read+write
connections to the shared DB and writes to `routes`, `groups`, `invites`,
`acl`, and `secrets` via the `store` package; it never migrates.

## Surface

Handlers are registered in `dash.registerRoutes` (`main.go`). Counts
below match the actual `mux.HandleFunc` calls.

- **Health** (1): `GET /health` — JSON `{ok:true}`.
- **Portal** (1): `GET /dash/` — tile grid with status/tasks dots.
- **Read pages** (6): `GET /dash/status/`, `/dash/tasks/`,
  `/dash/activity/`, `/dash/groups/`, `/dash/memory/`, `/dash/profile/`.
  Render full HTML pages from direct DB reads + group fs reads.
- **HTMX partials** (2): `GET /dash/tasks/x/list`,
  `GET /dash/activity/x/recent` — `<tbody>` fragments refreshed every
  10s by the parent page.
- **Memory edits** (2): `PUT|DELETE /dash/memory/{folder}/{rel}` —
  allow-listed file edits under a group folder (`MEMORY.md`,
  `.claude/CLAUDE.md`, flat `*.md` under `diary/`, `facts/`, `users/`,
  `episodes/`). Symlink-escape and path-traversal hardened.
- **Per-user secrets** (4): `GET /dash/me/secrets`,
  `POST /dash/me/secrets`, `PATCH|DELETE /dash/me/secrets/{key}`.
  Identity-bound to `X-User-Sub`; writes require same-origin.
  (`me_secrets.go`)
- **Routes editor — admin** (5): `GET /dash/routes/`,
  `POST /dash/routes/`, `PATCH /dash/routes/{id}`,
  `DELETE /dash/routes/{id}`, `POST /dash/routes/{id}/delete` (HTML-form
  fallback for DELETE). (`routes_admin.go`)
- **Groups CRUD — admin** (6): `GET /dash/groups/new`,
  `POST /dash/groups/new`, `GET /dash/groups/{folder}/settings`,
  `POST /dash/groups/{folder}/settings`,
  `DELETE /dash/groups/{folder}`, `POST /dash/groups/{folder}/delete`
  (form fallback). (`groups_admin.go`)
- **Invites — admin** (2): `GET/POST /dash/invites/` — list pending
  invites, create new (admin-gated), revoke by token. (`invites.go`)
- **Grants viewer — admin** (2): `GET/POST /dash/groups/{folder}/grants`
  — ACL rows for the folder, add form (all 32 actions), per-row revoke.
  (`grants_admin.go`)
- **Model selector**: dropdown in groups settings writes `groups.model`;
  container runner passes `ARIZUKO_MODEL` env var.
- **Skill toggle**: checkbox list in groups settings creates/removes
  `.disabled` marker at `<group>/.claude/skills/<name>/.disabled`.
- **Workspace links**: settings page "Agent files" section links to
  `/dav/{folder}/CLAUDE.md`, `PERSONA.md`, `MEMORY.md`, `workspace/`.

## Auth

dashd enforces auth itself; it does not assume the upstream filtered
the request:

- `requireUser` (`me_secrets.go`) — reads `X-User-Sub` set by proxyd;
  401 if absent. Used by every non-public route.
- `requireSameOrigin` — CSRF guard on state-changing requests; rejects
  cross-origin `Origin`/`Referer`.
- `requireAdmin` (`authz.go`) — calls `auth.Authorize(store, caller,
"admin", scope, nil)` with `caller.Extra` populated from
  `X-User-Groups`. Used by every routes/groups write verb. Scope is
  the target folder for per-group writes, `**` for global creates.

Read pages (`/dash/status/`, `/dash/tasks/`, etc.) currently render the
full DB to any authenticated user; per-group scoping of read pages is
future work.

## Entry points

- Binary: `dashd/main.go`
- Listen: `$DASH_PORT` (default `:8080`)

## Dependencies

- `auth` — token/scope check (`Authorize`, `Caller`)
- `store` — DB access for routes, groups, secrets, acl, invites
- `core` — config helpers used by groups/routes admin handlers
- `container` — group folder bootstrap on create
- `groupfolder` — folder path validation, parent resolution
- `chanlib` — request log middleware
- `diary` — extract `summary:` frontmatter for memory listings
- `theme` — shared CSS + theme toggle script
- `html_helpers.go` — shared page shell, nav, banner helpers (vendored htmx)

## Configuration

- `DATA_DIR` — base for `<DATA_DIR>/store/messages.db` and `<DATA_DIR>/groups/`
- `DB_PATH` — explicit DB DSN; overrides the `DATA_DIR`-derived path
- `DASH_PORT` — listen port (default `:8080`)

`INSTANCE_NAME` is not read by dashd today; the portal header is
static.

## Health signal

`GET /health` returns 200 unconditionally once the process is up. DB
liveness is observed by the read pages (errors surface as red banners).
Typical deploy reaches dashd through `proxyd` at `/dash/`.

## Files

- `main.go` — bootstrap, route table, portal, status, tasks, activity,
  groups (read-only), memory read/write.
- `me_secrets.go` — per-user secrets CRUD + shared `requireUser` /
  `requireSameOrigin` helpers.
- `routes_admin.go` — routes table CRUD (admin-gated).
- `groups_admin.go` — group create / settings (model, skills, workspace links) / delete (admin-gated).
- `grants_admin.go` — ACL viewer + add/revoke per folder (admin-gated).
- `tools_admin.go` — read-only MCP tool browser at `/dash/groups/{folder}/tools`.
- `tasks_admin.go` — task detail + run logs + pause/resume at `/dash/tasks/{id}`.
- `route_tokens.go` — `/dash/tokens/` chat/webhook route-token list + issue + revoke.
- `channels.go` — `/dash/channels/whatsapp/pair` pairing form + live status.
- `invites.go` — invite list + create + revoke (admin-gated).
- `profile.go` — `/dash/profile/` view of linked subs for the caller.
- `authz.go` — `requireAdmin` wrapper around `auth.Authorize`.
- `html_helpers.go` — shared page shell, nav, htmx boost, banner helpers.

## Future work

Per-group scoping of read pages (`/dash/status/`, `/dash/activity/`,
`/dash/memory/`) so a non-admin sees only folders they hold a grant on;
migration of direct DB reads to `gated/v1/*` once that surface lands
(`specs/5/5-uniform-mcp-rest.md`).
