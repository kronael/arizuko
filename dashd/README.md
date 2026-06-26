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

None. Schema and migrations live in the owning daemons — `routes`,
`groups`, `acl`, `secrets` in routd's `routd.db`; `invites` in onbod's
`onbod.db`. dashd (FS-mounted) holds read+write connections and writes
those tables directly via the `store` package; it never migrates.

## Surface

39 routes registered in `dash.registerRoutes` (`main.go`).

- **Public** (3): `GET /health`, `GET /openapi.json`, `GET /dash/assets/htmx.min.js` — no auth required.
- **Portal** (1): `GET /dash/` — tile grid with status/tasks dots.
- **Read pages** (6): `GET /dash/status/`, `/dash/tasks/`, `/dash/activity/`, `/dash/groups/`, `/dash/memory/`, `/dash/profile/`. Scope-filtered to caller's visible folders.
- **HTMX partials** (2): `GET /dash/tasks/x/list`, `GET /dash/activity/x/recent` — 10s-polled `<tbody>` fragments.
- **Memory edits** (2): `PUT|DELETE /dash/memory/` — admin-gated writes to allow-listed group files (`MEMORY.md`, `.claude/CLAUDE.md`, flat `*.md` under `diary/`, `facts/`, `users/`, `episodes/`). Symlink-escape hardened.
- **Per-user secrets** (4): `GET|POST /dash/me/secrets`, `PATCH|DELETE /dash/me/secrets/{key}` — identity-bound to `X-User-Sub`; writes require same-origin and are sealed at rest under `SECRETS_KEY`. `GET` serves an HTML management page (Accept `text/html`) and JSON otherwise. (`me_secrets.go`)
- **Tasks** (3): `GET /dash/tasks/{id}`, `POST /dash/tasks/`, `POST /dash/tasks/{id}/{action}` — detail, create, pause/resume. (`tasks_admin.go`)
- **Routes editor** (5): `GET|POST /dash/routes/`, `PATCH|DELETE /dash/routes/{id}`, `POST /dash/routes/{id}/delete` — admin-gated CRUD. (`routes_admin.go`)
- **Groups CRUD** (8): `GET|POST /dash/groups/new`, `GET|POST /dash/groups/{folder...}` (dispatchers to settings/delete/grants), `DELETE /dash/groups/{folder...}`, `GET /dash/groups/{folder}/tools|grants`, `POST /dash/groups/{folder}/grants|grants/revoke` — admin-gated. Model selector dropdown writes `groups.model`; skill toggles create/remove `.disabled` markers. (`groups_admin.go`, `grants_admin.go`, `tools_admin.go`)
- **Route tokens** (3): `GET|POST /dash/tokens/{folder}/`, `POST /dash/tokens/{folder}/{jid}/revoke` — issue chat/webhook tokens, revoke. Admin-gated writes; reads scope-filtered. (`route_tokens.go`)
- **Invites** (3): `GET|POST /dash/invites/`, `POST /dash/invites/{token}/revoke` — operator-only (`**`). (`invites.go`)
- **WhatsApp re-pair** (3): `GET /dash/channels/whatsapp/pair`, `GET /dash/channels/whatsapp/pair/status`, `POST /dash/channels/whatsapp/pair/start` — operator-only (`**`), proxies to whapd with service:dashd bearer. (`channels.go`)

## Auth

Every non-public route runs through `d.guard` — verifies proxyd's ES256 service:proxyd bearer (proves transit through proxyd, which stamps X-User-Sub/-Groups) before trusting the end-user identity. No verifier (AUTHD_URL unset) → open (local dev). Then:

- `requireUser` (`me_secrets.go`) — reads `X-User-Sub`; 401 if absent.
- `requireSameOrigin` (`me_secrets.go`) — CSRF guard on mutations; rejects cross-origin `Origin`/`Referer`.
- `requireAdmin` (`authz.go`) — calls `auth.Authorize(store, caller, "admin", scope, nil)` with `caller.Extra` from `X-User-Groups`. Scope is target folder or `**` for global creates. Used by write verbs.
- `requireVisible` (`authz.go`) — gates per-folder GETs to non-operators; 403 if caller's grants don't cover the folder. Used by settings/tokens/tools read pages.
- `requireOperator` (`authz.go`) — gates `**`-scoped surfaces (invites, whatsapp re-pair); 403 for non-operators.

Read surfaces (`/dash/status/`, `/dash/tasks/`, `/dash/activity/`, `/dash/groups/`, `/dash/memory/`) filter rows via `callerScope` + `visible` — non-operators see only folders they hold grants on (direct or subtree). Operators (`**`) see everything.

## Entry points

- Binary: `dashd/main.go`
- Listen: `DASH_PORT` (default `:8080`)

## Dependencies

- `auth` — `Authorize`, `Caller`, `KeySet`, `ProxydTransit`, `ServiceToken`
- `store` — routes, groups, secrets, acl, invites, tasks, messages, sessions, route_tokens
- `audit` — audit log emission
- `obs` — OTLP setup
- `resreg` — OpenAPI handler
- `core` — `MsgID` for audit rows
- `container` — group folder bootstrap on create
- `groupfolder` — folder path validation, parent resolution, `JidFolder`
- `chanlib` — request log middleware, `EnvOr`
- `diary` — `ExtractSummary` for memory listings
- `theme` — shared CSS + theme toggle script

## Configuration

- `DATA_DIR` — base for `<DATA_DIR>/store/*.db` and `<DATA_DIR>/groups/`
- `DB_PATH` — explicit messages.db DSN; overrides `DATA_DIR/store/messages.db`
- `DASH_PORT` — listen port (default `:8080`)
- `ARIZUKO_INSTANCE` — instance name for audit/obs; read at startup
- `AUTHD_URL` — authd JWKS endpoint; unset → identity verification disabled (local dev)
- `AUTHD_SERVICE_KEY` — ES256 private key for service:dashd bearer (whapd re-pair proxy); optional
- `AUTHD_SERVICE_NAME` — service name (default `dashd`)
- `HOST_APP_DIR` — app source path for enumerating stock skills in groups settings
- `WHAPD_URL` — whapd base URL for re-pair proxy (default `http://whapd:8080`)

## Health signal

`GET /health` returns 200 unconditionally once the process is up. DB
liveness is observed by the read pages (errors surface as red banners).
Typical deploy reaches dashd through `proxyd` at `/dash/`.

## Files

- `main.go` — bootstrap, route table, portal, status, tasks, activity, groups (list + routes), memory read/write.
- `authz.go` — `requireAdmin`, `requireVisible`, `requireOperator`, `callerScope`, `visible`, scope-filtered count helpers.
- `me_secrets.go` — per-user secrets CRUD + `requireUser` / `requireSameOrigin`.
- `routes_admin.go` — routes CRUD (admin-gated).
- `groups_admin.go` — group create / settings (model, skills, workspace links) / delete (admin-gated).
- `grants_admin.go` — ACL viewer + add/revoke per folder (admin-gated).
- `tools_admin.go` — read-only MCP tool browser per folder.
- `tasks_admin.go` — task detail + run logs + create + pause/resume.
- `route_tokens.go` — chat/webhook route-token list + issue + revoke.
- `channels.go` — WhatsApp re-pair form + live status (operator-only).
- `invites.go` — invite list + create + revoke (operator-only).
- `profile.go` — linked subs view + provider link buttons.
- `html_helpers.go` — page shell, nav, htmx boost, banner helpers.

## Future work

Migration of direct DB reads to `routd/v1/*` REST surface once that lands (`specs/5/5-uniform-mcp-rest.md`). Read scoping already implemented.
