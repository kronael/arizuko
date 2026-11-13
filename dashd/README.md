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
for nobody else's (`StoreReach`, `store/owner_reach.go`), and it opens
both through `store.OpenOwner` (`openSiblingOwner`, `main.go`). An owner
store that does not open means that profile is off, and the page banners
"store unavailable". runed's, authd's, proxyd's and whapd's state reach
dashd only over their `/v1` with the `service:dashd` bearer. runed owns its
durable run records and exposes them through its API.

**Writes stay direct on the owner's tables.** `Store.AsUser` puts the
operator's sub on the `audit_log` row, inside the write's own batch. A call
over `/v1` carries only `service:dashd`, and routd accepts a forwarded user
only from `service:proxyd` (`auth.ProxydTransit`). Routing dashd's writes
through `/v1` would either lose the human from the audit row or make dashd
a second identity stamper. Two kinds of write go over `/v1` all the same,
because the owner runs a lifecycle around them. An engagement disengage
must be contained on the window's claiming folder, and its audit row names
`service:dashd`. An approval verdict releases the original suspended call,
and dashd forwards the reviewer's sub as a parameter.

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
(`compose.ReservedRoutePaths`), so fallback links reflect the compiled core routes rather than a fresh backend registration. The
probe never hides a link: a daemon that is down is the one an operator
wants to open.

## Surface

`dash.registerRoutes` in `main.go` declares the HTTP routes. The bullets below
group the ones with behaviour worth stating.

The per-daemon pages are NOT among them: `/dash/routd/`, `/dash/runed/`,
`/dash/authd/`, `/dash/proxyd/`, `/dash/onbod/`,
`/dash/webd/`, `/dash/ttsd/` and each channel adapter's page are served by
their owners. `davd` serves none, `davd/README.md`.

