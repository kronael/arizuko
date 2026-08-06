# dashd

Operator dashboard daemon: HTMX views over `messages.db`, per-user secret
CRUD, and admin-gated CRUD over routes, groups, invites, and grants.

## Purpose

Standalone HTMX portal for operators and signed-in users. Reads most of
its data directly from the shared SQLite (`messages.db`) and the group
filesystem; write paths cover routes, groups, per-user secrets,
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
- **Read pages** (7): `GET /dash/status/`, `/dash/tasks/`, `/dash/activity/`, `/dash/groups/`, `/dash/memory/`, `/dash/profile/`, `/dash/packages/` (installed-package rows from `installed_packages`, spec 5/28). Scope-filtered to caller's visible folders.
- **HTMX partials** (2): `GET /dash/tasks/x/list`, `GET /dash/activity/x/recent` — 10s-polled `<tbody>` fragments.
- **Memory edits** (2): `PUT|DELETE /dash/memory/` — admin-gated writes to allow-listed group files (`MEMORY.md`, `.claude/CLAUDE.md`, flat `*.md` under `diary/`, `facts/`, `users/`, `episodes/`). Symlink-escape hardened.
- **Per-user secrets** (4): `GET|POST /dash/me/secrets`, `PATCH|DELETE /dash/me/secrets/{key}` — capability credentials (e.g. `GITHUB_TOKEN`); identity-bound to `X-User-Sub`; writes require same-origin and are sealed at rest under `SECRETS_KEY`. `GET` serves an HTML management page (Accept `text/html`) and JSON otherwise. (`me_secrets.go`)
- **Per-user env-profile keys** (4): `GET|POST /dash/me/env`, `PATCH|DELETE /dash/me/env/{key}` — the model credentials (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`); user-scoped only, a user's key overrides the operator default for their spawns. Same identity-bound + sealed-at-rest treatment as secrets. (`me_env.go`)
- **Tasks** (3): `GET /dash/tasks/{id}`, `POST /dash/tasks/`, `POST /dash/tasks/{id}/{action}` — detail, create, pause/resume. (`tasks_admin.go`)
- **Routes editor** (5): `GET|POST /dash/routes/`, `PATCH|DELETE /dash/routes/{id}`, `POST /dash/routes/{id}/delete` — admin-gated CRUD. (`routes_admin.go`)
- **Groups CRUD** (8): `GET|POST /dash/groups/new`, `GET|POST /dash/groups/{folder...}` (dispatchers to settings/delete/grants), `DELETE /dash/groups/{folder...}`, `GET /dash/groups/{folder}/tools|grants`, `POST /dash/groups/{folder}/grants|grants/revoke` — admin-gated. Model selector dropdown writes `groups.model`; skill toggles create/remove `.disabled` markers. (`groups_admin.go`, `grants_admin.go`, `tools_admin.go`)
- **Route tokens** (3): `GET|POST /dash/tokens/{folder}/`, `POST /dash/tokens/{folder}/{jid}/revoke` — issue chat/webhook tokens, revoke. Admin-gated writes; reads scope-filtered. (`route_tokens.go`)
- **Invites** (3): `GET|POST /dash/invites/`, `POST /dash/invites/{ref}/revoke` — operator-only (`**`). (`invites.go`)
- **WhatsApp re-pair** (3): `GET /dash/channels/whatsapp/pair`, `GET /dash/channels/whatsapp/pair/status`, `POST /dash/channels/whatsapp/pair/start` — operator-only (`**`), proxies to whapd with service:dashd bearer. (`channels.go`)
- **Proactive view** (1): `GET /dash/proactive/` — operator-only (`**`) read-only view of spec 5/6: per-group `mode:` / quiet hours / parse error, parsed through the shared `proactive.Parse` routd's scanner gates on, plus per-chat last-fired from routd's `chat_proactive`. No control — `mode:` stays operator-edited in the group's `CLAUDE.md` (single source, no DB/file drift) and the cooldown is mandatory by spec. A banner names `PROACTIVE_ENABLED`; dashd cannot read routd's env, so it states the rule rather than a live value. (`proactive_page.go`)
- **Engagement view** (1): `GET /dash/engagement/` — operator-only (`**`) read-only view of spec 5/G: every live engagement window (chat, thread, group, time left), i.e. the conversations the agent keeps answering in without being addressed. Reads routd's `GET /v1/engagement` list face over HTTP with the service:dashd bearer (`routes:read`), NOT `chat_reply_state` out of `dbRoutd` — routd owns those columns and applies the containment. No control: a window expires at TTL, and both early writers (`disengage`, `POST /v1/engagement`) keep the audit row in routd's own transaction, so a button here would be a third writer. Operator-only is load-bearing — dashd's service token has an empty folder claim, which routd reads as list-all. (`engagement_page.go`)
- **proxyd control plane** (3): `GET|POST /dash/proxyd/`, `POST /dash/proxyd/delete` — operator-only (`**`) view + create + delete of proxyd's reverse-proxy route table. proxyd OWNS `proxyd_routes`, so all three go over HTTP to its `/v1/proxyd_routes` with the service:dashd bearer and the caller's `X-User-*` forwarded; proxyd writes the single audit row in the mutation's own transaction. (`proxyd_page.go`)

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
- `PROXYD_URL` — proxyd base URL for the `/dash/proxyd/` control plane (default `http://proxyd:8080`, matching webd)
- `RUNED_URL` — runed base URL for the `/dash/runed/` kill proxy (default `http://runed:8080`).
- `ROUTER_URL` — routd base URL for the `/dash/engagement/` view (default `http://routd:8080`). Compose emits none of these three; all resolve through `backendURL`, which names the compose service on the fixed in-container `:8080`. Set one only to override.

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
- `proxyd_page.go` — proxyd route table view + add + delete over `/v1/proxyd_routes` (operator-only).
- `packages_page.go` — installed-packages read view (`/dash/packages/`).
- `invites.go` — invite list + create + revoke (operator-only).
- `profile.go` — linked subs view + provider link buttons.
- `html_helpers.go` — page shell, nav, htmx boost, banner helpers.

## Future work

Migration of direct DB reads to `routd/v1/*` REST surface once that lands (`specs/5/17-openapi-mcp.md`, rolled out by `specs/5/16-mcp-rest-unification.md`). Read scoping already implemented.
