# dashd

The operator cockpit's hub and its cross-cutting pages: an HTMX portal for
operators and signed-in users, per-user secret CRUD, and admin-gated CRUD
over routes, groups, invites, grants and tasks.

## Which door each page reads through

<!-- twin:dashd -->

```
browser
   │ /dash/...
   ▼
proxyd: auth "user", strips and stamps X-User-*, adds service:proxyd
   │ /dash/<daemon>/ ──────────────► that daemon's own page
   │ /dash/ and the rest
   ▼
┌──────────────────────────────────────────────────────────────┐
│ dashd: guard (ProxydTransit), then requireUser, requireAdmin,│
│ requireVisible or requireOperator                            │
└──────┬──────────────────────────────┬────────────────────────┘
       │ own store key                │ service:dashd bearer,
       │ StoreReach: routd, onbod     │ empty folder claim
       ▼                              ▼
┌──────────────────────────┐   ┌───────────────────────────────┐
│ routd tables             │   │ routd /v1: groups, tasks,     │
│ acl for every gate       │   │ routes, acl, route_tokens,    │
│ admin writes, AsUser     │   │ cost/summary, messages,       │
│ portal counts, packages, │   │ sessions, principals,         │
│ cooldowns, route joins   │   │ routing, engagement,          │
│ secret and env key names │   │ pending_actions, audit        │
│ onbod tables             │   │ onbod /v1: invites, audit     │
│ invite writes            │   │ runed, authd /v1/audit        │
└──────────────────────────┘   │ proxyd /v1/proxyd_routes      │
                               │ whapd /v1/pair                │
                               └───────────────────────────────┘
groups/<folder>/ files: memory pages, skill toggles, group bootstrap
```

<!-- /twin -->

dashd owns no table. It holds a store key for routd's and onbod's tables and
for nobody else's (`StoreReach`, `store/owner_reach.go:16`), and it opens
both through `store.OpenOwner` (`openSiblingOwner`, `main.go`). An owner
store that does not open means that profile is off, and the page banners
"store unavailable". runed's, authd's, proxyd's and whapd's state reach
dashd only over their `/v1` with the `service:dashd` bearer. Live process
state, such as runed's runs and breaker, exists only in that daemon's
memory, so no table read could show it.

**Writes stay direct on the owner's tables.** `Store.AsUser` puts the
operator's sub on the `audit_log` row, inside the write's own batch. A call
over `/v1` carries only `service:dashd`, and routd accepts a forwarded user
only from `service:proxyd` (`auth.ProxydTransit`). Routing dashd's writes
through `/v1` would either lose the human from the audit row or make dashd
a second identity stamper. Two kinds of write go over `/v1` all the same,
because the owner runs a lifecycle around them. An engagement disengage
must be contained on the window's claiming folder, and its audit row names
`service:dashd`. An approval verdict commits together with the agent's
answer, and dashd forwards the reviewer's sub as a parameter.

**Reads go over the owner's `/v1` where the owner serves the read.** Two
read paths to one consumer drift. The exception: the owner refuses to
serve the read. `/dash/me/secrets` and `/dash/me/env` list key names from
`secrets` directly, because routd's `secrets` resource is write-only on
purpose. The test is "would the owner serve this if asked"; serving it
would publish an API that lists secrets. The other direct reads in the
figure are joins and counts no `/v1` route answers yet.

**An unreachable owner renders as an error banner, never as empty data.**
"No routes" or "costs nothing" are statements an operator acts on, and
they are false when the source did not answer.

**A page about one daemon lives in that daemon; a page across daemons, or
operator-global, lives in dashd.** Each daemon serves its own
`/dash/<daemon>/` from handlers beside its `/v1`, and proxyd routes the
prefix there. dashd holds no renderer for another daemon's runtime, which
would be a second renderer of the same data.

## The hub links only to what proxyd routes

`/dash/services/` is a tile grid over a static daemon list (`services.go`).
Each request probes every `<daemon>:8080/health` at once with a 500 ms
timeout and no cached loop, so one slow daemon cannot stall the page.
A tile has three states: `ok` for a 2xx, `err` for a refusal, timeout or
non-2xx, `unknown` when the name does not resolve, that is, the container is
not deployed. There is no `warn` state: `/health` is public on every daemon,
so a probe can never see a scope denial. `davd` has no `/health`, so its
tile probes `GET /`.

