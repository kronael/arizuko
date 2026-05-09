---
status: spec
---

# Platform Control API

A single typed surface for controlling the entire arizuko platform —
consumed by the dashboard (operators), exposed as MCP (agents), and
the canonical contract for any future client (CLI, third-party
integrations). Today the surface is split: MCP via `ipc/ipc.go` is the
comprehensive write API; `dashd` is a read-only HTML dashboard hitting
the DB directly; `webd` carries chat-specific HTTP. The split costs
parity, doc surface, and refactor cost. This spec collapses it.

## Why now

Phase 6 ships products. A product without a way for the operator to
manage it is half a product. The dashboard already covers reading;
mutations live in scattered MCP tools that operators can't reach.
Closing that gap is the platform half of the products phase.

## Constraints

- **One handler per capability**, not two. The HTTP route and the
  MCP tool dispatch into the same Go function — they differ only in
  serialization and identity.
- **Least surface, max orthogonality** for the underlying resource
  model. Agents may want tailored verbs on top, but those wrap the
  orthogonal core; they never bypass it.
- **No new daemon.** The handlers live where the data does (`gated`
  owns the DB; runtime tools live in `ipc`); the HTTP front lives in
  `webd` (already routes `/api/`); `dashd` becomes a thin HTML client
  of the API instead of a parallel DB consumer.
- **Stable contract.** `/v1/` namespace, additive evolution, breaking
  changes get `/v2/`. Agent-facing MCP names track the API but can be
  renamed/described independently for agent ergonomics.

## Today's surface (compressed inventory)

**MCP (31+ tools)** — comprehensive write surface, tier-gated and
grant-gated. Maps cleanly to resources: `messages`, `routes`, `tasks`,
`sessions`, `groups`, `grants`, `invites`, `web_routes`, `vhosts`,
plus inspect\_\* read tools. Already JSON-RPC over a unix socket per
group.

**dashd** — 12 GET routes returning HTML/HTMX. Reads `groups`,
`messages`, `routes`, `scheduled_tasks`, `sessions`, `auth_users`,
`task_run_logs`, `channels`. Only mutations: PUT/DELETE on memory
files. **No** group/route/task/grant/invite write paths exist in the
dashboard today.

**webd** — `/api/groups`, `/api/groups/{folder}/topics`, `/api/groups/{folder}/messages`
serve the chat UI; `/x/*` returns HTMX partials of the same. `/mcp`
exposes 3 hand-picked MCP tools (`send_message`, `steer`, `get_round`)
for browser-side agent integration; `/slink/*` is the public
unauthenticated guest surface (token-gated). Channel adapters POST
to `/send`, `/typing`, `/v1/round_done`.

The capability gap is roughly: everything an agent can do via MCP, the
dashboard cannot do via UI. This API closes that.

## Resource model

Eleven resources. Each has list / get / mutate verbs; nothing else.

| Resource   | Path prefix      | Owns                                  |
| ---------- | ---------------- | ------------------------------------- |
| groups     | `/v1/groups`     | folder, parent, name, product, state  |
| routes     | `/v1/routes`     | jid → folder routing rules            |
| tasks      | `/v1/tasks`      | scheduled prompts, owner, cron, state |
| messages   | `/v1/messages`   | read + send (write goes via channels) |
| sessions   | `/v1/sessions`   | per-group session id, reset           |
| grants     | `/v1/grants`     | per-folder grant rules                |
| invites    | `/v1/invites`    | onboarding tokens                     |
| channels   | `/v1/channels`   | registered platform adapters          |
| web_routes | `/v1/web-routes` | /pub, /chat, /x dynamic routes        |
| vhosts     | `/v1/vhosts`     | hostname → folder mapping             |
| users      | `/v1/users`      | auth_users + identity mapping         |

Verbs:

- `GET /v1/<resource>` — list (paginated, filtered)
- `GET /v1/<resource>/{id}` — get one
- `POST /v1/<resource>` — create
- `PATCH /v1/<resource>/{id}` — partial update (e.g. `{status: "paused"}`)
- `DELETE /v1/<resource>/{id}` — delete

