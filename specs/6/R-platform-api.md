---
status: spec
---

# Platform Control API

A federated typed surface for controlling the entire arizuko platform —
each daemon owns its own tables, exposes its own `/v1/*`, mints or
validates capability tokens. Consumed by the dashboard (operators),
exposed as MCP (agents), and the canonical contract for any future
client. No unified handler library; ownership matches the existing
process boundaries. Coordination is by signed token, not shared
function call.

## Why now

Phase 6 ships products. A product without an operator-facing way to
manage it is half a product. The dashboard already covers reading;
mutations live in scattered MCP tools that operators can't reach from
a browser. Closing that gap shouldn't be a giant central refactor —
each daemon already knows its own domain. The spec just makes that
ownership explicit and gives every daemon a uniform `/v1/*` surface.

## Constraints

- **Each daemon owns its tables.** The daemon that writes a table is
  the one that exposes its `/v1/*` and runs its migrations. No second
  daemon reaches into another's tables; they call its API.
- **One uniform contract per daemon.** Every daemon serving control
  operations exposes the same shape: `/v1/<resource>` with REST verbs,
  uniform error envelope, uniform pagination, uniform token validation.
- **Capability tokens, not grant lookups.** Authorization is a signed
  token carrying `(sub, scopes, exp)`. Each daemon validates the
  signature and checks scope vs operation. Grant rules live where they
  are issued from, not in every backend.
- **Shared verification, distributed issuance.** Token format and
  signing key live in the `auth/` library; every daemon imports it.
  Issuance is per-lifecycle: proxyd at user login, MCP host at agent
  spawn, onbod at invite redemption.
- **No new daemon for auth.** A future `authd` is one refactor away
  if centralized revocation/audit becomes load-bearing — until then,
  distributed minting fits the existing topology.
- **Every issuer uses the same `auth/` library.** proxyd (user
  sessions), MCP host (agent caps), onbod (invite redemption) all
  call `auth.Mint(...)` and produce the same JWT shape. Onbod is not
  a special case — its tokens look like proxyd's, just with narrower
  scopes.
- **Stable contract.** `/v1/` namespace, additive evolution, breaking
  changes get `/v2/`. MCP tool names track resources but can be
  renamed for agent ergonomics.

## Today's surface (compressed)

**MCP via `ipc/` (inside gated)** — 31+ tools, comprehensive write
surface, tier-gated and grant-gated, JSON-RPC over a unix socket per
group.

**dashd** — 12 GET routes returning HTML/HTMX. Reads multiple tables
directly from the shared DB. **No** group/route/task/grant/invite write
paths exist in the dashboard.

**webd** — `/api/groups`, `/api/groups/{folder}/topics`,
`/api/groups/{folder}/messages` for the chat UI; `/x/*` HTMX partials;
`/mcp` exposes 3 hand-picked tools for browser agents; `/slink/*` is
the public unauthenticated guest surface; `/send`, `/typing`,
`/v1/round_done` for channel adapter inbound.

The capability gap: everything an agent can do via MCP, the dashboard
cannot do via UI. This spec closes that — by adding `/v1/*` to the
daemons that already hold the data.

## Daemon ownership

Each daemon owns its tables and serves the matching API. No
cross-daemon reaches into another's storage.

| Daemon     | Owns                                                 | Serves                                                                                   |
| ---------- | ---------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| **gated**  | groups, routes, sessions, channels, messages, grants | `/v1/groups`, `/v1/routes`, `/v1/sessions`, `/v1/channels`, `/v1/messages`, `/v1/grants` |
| **timed**  | scheduled_tasks, task_run_logs                       | `/v1/tasks`                                                                              |
| **webd**   | web_routes, vhosts, slink tokens                     | `/v1/web-routes`, `/v1/vhosts` (chat reads stay on existing `/api/*`)                    |
| **onbod**  | invites, admissions, auth_users                      | `/v1/invites`, `/v1/users`                                                               |
| **proxyd** | OAuth state, session-token issuance                  | `/auth/*` (existing); becomes a token issuer surface                                     |
| **dashd**  | nothing — aggregator UI calling the above            | HTML/HTMX over `/v1/*` of others                                                         |