Whether a tile links is read from the live route table: dashd asks proxyd's
`GET /v1/proxyd_routes` which `/dash/<daemon>/` rows exist. The route entry
is the registration; there is no autodiscovery. When proxyd does not
answer, the hub falls back to the core routes compiled in
(`compose.ReservedRoutePaths`), so it never links at a path that 404s. The
probe never hides a link: a daemon that is down is the one an operator
wants to open.

## Surface

59 routes registered in `dash.registerRoutes` (`main.go`). The bullets below
group the ones with behaviour worth stating.

The per-daemon pages are NOT among them: `/dash/routd/`, `/dash/runed/`,
`/dash/authd/`, `/dash/proxyd/`, `/dash/onbod/`, `/dash/timed/`,
`/dash/webd/`, `/dash/ttsd/` and each channel adapter's page are served by
their owners. `davd` serves none, `davd/README.md`.

- **Public** (3): `GET /health`, `GET /openapi.json`, `GET /dash/assets/htmx.min.js` — no auth required.
- **Portal** (1): `GET /dash/` — tile grid with status/tasks dots.
- **Read pages** (7): `GET /dash/status/`, `/dash/tasks/`, `/dash/activity/`, `/dash/groups/`, `/dash/memory/`, `/dash/profile/`, `/dash/packages/` (installed-package rows from `installed_packages`). Scope-filtered to caller's visible folders. `/dash/status/` also carries the **Answering** section for the operator only (`**`): the window's turn outcomes and every chat whose newest human message is newer than its newest agent reply, read from routd's `GET /v1/routing/answering`. Each row carries the loop's verdict, so a chat that answers nothing by configuration (`observed`, `no route`) reads differently from one where a turn was due and nothing came (`no reply`). Operator-only is load-bearing — the turn counts are one instance-wide fold that cannot be narrowed per viewer, and a held chat belongs to no folder. (`answering_section.go`)
- **HTMX partials** (2): `GET /dash/tasks/x/list`, `GET /dash/activity/x/recent` — 10s-polled `<tbody>` fragments.
- **Memory edits** (2): `PUT|DELETE /dash/memory/` — admin-gated writes to allow-listed group files (`MEMORY.md`, `.claude/CLAUDE.md`, flat `*.md` under `diary/`, `facts/`, `users/`, `episodes/`). Symlink-escape hardened. No locking — last write wins, and the agent re-reads its files at session start.
- **Per-user secrets** (4): `GET|POST /dash/me/secrets`, `PATCH|DELETE /dash/me/secrets/{key}` — capability credentials (e.g. `GITHUB_TOKEN`); identity-bound to `X-User-Sub`; writes require same-origin and are sealed at rest under `SECRETS_KEY`. `GET` serves an HTML management page (Accept `text/html`) and JSON otherwise. (`me_secrets.go`)
- **Per-user env-profile keys** (4): `GET|POST /dash/me/env`, `PATCH|DELETE /dash/me/env/{key}` — the model credentials (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`); user-scoped only, a user's key overrides the operator default for their spawns. Same identity-bound + sealed-at-rest treatment as secrets. (`me_env.go`)
- **Tasks** (3): `GET /dash/tasks/{id}`, `POST /dash/tasks/`, `POST /dash/tasks/{id}/{action}` — detail, create, pause/resume/cancel. (`tasks_admin.go`) The create form parses the schedule with `core.NextRun` only (`taskNextRun`), so it refuses an RFC3339 one-shot that routd's `parseTaskSchedule` accepts from the agent's `schedule_task`: one schedule, accepted on one face and refused on the other.
- **Routes editor** (5): `GET|POST /dash/routes/`, `PATCH|DELETE /dash/routes/{id}`, `POST /dash/routes/{id}/delete` — admin-gated CRUD. The GET also renders the **Unrouted chats** section for the operator only (`**`): every chat routd holds because no rule claims it, read from routd's `GET /v1/routing/unrouted`, each with a claim form that posts the same `POST /dash/routes/` write. Operator-only is load-bearing — a held chat belongs to no folder, so no folder admin has a claim to see it. (`routes_admin.go`)
- **Groups CRUD** (8): `GET|POST /dash/groups/new`, `GET|POST /dash/groups/{folder...}` (dispatchers to settings/delete/grants), `DELETE /dash/groups/{folder...}`, `GET /dash/groups/{folder}/tools|grants`, `POST /dash/groups/{folder}/grants|grants/revoke` — admin-gated. Model selector dropdown writes `groups.model`; skill toggles create/remove `.disabled` markers. (`groups_admin.go`, `grants_admin.go`, `tools_admin.go`)
- **Route tokens** (2): `GET /dash/tokens/{folder}/`, `POST /dash/tokens/{folder}/{jid}/revoke` — list + revoke, NO MINT. Reads scope-filtered; revoke admin-gated. Issuance belongs to the resource owner — routd's `POST /v1/route_tokens/{chat,hook}`, the agent's `issue_chat_link`/`issue_webhook`/`issue_pairing_link`, and `arizuko token`. A mint form here was a second writer over `route_tokens` with its own label validation (BUGS `V2`); there is no token minting anywhere in the cockpit. (`route_tokens.go`)
- **Invites** (3): `GET|POST /dash/invites/`, `POST /dash/invites/{ref}/revoke` — operator-only (`**`). (`invites.go`)
- **WhatsApp re-pair** (3): `GET /dash/channels/whatsapp/pair`, `GET /dash/channels/whatsapp/pair/status`, `POST /dash/channels/whatsapp/pair/start` — operator-only (`**`), proxies to whapd with service:dashd bearer. (`channels.go`)
- **Proactive view** (1): `GET /dash/proactive/` — operator-only (`**`) read-only view of proactive interjection: per-group `mode:` / quiet hours / parse error, parsed through the shared `proactive.Parse` routd's scanner gates on, plus per-chat last-fired from routd's `chat_proactive`. No control — `mode:` stays operator-edited in the group's `CLAUDE.md` (single source, no DB/file drift) and the cooldown is mandatory. A banner names `PROACTIVE_ENABLED`; dashd cannot read routd's env, so it states the rule rather than a live value. (`proactive_page.go`)
- **Engagement** (2): `GET /dash/engagement/` — operator-only (`**`) view of engagement: every live engagement window (chat, thread, group, time left), i.e. the conversations the agent keeps answering in without being addressed. `POST /dash/engagement/disengage` ends one now, behind a confirm — the remedy for a runaway engagement, which otherwise had none but waiting out the TTL. Both go through routd's `/v1/engagement` over HTTP with the service:dashd bearer (`routes:read` + `routes:write`), NOT `chat_reply_state` out of `dbRoutd`: routd owns those columns, contains the write on the window's CLAIMING folder, and writes the `audit_log` row inside the mutation's own transaction (`DB.SetEngagementAudited`) — none of which a direct-DB write here would do. Operator-only is load-bearing — dashd's service token has an empty folder claim, which routd reads as list-all. (`engagement_page.go`)
- **Approvals** (2): `GET /dash/approvals/`, `POST /dash/approvals/{id}/resolve` — the review queue for held tool calls: what is waiting (group, tool, full arguments, chat, age) with per-row approve/reject + note, and the recent verdicts. Open to any signed-in caller and scoped per row: the proxyd-verified sub forwards as `reviewer`, and routd returns and accepts only the folders that human holds `admin` on (`resolveHoldTx`). Both go over routd's `/v1/pending_actions` with the service:dashd bearer (`pending_actions:read` + `pending_actions:write`), NOT direct SQL: routd owns the resolution lifecycle — the verdict commits together with the agent's answer, and a direct-DB verdict would decide a call no agent is told about. `reviewed_by` names the human. The portal banners the count of held calls THIS caller may answer, through the same `admin` question. (`approvals_page.go`)
- **Audit** (2): `GET /dash/audit/` — operator-only (`**`) view of the audit trail, FEDERATED across routd, runed, authd and onbod via each daemon's `GET /v1/audit` with the service:dashd bearer (`audit:read`), NOT by opening runed's or authd's tables: dashd reaches routd's and onbod's tables and nobody else's, and a second reader of a table whose owner publishes no contract is a recorded defect class. Merged newest-first with a Source column; the "older" cursor is composite because an `audit_log` id is unique within one owner and not across them, and paging on the instant alone would drop rows that share one. A source that fails gets a banner naming it and the survivors still render — silence would report "nothing happened in runed" when runed simply did not answer. onbod joins only when its store opens: it is an optional profile, and a daemon that was never deployed is not a source that failed. (`audit_page.go`)

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

- `DATA_DIR` — base for `<DATA_DIR>/groups/`
- `DYNAMODB_URL` + `AWS_*` + `STORE_CA` — the store server dashd reads routd's
  and onbod's tables on, through `store.OpenOwner`. An owner that
  will not open means its profile is off, and that view banners "store
  unavailable"
- `DASH_PORT` — listen port (default `:8080`)
- `ARIZUKO_INSTANCE` — instance name for audit/obs; read at startup
- `AUTHD_URL` — authd's base URL, used twice: the JWKS endpoint (unset → identity verification disabled, local dev) and the backend for the `/dash/authd/` control plane (default `http://authd:8080` via `backendURL`)
- `AUTHD_SERVICE_KEY` — ES256 private key for service:dashd bearer (whapd re-pair proxy); optional
- `AUTHD_SERVICE_NAME` — service name (default `dashd`)
- `WHAPD_URL` — whapd base URL for re-pair proxy (default `http://whapd:8080`)
- `PROXYD_URL` — proxyd base URL (default `http://proxyd:8080`). The hub asks its `/v1/proxyd_routes` which daemon pages are actually routed; on failure it degrades to the core routes compiled in, so an operator is never linked at a path that 404s
- `RUNED_URL`, `ONBOD_URL` — two of the audit sources (default `http://runed:8080`, `http://onbod:8080`). `RUNED_URL` is also where the group delete page stops each removed folder's run (`POST /v1/runs/stop`, dashd's `runs:kill`). It probes `GET /health` first: runed unreachable answers 503 with nothing removed, and `arizuko group <instance> rm -f <folder>` is the escape. A stop that fails after the purge answers 200 with a warning, because the rows are gone.
- `ROUTER_URL` — routd base URL (default `http://routd:8080`); the `/v1` reads in the figure above go here. Compose emits none of these; all resolve through `backendURL`, which names the compose service on the fixed in-container `:8080`. Set one only to override.