Action verbs only when no state-mutation framing fits. E.g.
`POST /v1/messages` IS "send a message" — no separate `/send` endpoint.
`POST /v1/sessions/{id}:reset` for the rare action-shaped case (Google's
`:verb` convention; mechanically distinct from `PATCH`).

Inspection verbs (`inspect_*`) collapse into list endpoints with
filters. `inspect_routing(jid)` becomes `GET /v1/routes?jid=…`;
`inspect_messages(chat_jid, limit, before)` becomes
`GET /v1/messages?chat_jid=…&limit=…&before=…`.

## Architecture

```
                    ┌─────────────┐    ┌─────────────┐
                    │  Dashboard  │    │   Agents    │
                    │  (browser)  │    │ (containers)│
                    └──────┬──────┘    └──────┬──────┘
                           │                   │
                       JSON/REST          MCP JSON-RPC
                           │                   │
                    ┌──────┴────────┬──────────┴─────┐
                    │  webd /v1/*   │   ipc gated.sock│
                    │  (HTTP front) │   (MCP front)   │
                    └──────┬────────┴──────────┬─────┘
                           │                   │
                           └─────────┬─────────┘
                                     │
                          ┌──────────▼──────────┐
                          │  api/handlers.go    │
                          │  (one func per      │
                          │   resource × verb)  │
                          └──────────┬──────────┘
                                     │
                                     ▼
                              store.* + state
```

`api/handlers.go` (new package): pure functions taking a typed
`Identity` (sub, folder, tier, grant set) and a typed request, returning
a typed response. No HTTP, no JSON-RPC. Pure business logic.

Two thin adapters:

- `webd/api.go` — REST adapter. Decodes URL+JSON to typed request,
  derives `Identity` from JWT + grants table, calls handler, encodes
  typed response to JSON.
- `ipc/api_mcp.go` — MCP adapter. Decodes JSON-RPC tool args to typed
  request, derives `Identity` from socket-bound folder+tier+grants,
  calls handler, encodes response to JSON-RPC. Keeps the existing
  per-group socket model.

Both adapters are mechanical; the contract is the handler signature.

## Auth & identity

Identity is the same shape from both surfaces:

```go
type Identity struct {
    Sub    string   // user (HTTP) or "agent:<folder>" (MCP)
    Folder string   // for HTTP: derived from URL or implicit "*"; for MCP: socket-bound
    Tier   int      // 0=root, 1=tenant, 2=send-only, 3+=clamped
    Grants []Grant  // resolved from store.LoadGrants(folder)
}
```

Authorization is **per (resource, verb)** with grants checked the same
way for both surfaces. The grant evaluator already exists; this spec
just routes both fronts through it.

- HTTP auth: JWT from proxyd (existing `auth.RequireSigned`); folder
  derived from path segment; tier looked up by sub.
- MCP auth: socket bind (existing); folder/tier/grants from socket
  context.

If the same user calls `PATCH /v1/tasks/foo` from the dashboard and the
agent calls `pause_task(taskId=foo)` from MCP, they hit identical
`PauseTask(ctx, ident, id)` with identical grant checks.

## Agent-tailored layer

Agents work better with verb-shaped, narrative tool names:
`send_voice`, `escalate_group`, `delegate_group`, `schedule_task` are
clearer than `POST /v1/messages?voice=true&…`. The MCP adapter keeps
those names. They map 1:1 to API calls — sometimes a single call,
sometimes a tiny composition (e.g. `send_voice` = TTS render →
`POST /v1/messages` with attachment).

Rule: every agent-tailored MCP tool is implementable as a thin script
over `/v1/*` calls. If an MCP tool needs capability the API doesn't
have, add it to the API first. The MCP layer never reaches past the
API into the DB directly.

Tool descriptions diverge between surfaces:

- API: machine-oriented OpenAPI spec, terse.
- MCP: agent-oriented prose, examples, when-to-use.

## Dashboard refactor

`dashd` currently makes 12 routes. Each becomes one or more `GET /v1/*`
calls; HTML rendering moves client-side or stays as Go templates that
fetch from `/v1/*` instead of the DB. Concretely:

- `/dash/groups/` → fetches `/v1/groups` + `/v1/routes`, renders.
- `/dash/tasks/` → fetches `/v1/tasks`, renders. Add task scheduling
  form posting to `POST /v1/tasks`.
