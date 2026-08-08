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

71 routes registered in `dash.registerRoutes` (`main.go`). The bullets below
group the ones with behaviour worth stating; the per-daemon cockpits
(`/dash/services/`, `/dash/routd/`, `/dash/runed/`, `/dash/authd/`,
`/dash/proxyd/`) are reached from the services hub rather than the nav, and
`dashd/services_test.go` pins each tile's `Built` flag to whether its route is
actually mounted — in both directions, so a shipped page cannot sit behind an
unflipped tile.

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
- **Engagement** (2): `GET /dash/engagement/` — operator-only (`**`) view of spec 5/G: every live engagement window (chat, thread, group, time left), i.e. the conversations the agent keeps answering in without being addressed. `POST /dash/engagement/disengage` ends one now, behind a confirm — the remedy for a runaway engagement, which otherwise had none but waiting out the TTL. Both go through routd's `/v1/engagement` over HTTP with the service:dashd bearer (`routes:read` + `routes:write`), NOT `chat_reply_state` out of `dbRoutd`: routd owns those columns, contains the write on the window's CLAIMING folder, and writes the `audit_log` row inside the mutation's own transaction (`DB.SetEngagementAudited`) — none of which a direct-DB write here would do. Operator-only is load-bearing — dashd's service token has an empty folder claim, which routd reads as list-all. (`engagement_page.go`)
- **Approvals** (2): `GET /dash/approvals/`, `POST /dash/approvals/{id}/resolve` — operator-only (`**`) review queue for spec 5/19's held tool calls: what is waiting (group, tool, full arguments, chat, age) with per-row approve/reject + note, and the recent verdicts. Both go over routd's `/v1/pending_actions` with the service:dashd bearer (`pending_actions:read` + `pending_actions:write`), NOT direct SQL: routd owns the resolution lifecycle — the verdict commits together with the resolution message that makes the ORIGINAL agent re-issue the call (`resolveHoldTx`), and a direct-DB verdict would approve a row no agent is told about. The proxyd-verified operator sub forwards as `reviewer`, so `reviewed_by` names the human. The portal banners the held count (that one number is a direct `pending_actions` count, like the portal's other counts). (`approvals_page.go`)
- **Audit** (2): `GET /dash/audit/` — operator-only (`**`) view of the audit trail, FEDERATED across routd, runed and authd via each daemon's `GET /v1/audit` with the service:dashd bearer (`audit:read`), NOT by opening `runed.db`/`auth.db`: dashd is FS-mounted on `routd.db` alone, and a second reader of a table whose owner publishes no contract is a recorded defect class. Merged newest-first with a Source column; the "older" cursor is composite (`routd:123,runed:45,authd:7`) because `id` is a per-DB AUTOINCREMENT. A source that fails gets a banner naming it and the survivors still render — silence would report "nothing happened in runed" when runed simply did not answer. onbod's table is the fourth owner and not yet federated (BUGS `F35`). (`audit_page.go`)
- **proxyd control plane** (3): `GET|POST /dash/proxyd/`, `POST /dash/proxyd/delete` — operator-only (`**`) view + create + delete of proxyd's reverse-proxy route table. proxyd OWNS `proxyd_routes`, so all three go over HTTP to its `/v1/proxyd_routes` with the service:dashd bearer and the caller's `X-User-*` forwarded; proxyd writes the single audit row in the mutation's own transaction. (`proxyd_page.go`)
- **authd control plane** (2): `GET /dash/authd/`, `POST /dash/authd/revoke` — operator-only (`**`) view of the signing-key lifecycle (which kid signs, what a rotation retired, when a retiring key's passes stop being accepted) and of the refresh-token families behind each login, plus a per-login sign-out. Both go over HTTP to authd's `/v1/signing_keys`, `/v1/sessions` and `DELETE /v1/sessions/{family_id}` with the service:dashd bearer (`signing_keys:read`, `sessions:read`, `sessions:write`), NOT by opening `auth.db`: dashd is not FS-mounted on it and must not be — authd is the sole ES256 signer, so that file is the trust boundary, and HTTP is also what puts the revoke's audit row in the mutation's own transaction. Renders projected rows, never authd's raw answer, so no key material or token value can reach the page. The audit rows are linked to `/dash/audit/`, not repeated. No fleet-wide logout: that means retiring the active key, which spec `5/1` keeps out-of-band, and the page says so rather than omitting it silently. (`authd_page.go`)

## Auth

Every non-public route runs through `d.guard` — verifies proxyd's ES256 service:proxyd bearer (proves transit through proxyd, which stamps X-User-Sub/-Groups) before trusting the end-user identity. No verifier (AUTHD_URL unset) → open (local dev). Then:

- `requireUser` (`me_secrets.go`) — reads `X-User-Sub`; 401 if absent.
- `requireSameOrigin` (`me_secrets.go`) — CSRF guard on mutations; rejects cross-origin `Origin`/`Referer`.
- `requireAdmin` (`authz.go`) — calls `auth.Authorize(store, caller, "admin", scope, nil)` with `caller.Extra` from `X-User-Groups`. Scope is target folder or `**` for global creates. Used by write verbs.
- `requireVisible` (`authz.go`) — gates per-folder GETs to non-operators; 403 if caller's grants don't cover the folder. Used by settings/tokens/tools read pages.
- `requireOperator` (`authz.go`) — gates `**`-scoped surfaces (invites, whatsapp re-pair, proactive, engagement, audit, and the proxyd/runed/routd/authd cockpits); 403 for non-operators. Load-bearing on every page that calls a sibling daemon: dashd presents its own service bearer with an empty folder claim, which those daemons read as list-all, so this gate is the whole containment.

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
- `DB_PATH` — explicit routd.db DSN; overrides `DATA_DIR/store/routd/routd.db`.
  `ONBOD_DB_PATH` / `RUNED_DB_PATH` are the same for the invites and runs
  views (default `DATA_DIR/store/<owner>/<owner>.db`); absent = that daemon's
  profile is off and the view banners "store unavailable"
- `DASH_PORT` — listen port (default `:8080`)
- `ARIZUKO_INSTANCE` — instance name for audit/obs; read at startup
- `AUTHD_URL` — authd's base URL, used twice: the JWKS endpoint (unset → identity verification disabled, local dev) and the backend for the `/dash/authd/` control plane (default `http://authd:8080` via `backendURL`)
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
- `authd_page.go` — signing-key lifecycle + login sessions + per-login sign-out over authd's `/v1` (operator-only).
- `audit_page.go` — the audit trail, federated over routd's, runed's and authd's `GET /v1/audit` (operator-only).
- `approvals_page.go` — the held-call review queue + approve/reject over routd's `/v1/pending_actions` (operator-only).
- `engagement_page.go` — live engagement windows + force-disengage over routd's `/v1/engagement`; also `bearerCall`, the shared service:dashd transport (operator-only).
- `proactive_page.go` — per-group proactive mode + per-chat cooldown, read-only (operator-only).
- `services.go` — the daemon hub: health probes and the `Built` tile list.
- `routd_page.go`, `runed_page.go`, `usage_page.go` — the remaining cockpits and the usage analytics view.
- `packages_page.go` — installed-packages read view (`/dash/packages/`).
- `invites.go` — invite list + create + revoke (operator-only).
- `profile.go` — linked subs view + provider link buttons.
- `html_helpers.go` — page shell, nav, htmx boost, banner helpers.

## Future work

Migration of direct DB reads to `routd/v1/*` REST surface once that lands (`specs/5/17-openapi-mcp.md`, rolled out by `specs/5/16-mcp-rest-unification.md`). Read scoping already implemented.