`grants` lives with gated for now (gated already runs migrations and
has the broadest schema authority). If a future split puts grants
elsewhere, the issuance flow doesn't change — issuers query the grants
owner at mint time.

**proxyd routes** are not in the ownership table: they're declared
per-daemon in `template/services/<name>.toml` `[[proxyd_route]]`
blocks (and equivalent for core daemons), aggregated by
`compose/compose.go` at compose-generate time, and consumed by proxyd
via `PROXYD_ROUTES_JSON`. See `specs/6/2-proxyd-standalone.md` "Per-
daemon route declarations". proxyd remains the verifier — it doesn't
"own" a routes table; it executes the operator-composed one.

## Resource model

Each `/v1/<resource>` namespace exposes the same five verbs:

- `GET /v1/<resource>` — list (paginated, filtered)
- `GET /v1/<resource>/{id}` — get one
- `POST /v1/<resource>` — create
- `PATCH /v1/<resource>/{id}` — partial update
- `DELETE /v1/<resource>/{id}` — delete

Action verbs only when no state-mutation framing fits. E.g.
`POST /v1/messages` IS "send a message" — no separate `/send`.
`POST /v1/sessions/{id}:reset` for action-shaped (Google's `:verb`
convention).

`inspect_*` MCP tools collapse into list endpoints with filters.
`inspect_routing(jid)` → `GET gated/v1/routes?jid=…`;
`inspect_messages(chat_jid, limit, before)` →
`GET gated/v1/messages?chat_jid=…&limit=…&before=…`.

## Token model

A platform token is a signed JWT (HS256, signed with `AUTH_SECRET`)
carrying:

```json
{
  "sub": "user:abc123" | "agent:atlas/main" | "key:k_42",
  "scope": ["groups:read", "tasks:write", "messages:send", "*:read", ...],
  "folder": "atlas/main",
  "tier": 2,
  "iat": 1735000000,
  "exp": 1735003600,
  "iss": "proxyd" | "mcp-host" | "onbod"
}
```

**Scopes** are `<resource>:<verb>` pairs. `*:*` is root. `tasks:*`
all task verbs. Wildcards resolve against `(resource, verb)` of the
incoming request. Scopes are minted from grant rules at issuance time
(snapshot), so revoking grants requires either token expiry or an
explicit revocation list — short TTLs are the default, revocation lists
deferred until needed.

**`folder`** scopes the token to a subtree. `atlas/main` token can
operate on `atlas/main/*` resources but not on `rhias/*`. Root tokens
omit `folder` (or set `folder: "*"`).