- `/dash/activity/` → fetches `/v1/messages?limit=50&order=desc`.
- `/dash/memory/` → split into `/v1/groups/{folder}/files/*` (new
  resource) for the file editor.
- `/dash/status/` → fetches `/v1/groups`, `/v1/sessions`, `/v1/channels`.

New dashboard pages get write paths for free: any `POST/PATCH/DELETE`
the API supports has a dashboard form behind the same grant check.

`dashd` itself can either stay as the HTML host (rendering server-side
against `/v1/*`) or be deleted in favor of an HTMX shell embedded in
webd. Either is valid; deferring that choice.

## Pagination, filtering, errors

Conventions, applied uniformly:

- List endpoints: `?limit=`, `?cursor=` (opaque), `?order=asc|desc`.
- Filtering: query string per documented field (`?folder=…&status=…`).
- Errors: `{error: {code: "STRING_CODE", message: "human", details: {…}}}`.
- Codes are stable, machine-readable: `not_found`, `forbidden`,
  `invalid_argument`, `conflict`, `unauthenticated`, `unavailable`.

MCP errors map: `forbidden` → JSON-RPC `-32004 forbidden`, etc.

## What this spec is not

- Not a UI redesign. The dashboard's information architecture is
  separate work; this spec only changes what it consumes.
- Not a stability guarantee for MCP tool names. `/v1/*` is the stable
  contract; agent-facing tool names can evolve.
- Not a permission model overhaul. The grant evaluator stays;
  this spec just centralizes invocation.
- Not a transport spec. JSON over HTTP for the API, JSON-RPC over unix
  socket for MCP — both already in use.

## Implementation phases

1. **Extract handlers** — create `api/` package. Move business logic
   out of `ipc/ipc.go` handlers and `dashd/handlers.go` queries into
   `api/<resource>.go` files. Both existing surfaces dispatch to the
   new handlers; behaviour unchanged. Pure refactor, no API exposed
   yet.
2. **Mount HTTP front** — add `/v1/*` routes to webd, JWT-gated, with
   per-resource handlers calling into `api/`. Document with an
   OpenAPI spec at `/v1/openapi.json`.
3. **Migrate dashboard** — port `dashd` GETs to `/v1/*` calls. Add
   write forms for the gaps: tasks, routes, grants, invites,
   web_routes, vhosts.
4. **MCP audit** — verify every MCP tool dispatches via `api/`. Where
   a tool encodes agent-tailored composition, document that and keep.
   Where a tool duplicates API logic, replace with a thin call.
5. **Document for agents** — `ant/skills/self/SKILL.md` already lists
   MCP tools; add a top-level "platform API" pointer for agents that
   want to reach beyond their tier (e.g. `oracle` skill could query
   `/v1/messages` cross-folder if grants allow).

Each phase is independently shippable and reversible.

## Open

- **Dashboard hosting**: keep `dashd` as separate Go service (current),
  or fold into webd? Lean: keep, simpler to evolve UI.
- **Streaming**: API is request/response; SSE for live updates lives
  on webd's existing `/slink/stream` and dashd's HTMX polling. A
  unified subscribe model is later work.
- **Bulk endpoints**: `POST /v1/routes:batch` or just multiple POSTs?
  Lean: multiple POSTs; bulk only if the dashboard demands it.
- **Versioning of MCP tool descriptions**: as agents update via
  migrations, do MCP tool descriptions migrate too? Today they're
  baked into `ipc/ipc.go`; if descriptions become user-editable per
  product, that's a separate spec.
- **Third-party API consumers**: rate limits, API keys (separate from
  proxyd JWT), webhook callbacks. Defer until first external need.

## Code pointers

- Today: `ipc/ipc.go` (MCP tools), `ipc/inspect.go` (read tools),
  `dashd/handlers.go` (dashboard reads), `webd/api.go` (chat API),
  `auth/middleware.go` (JWT gate), `core/grants.go` (grant evaluator).
- After phase 1: `api/<resource>.go` per resource, with `ipc/` and
  `dashd/` calling in.
- After phase 2: `webd/v1.go` mounting REST front; `webd/openapi.go`
  generating spec.