Group settings enumerate active system and exact-folder package skills through
the shared inventory. Marker reads and writes hold the package lock. Unsafe
paths and writable managed payloads report visible errors before settings writes.

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
- `route_tokens.go` — route-token list + revoke. Mints nothing.
- `channels.go` — WhatsApp re-pair form + live status (operator-only).
- `audit_page.go` — the audit trail, federated over routd's, runed's, authd's and onbod's `GET /v1/audit` (operator-only).
- `approvals_page.go` — the held-call review queue + approve/reject over routd's `/v1/pending_actions`, scoped to the folders the caller holds `admin` on.
- `engagement_page.go` — live engagement windows + force-disengage over routd's `/v1/engagement`; also `bearerCall`, the shared service:dashd transport (operator-only).
- `proactive_page.go` — per-group proactive mode + per-chat cooldown, read-only (operator-only).
- `services.go` — the daemon hub: health probes, the static tile list and the route-table read that decides each link.
- `usage_page.go` — the usage analytics view; every figure over routd's `/v1`.
- `me_connections.go`, `me_env.go` — the surrogate-OAuth "Connect &lt;provider&gt;" dance and the per-user env view.
- `packages_page.go` — installed-packages read view (`/dash/packages/`).
- `invites.go` — invite list + create + revoke (operator-only).
- `profile.go` — linked subs view + provider link buttons.
- `html_helpers.go` — page shell, nav, htmx boost, banner helpers.

## What it does not do

- **No per-daemon control plane.** Each `/dash/<daemon>/` is served by its
  daemon; proxyd routes each prefix to the owner. A copy here would be
  unreachable code rendering the same data twice.
- **No second reader of another owner's table.** dashd holds routd's and
  onbod's handles and nobody else's. Where it needs runed's or authd's rows it
  asks over `/v1` with the `service:dashd` bearer.
- **Nothing that mints.** The route-token page lists and revokes; routd mints.