- **Public** (3): `GET /health`, `GET /openapi.json`, `GET /dash/assets/htmx.min.js` — no auth required.
- **Portal** (1): `GET /dash/` — tile grid with status/tasks dots.
- **Read pages** (7): `GET /dash/status/`, `/dash/tasks/`, `/dash/activity/`, `/dash/groups/`, `/dash/memory/`, `/dash/profile/`, `/dash/packages/` (installed-package rows from `installed_packages`). Scope-filtered to caller's visible folders. `/dash/status/` also carries the **Answering** section for the operator only (`**`): the window's turn outcomes and every chat whose newest human message is newer than its newest agent reply, read from routd's `GET /v1/routing/answering`. Each row carries the loop's verdict, so a chat that answers nothing by configuration (`observed`, `no route`) reads differently from one where a turn was due and nothing came (`no reply`). Operator-only is load-bearing — the turn counts are one instance-wide fold that cannot be narrowed per viewer, and a held chat belongs to no folder. (`answering_section.go`)
- **HTMX partials** (2): `GET /dash/tasks/x/list`, `GET /dash/activity/x/recent` — 10s-polled `<tbody>` fragments.
- **Memory edits** (2): `PUT|DELETE /dash/memory/` — admin-gated writes to allow-listed group files (`MEMORY.md`, `.claude/CLAUDE.md`, flat `*.md` under `diary/`, `facts/`, `users/`, `episodes/`). Symlink-escape hardened. No locking — last write wins, and the agent re-reads its files at session start.
- **Per-user secrets** (4): `GET|POST /dash/me/secrets`, `PATCH|DELETE /dash/me/secrets/{key}` — capability credentials (e.g. `GITHUB_TOKEN`); identity-bound to `X-User-Sub`; writes require same-origin and are sealed at rest under `SECRETS_KEY`. `GET` serves an HTML management page (Accept `text/html`) and JSON otherwise. (`me_secrets.go`)
- **Per-user env-profile keys** (4): `GET|POST /dash/me/env`, `PATCH|DELETE /dash/me/env/{key}` — the model credentials (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`); user-scoped only, a user's key overrides the operator default for their spawns. Same identity-bound + sealed-at-rest treatment as secrets. (`me_env.go`)
- **Tasks** (3): `GET /dash/tasks/{id}`, `POST /dash/tasks/`, `POST /dash/tasks/{id}/{action}` — detail, create, pause/resume/cancel. (`tasks_admin.go`) The create form checks the schedule with `core.NextRun` only and stores no `next_run`, so it refuses an RFC3339 one-shot that routd's `parseTaskSchedule` accepts from the agent's `schedule_task`: one schedule, accepted on one face and refused on the other.
- **Routes editor** (5): `GET|POST /dash/routes/`, `PATCH|DELETE /dash/routes/{id}`, `POST /dash/routes/{id}/delete` — admin-gated CRUD. The GET also renders the **Unrouted chats** section for the operator only (`**`): every chat routd holds because no rule claims it, read from routd's `GET /v1/routing/unrouted`, each with a claim form that posts the same `POST /dash/routes/` write. Operator-only is load-bearing — a held chat belongs to no folder, so no folder admin has a claim to see it. (`routes_admin.go`)
- **Groups CRUD** (8; the delete route removes nothing: it answers 409 with the command `arizuko group -i <instance> rm <folder>`, because a purge also removes grants that bind outside the folder, and admin on one folder does not give that right): `GET|POST /dash/groups/new`, `GET|POST /dash/groups/{folder...}` (dispatchers to settings/delete/grants), `DELETE /dash/groups/{folder...}`, `GET /dash/groups/{folder}/tools|grants`, `POST /dash/groups/{folder}/grants|grants/revoke` — admin-gated. Agent field writes the handler in `groups.config.agent`. Model selector dropdown writes `groups.model`; skill toggles create/remove `.disabled` markers. (`groups_admin.go`, `grants_admin.go`, `tools_admin.go`)
- **Route tokens** (2): `GET /dash/tokens/{folder}/`, `POST /dash/tokens/{folder}/{jid}/revoke` — list + revoke, NO MINT. Reads scope-filtered; revoke admin-gated. Issuance belongs to the resource owner — routd's `POST /v1/route_tokens/{chat,hook}`, the agent's `issue_chat_link`/`issue_webhook`/`issue_pairing_link`, and `arizuko token`. The cockpit exposes no token mint form. (`route_tokens.go`)
- **Invites** (3): `GET|POST /dash/invites/`, `POST /dash/invites/{ref}/revoke` — operator-only (`**`). (`invites.go`)
- **WhatsApp re-pair** (3): `GET /dash/channels/whatsapp/pair`, `GET /dash/channels/whatsapp/pair/status`, `POST /dash/channels/whatsapp/pair/start` — operator-only (`**`), proxies to whapd with service:dashd bearer. (`channels.go`)
- **Engagement** (2): `GET /dash/engagement/` — operator-only (`**`) view of engagement: every live engagement window (chat, thread, group, time left), i.e. the conversations the agent keeps answering in without being addressed. `POST /dash/engagement/disengage` ends one now, behind a confirm — the remedy for a runaway engagement, which otherwise had none but waiting out the TTL. Both go through routd's `/v1/engagement` over HTTP with the service:dashd bearer (`routes:read` + `routes:write`), NOT `chat_reply_state` out of `dbRoutd`: routd owns those columns, contains the write on the window's CLAIMING folder, and writes the `audit_log` row inside the mutation's own transaction (`DB.SetEngagementAudited`) — none of which a direct-DB write here would do. Operator-only is load-bearing — dashd's service token has an empty folder claim, which routd reads as list-all. (`engagement_page.go`)
- **Model pauses** (2): `GET /dash/model-pauses/` shows each folder's reason, episode, next attempt, attempts and queued input count. `POST /dash/model-pauses/clear` submits the displayed folder, episode and probe attempt to routd's `/v1/model_pauses/clear`. Both require the operator. The POST also checks CSRF. Both use the `model_pauses` API with the `service:dashd` bearer. (`model_pauses.go`)
- **Approvals** (2): `GET /dash/approvals/`, `POST /dash/approvals/{id}/resolve` — held calls and recent verdicts, with routd's rendered request and standing rule, approve/reject controls and a note. The proxyd-verified sub forwards as `reviewer`. routd lists and resolves only folders that human holds `admin` on (`resolveHoldInto`). Both routes use routd's `/v1/pending_actions` with the `service:dashd` bearer and `pending_actions:read` or `pending_actions:write`. The verdict releases the original suspended call. `reviewed_by` names the human. The portal count uses the same folder check. (`approvals_page.go`)
- **Audit** (2): `GET /dash/audit/` — operator-only (`**`) view of the audit trail, FEDERATED across routd, runed, authd and onbod via each daemon's `GET /v1/audit` with the service:dashd bearer (`audit:read`), NOT by opening runed's or authd's tables: dashd reaches routd's and onbod's tables and nobody else's, and a second reader of a table whose owner publishes no contract is a recorded defect class. Merged newest-first with a Source column; the "older" cursor is composite because an `audit_log` id is unique within one owner and not across them, and paging on the instant alone would drop rows that share one. A source that fails gets a banner naming it and the survivors still render — silence would report "nothing happened in runed" when runed simply did not answer. onbod joins only when its store opens: it is an optional profile, and a daemon that was never deployed is not a source that failed. (`audit_page.go`)

## Auth

Every non-public route runs through `d.guard` — verifies proxyd's ES256 service:proxyd bearer (proves transit through proxyd, which stamps X-User-Sub/-Groups) before trusting the end-user identity. No verifier (AUTHD_URL unset) → open (local dev). Then:

- `requireUser` (`me_secrets.go`) — reads `X-User-Sub`; 401 if absent.
- `requireSameOrigin` (`me_secrets.go`) — CSRF guard on mutations; rejects cross-origin `Origin`/`Referer`.
- `requireWriter` checks the signed-in user, same origin and store availability.
- `requireAdmin` calls `requireWriter`, then checks admin on the target with the verified sub alone. `X-User-Groups` supplies no authority.
- `requireVisible` requires interact on the folder, including for operators. Settings, tokens and tools use this check for their read pages.
- `requireOperator` requires admin on `**` for instance-wide pages. The gate confines calls that use dashd's service bearer with an empty folder claim.

`callerScope` reads one `auth.Policy` for the request. Folder pages, rows and
counts use `visible`, which checks interact on each folder. Operators pass
the same visibility check. Matching local rows decide before the defaults in
`auth/defaults.go`.

Grant add and revoke call `requireWriter`, then `CheckGrant` with the admin
question and the mutation in one read. Each write proves that policy version.

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

| Variable                                   | Default                                         | Meaning                                                                               |
| ------------------------------------------ | ----------------------------------------------- | ------------------------------------------------------------------------------------- |
| `DATA_DIR`                                 | empty                                           | Group files live under DATA_DIR/groups; empty resolves the relative groups directory. |
| `DASH_PORT`                                | `:8080`                                         | HTTP port; a missing leading colon is added.                                          |
| `ASSISTANT_NAME`                           | `arizuko`                                       | Displayed brand name.                                                                 |
| `DASH_ACCENT_COLOR`                        | unset                                           | Dashboard accent override.                                                            |
| `APP_SRC_DIR`                              | `HOST_APP_DIR`, then empty                      | Release ant asset directory for skill inspection.                                     |
| `HOST_APP_DIR`                             | empty                                           | Fallback ant asset directory.                                                         |
| `AUTHD_URL`                                | unset for verifier; `http://authd:8080` for API | JWKS gate and authd control-plane base.                                               |
| `AUTHD_SERVICE_KEY`                        | unset                                           | Bootstrap secret for service:dashd.                                                   |
| `AUTHD_SERVICE_NAME`                       | `dashd`                                         | Service-token exchange principal.                                                     |
| `RUNED_URL`                                | `http://runed:8080`                             | Execution/audit API.                                                                  |
| `PROXYD_URL`                               | `http://proxyd:8080`                            | Proxy route inventory.                                                                |
| `ROUTER_URL`                               | `http://routd:8080`                             | Conversation, task, cost, and approval APIs.                                          |
| `ONBOD_URL`                                | `http://onbod:8080`                             | Onboarding/audit API.                                                                 |
| `WHAPD_URL`                                | `http://whapd:8080`                             | Pairing API base.                                                                     |
| `WHATSAPP_PHONE`                           | unset                                           | Initial value on the pairing form.                                                    |
| `SECRETS_KEY`                              | unset                                           | Keyring for user secrets and surrogate OAuth credentials.                             |
| `AUTH_SECRET`                              | empty                                           | Surrogate OAuth state secret.                                                         |
| `AUTH_BASE_URL`                            | `WEB_HOST`                                      | Public surrogate callback origin.                                                     |
| `WEB_HOST`                                 | empty                                           | Fallback callback origin.                                                             |
| `DYNAMODB_URL`                             | required                                        | Store endpoint.                                                                       |
| `AWS_REGION`                               | `us-east-1`                                     | Store signing region.                                                                 |
| `AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY` | required                                        | Daemon store identity.                                                                |
| `STORE_CA`                                 | unset                                           | Base64 PEM endpoint certificate; unset uses system trust.                             |
| `ARIZUKO_INSTANCE`                         | unset                                           | Audit and observability instance label.                                               |
| `LOG_LEVEL`                                | `info`                                          | JSON stderr verbosity.                                                                |
| `OTEL_EXPORTER_OTLP_ENDPOINT`              | unset                                           | Optional OTLP logs through obs.                                                       |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`       | unset                                           | Optional OTLP trace export; independent of the log endpoint.                          |
| `METRICS_ENABLED`                          | `false`                                         | Literal true mounts public GET /metrics.                                              |

Surrogate provider credentials use `SURROGATE_<PROVIDER>_CLIENT_ID` and `SURROGATE_<PROVIDER>_CLIENT_SECRET` via the shared broker.

## Health signal

`GET /health` returns 200 unconditionally once the process is up. DB
liveness is observed by the read pages (errors surface as red banners).
Typical deploy reaches dashd through `proxyd` at `/dash/`.

## Files

- `main.go` — bootstrap, route table, portal, status, tasks, activity, groups (list + routes), memory read/write.
- `authz.go` — `requireAdmin`, `requireVisible`, `requireOperator`, `callerScope`, `visible`, scope-filtered count helpers.
- `me_secrets.go` — per-user secrets CRUD + `requireUser` / `requireSameOrigin`.
- `routes_admin.go` — routes CRUD (admin-gated).
- `groups_admin.go` — group create / settings (model, skills, workspace links) / delete (admin-gated; answers with the host command and removes nothing).
- `grants_admin.go` — ACL viewer + add/revoke per folder (admin-gated).
- `tools_admin.go` — read-only MCP tool browser per folder.
- `tasks_admin.go` — task detail + run logs + create + pause/resume.
- `route_tokens.go` — route-token list + revoke. Mints nothing.
- `channels.go` — WhatsApp re-pair form + live status (operator-only).
- `audit_page.go` — the audit trail, federated over routd's, runed's, authd's and onbod's `GET /v1/audit` (operator-only).
- `approvals_page.go` — the held-call review queue + approve/reject over routd's `/v1/pending_actions`, scoped to the folders the caller holds `admin` on.
- `engagement_page.go` — live engagement windows + force-disengage over routd's `/v1/engagement`; also `bearerCall`, the shared service:dashd transport (operator-only).
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

## State and request failures

`StoreReach["dashd"]` reaches `routd` and `onbod`; dashd owns no table or resreg resource.
Direct joins and admin writes use routd's `groups`, `acl`, `acl_membership`, `routes`, `route_tokens`, `scheduled_tasks`,
`secrets`, `installed_packages`, `github_sources`, chat state, and onbod's invite/admission entities.
Audited direct writes record the human sub in the owning `audit_log`.
The owner APIs provide message, session, task, cost, approval, and audit reads described above.

Disk operations access group memory, settings, and selected package files under `DATA_DIR/groups` and `DATA_DIR/packages`.
No dashd database, durable retry queue, MCP socket, or IPC listener exists.
There is no periodic service health loop. A services-page request probes the static service inventory concurrently with a 500 ms timeout.
The hub's route lookup is request-driven. A failed lookup uses compiled core paths; it does not establish backend availability.
Shutdown closes HTTP immediately. A failed backend read displays an error rather than silently replacing the result with an empty list.
Opening routd's store fails boot; an unavailable optional onbod owner produces a profile error on affected pages.
Grep `open routd's store`, `dashd started`, `store unavailable`, and request errors for the failing /v1 path.
`GET /metrics` reports HTTP request count and duration when enabled; `/health` tests only process liveness.

Known limits include the grant form's delegation checks ([BUGS L540](../BUGS.md)), one-shot schedule validation ([BUGS L485](../BUGS.md)),
and the pairing proxy's service identity ([BUGS L492](../BUGS.md)).
Tests exercise gates and stubbed API reads; local documentation checks do not render a live operator session.

Sources: [boot and routes](main.go), [service probes](services.go), [WhatsApp proxy](channels.go),
[direct-store readers](dashstore/dashstore.go), [owner reach](../store/owner_reach.go).

## Mounted route patterns

The table transcribes the mux patterns; the auth rules above apply to each surface.

| Method and path                                | Owner |
| ---------------------------------------------- | ----- |
| `GET /metrics`                                 | dashd |
| `GET /health`                                  | dashd |
| `GET /openapi.json`                            | dashd |
| `GET /dash/assets/htmx.min.js`                 | dashd |
| `GET /dash/`                                   | dashd |
| `GET /dash/services/`                          | dashd |
| `GET /dash/packages/`                          | dashd |
| `GET /dash/audit/`                             | dashd |
| `GET /dash/approvals/`                         | dashd |
| `POST /dash/approvals/{id}/resolve`            | dashd |
| `GET /dash/usage/`                             | dashd |
| `GET /dash/engagement/`                        | dashd |
| `GET /dash/model-pauses/`                      | dashd |
| `POST /dash/model-pauses/clear`                | dashd |
| `POST /dash/engagement/disengage`              | dashd |
| `GET /dash/status/`                            | dashd |
| `GET /dash/tasks/`                             | dashd |
| `GET /dash/activity/`                          | dashd |
| `GET /dash/groups/{folder...}`                 | dashd |
| `GET /dash/memory/`                            | dashd |
| `GET /dash/profile/`                           | dashd |
| `PUT /dash/memory/`                            | dashd |
| `DELETE /dash/memory/`                         | dashd |
| `GET /dash/tasks/x/list`                       | dashd |
| `GET /dash/activity/x/recent`                  | dashd |
| `GET /dash/tasks/{id}`                         | dashd |
| `POST /dash/tasks/`                            | dashd |
| `POST /dash/tasks/{id}/{action}`               | dashd |
| `GET /dash/me/secrets`                         | dashd |
| `POST /dash/me/secrets`                        | dashd |
| `PATCH /dash/me/secrets/{key}`                 | dashd |
| `DELETE /dash/me/secrets/{key}`                | dashd |
| `GET /dash/me/env`                             | dashd |
| `POST /dash/me/env`                            | dashd |
| `PATCH /dash/me/env/{key}`                     | dashd |
| `DELETE /dash/me/env/{key}`                    | dashd |
| `GET /dash/me/connections`                     | dashd |
| `POST /dash/me/connections/{provider}/start`   | dashd |
| `GET /dash/me/connections/{provider}/callback` | dashd |
| `DELETE /dash/me/connections/{provider}`       | dashd |
| `GET /dash/channels/whatsapp/pair`             | dashd |
| `GET /dash/channels/whatsapp/pair/status`      | dashd |
| `POST /dash/channels/whatsapp/pair/start`      | dashd |
| `GET /dash/routes/`                            | dashd |
| `POST /dash/routes/`                           | dashd |
| `PATCH /dash/routes/{id}`                      | dashd |
| `DELETE /dash/routes/{id}`                     | dashd |
| `POST /dash/routes/{id}/delete`                | dashd |
| `GET /dash/github_sources/`                    | dashd |
| `POST /dash/github_sources/`                   | dashd |
| `POST /dash/github_sources/delete`             | dashd |
| `GET /dash/groups/new`                         | dashd |
| `POST /dash/groups/new`                        | dashd |
| `POST /dash/groups/{folder...}`                | dashd |
| `DELETE /dash/groups/{folder...}`              | dashd |
| `GET /dash/groups/{folder}/tools`              | dashd |
| `GET /dash/groups/{folder}/grants`             | dashd |
| `POST /dash/groups/{folder}/grants`            | dashd |
| `POST /dash/groups/{folder}/grants/revoke`     | dashd |
| `GET /dash/tokens/{folder}/`                   | dashd |
| `POST /dash/tokens/{folder}/{jid}/revoke`      | dashd |
| `GET /dash/invites/`                           | dashd |
| `POST /dash/invites/`                          | dashd |
| `POST /dash/invites/{ref}/revoke`              | dashd |

JWKS keys load lazily during verification through auth.FetchKeys; there is no fixed background JWKS ticker.
Service tokens refresh on demand within 60 s of expiry, with a 10 s exchange timeout ([auth/service.go](../auth/service.go)).