**`tier`** is denormalized from grants for fast tier-gated checks
(today's existing tier system is preserved).

### Issuance sites

| Issuer                                       | Triggers on                         | Token shape                                                               | Verifier                    |
| -------------------------------------------- | ----------------------------------- | ------------------------------------------------------------------------- | --------------------------- |
| **proxyd**                                   | OAuth login (existing)              | user session, scopes from grants                                          | Every backend daemon        |
| **MCP host** (currently `ipc/` inside gated) | Agent container spawn / socket bind | agent capability, folder-scoped                                           | Every daemon agent talks to |
| **onbod**                                    | Invite redemption / admission       | initial user session, narrow scope; **same `auth/` lib, same JWT format** | Every backend daemon        |
| **dashd**                                    | API key creation (operator action)  | long-lived, narrow scope                                                  | Every backend daemon        |

The MCP host mints the agent token at the moment a container spawns,
embedding `(folder, tier, grants snapshot)`. The token is passed into
the container as an env var or via the MCP socket handshake. Agents use
it for any HTTP call to a sibling daemon's `/v1/*`.

### Verification

`auth/Verify(token) → Identity{sub, scope, folder, tier}` lives in the
shared `auth/` library. Every daemon imports it. No daemon implements
its own verification.

Per-request auth at every `/v1/*` endpoint:

```go
ident, err := auth.VerifyHTTP(r)        // signature + exp + iss
if !auth.HasScope(ident, "tasks", "write") { return 403 }
if !auth.MatchesFolder(ident, taskFolder) { return 403 }
// proceed
```

## MCP federation

Today the MCP host (`ipc/` inside gated) handles every tool call
locally. Some tools touch tables gated owns (groups, routes, sessions,
channels, messages, grants) — those stay local. Tools touching tables
owned by other daemons (tasks → timed, invites → onbod) become
**HTTP forwards** with the agent's capability token:

```
agent → ipc.tools/call(pause_task, ...)
       → ipc validates token scope (tasks:write)
       → ipc HTTP-PATCH timed/v1/tasks/{id} {status: paused}
              with Authorization: Bearer <agent-token>
       → timed verifies token, checks scope, executes, returns
       → ipc returns result to agent as JSON-RPC
```

Single MCP socket per agent (today's model). The socket host becomes
a thin API gateway for the agent — local in-process calls for own-
domain operations, HTTP forwards for cross-daemon. Latency added only
where the daemon split demands it.

Alternative considered: multiple MCP sockets per agent (one per
daemon), agent registers all of them. Rejected for now — more sockets,
more configuration, no operator benefit. Revisit if MCP host becomes a
bottleneck.

Agent-tailored tool names (`send_voice`, `escalate_group`,
`schedule_task`) keep their narrative shape. Each maps to one or a
small composition of `/v1/*` calls — the orthogonal core. Tools never
reach past `/v1/*` into another daemon's DB.

## Dashboard

`dashd` becomes an aggregator. It holds an operator session token
(issued by proxyd at login) and makes `/v1/*` calls to gated, timed,
webd, onbod to render its pages. Adds write paths (forms posting to
`POST/PATCH/DELETE` of the relevant daemon) wherever today's UI is
read-only.

Concrete migrations:

| dashd page        | Old (direct DB)                        | New (federated API)                                                                   |
| ----------------- | -------------------------------------- | ------------------------------------------------------------------------------------- |
| `/dash/groups/`   | reads `groups`, `routes`               | `gated/v1/groups`, `gated/v1/routes`                                                  |
| `/dash/tasks/`    | reads `scheduled_tasks`                | `timed/v1/tasks` (+ form → `POST timed/v1/tasks`)                                     |
| `/dash/activity/` | reads `messages` LIMIT 50              | `gated/v1/messages?limit=50&order=desc`                                               |
| `/dash/status/`   | reads `groups`, `sessions`, `channels` | `gated/v1/groups`, `gated/v1/sessions`, `gated/v1/channels`                           |
| `/dash/memory/`   | direct fs read/write                   | new resource on whichever daemon owns the group fs (likely gated): `gated/v1/files/*` |
| `/dash/profile/`  | reads `auth_users`                     | `onbod/v1/users/{sub}`                                                                |

dashd never touches tables directly after this refactor.

## What changes vs. status quo

| Today                                               | After                                                              |
| --------------------------------------------------- | ------------------------------------------------------------------ |
| Every daemon has a DB connection, queries any table | Each daemon owns its tables; others call its `/v1/*`               |
| Grants stored centrally, looked up per request      | Grants minted into tokens at issuance; verified per request        |
| No HTTP API for control operations                  | Every daemon serves `/v1/*` for its domain                         |
| Agent tools are inline in `ipc/ipc.go`              | MCP host calls own logic locally; cross-daemon via HTTP            |
| Dashboard reads DB directly, can't write            | Dashboard is `/v1/*` client only; full read+write parity           |
| Token format: signed identity headers from proxyd   | Same wire format, extended to carry scopes; uniform across daemons |

## What this spec is not

- Not a UI redesign. The dashboard's information architecture is
  separate work.
- Not stability for MCP tool names. `/v1/*` per daemon is the stable
  contract; MCP tool names can evolve for agent ergonomics.
- Not a permission model overhaul. Grant rules stay; this spec just
  packages them as scopes in tokens at issuance.
- Not a transport spec. JSON over HTTP for `/v1/*`, JSON-RPC over
  unix socket for MCP — both already in use.
- Not storage federation. Daemons can still share the SQLite DB
  underneath; what changes is who is allowed to write each table. A
  future per-daemon-DB migration is a separate spec.

## Implementation phases

Each phase is independently shippable.

1. **Token format + verification.** Extend `auth/` library to mint
   and verify scoped JWTs. Migrate proxyd's existing session-token
   minter to the new format. Backward-compatible header carrying
   during cutover. **Deliverable:** any daemon can verify a scoped
   token.

2. **gated `/v1/*`.** Mount HTTP server on gated for its owned
   resources (groups, routes, sessions, channels, messages, grants).
   Every endpoint validates token + checks scope. Document with
   per-daemon `openapi.json`. **Deliverable:** the largest surface is
   network-callable.

3. **timed `/v1/tasks` + onbod `/v1/{invites,users}`.** Each daemon
   gets its slice. **Deliverable:** all control operations have an
   HTTP endpoint.

4. **MCP host upgrade.** `ipc/` mints agent capability tokens at
   socket bind. Tools touching foreign-domain resources convert to
   HTTP forwards with the agent token. Local-domain tools stay
   in-process. **Deliverable:** MCP federation working.

5. **Dashboard migration.** dashd ports each page to call the
   relevant daemon's `/v1/*`. Add write forms for tasks, routes,
   grants, invites, web-routes, vhosts. **Deliverable:** parity with
   MCP — operators can do everything an agent can.

6. **Cleanup.** Remove direct-DB access from non-owner daemons. Each
   daemon's go.mod imports trim down. **Deliverable:** ownership
   boundary enforced by code structure, not just convention.

## Open

- **Token TTL & revocation.** Short TTL (1h?) is the default; revocation
  list deferred. Long-lived API keys (dashd-issued) need a revocation
  table — when, where?
- **Cross-daemon transactions.** Today some operations span tables
  (e.g. "create group → seed skills → register routes"). Once
  ownership is split, these become saga-shaped. Acceptable since each
  step is idempotent; flag if not.
- **MCP socket lifecycle vs. token TTL.** Container can outlive its
  token if a turn runs longer than TTL. Refresh via the MCP host?
  Accept the rare expiry? Deferred — short-lived turns mostly avoid
  this.
- **dashd hosting decision.** Keep dashd as Go HTML server, or fold
  HTML rendering into webd? Lean: keep, separable from this spec.
- **`authd` daemon.** Adding it later is one refactor (move minting
  out of proxyd/onbod/MCP-host into a shared service). Triggered by:
  centralized revocation list, audit log, OIDC delegation.
- **Bulk endpoints.** `POST /v1/routes:batch` or many POSTs? Lean:
  many; bulk only on demand.

## Code pointers

- `auth/` — middleware-only today; gains `Mint(...)`, `VerifyHTTP(...)`,
  `HasScope(...)`, `MatchesFolder(...)`. The single source of truth for
  token format.
- `proxyd/` — existing OAuth + JWT; extends issuance to carry scopes
  derived from grants.
- `gated/` (entry) + `ipc/` (MCP host subsystem) — gains its `/v1/*`
  HTTP front; MCP host gains agent-token minter and HTTP-forward
  client for cross-daemon tools.
- `timed/`, `onbod/`, `webd/` — each gains a small `v1.go` for its
  owned resources.
- `dashd/` — replaces direct `store.*` calls with `/v1/*` HTTP calls
  using the operator's session token.
- `core/grants.go` — stays as the rule evaluator; called only at
  issuance sites (proxyd, MCP host, dashd) to compute scopes from
  rules.
