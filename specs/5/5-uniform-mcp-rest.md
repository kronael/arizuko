---
status: partial
depends: [1-auth-standalone, 35-proxyd-standalone]
---

# Uniform REST + MCP per resource

> **Canonical principle + federation.** This is the canonical "one
> handler, two faces" statement: it carries the coverage matrix,
> per-resource handler pattern, audit contract, and acceptance criteria,
> and defines the federated `/v1/*` surface. The phase-7 program
> ([`specs/7/index.md`](../7/index.md)) is the downstream continuation
> that finishes the unification across the data model + git-as-truth; it
> does not relocate this spec's content. The MCP face is hand-authored
> here; deriving it from annotated REST is a separate downstream followup
> ([`11/18-openapi-mcp`](../11/18-openapi-mcp.md)), not a dependency.

**Every operator action accessible via both REST (outside, OAuth-gated)
AND MCP (inside, scope-gated), wrapped over a single handler.** One
resource, one handler, two faces. Auth is the only thing that differs.

This spec defines the federated `/v1/*` surface and how tokens are
consumed AND makes explicit that the MCP tool surface is the same
registry viewed through a different auth lens — never a second
hand-written tree. Per-daemon ownership: each daemon owns its tables and
serves its own `/v1/*`; cross-daemon MCP calls become HTTP forwards over
capability tokens. Tokens are minted by the central `authd` daemon (the
sole signer, per [`1-auth-standalone.md`](1-auth-standalone.md)); every
daemon here **consumes + offline-verifies** authd-issued tokens, none
mint their own.

## Why

Today's surface is split asymmetrically:

- `dashd` reads `groups`, `routes`, `messages` direct from the shared
  DB; no write paths exist.
- `ipc/ipc.go` registers the action tools the agent can call
  ([`ipc/README.md` "Tool surface"](../../ipc/README.md)), each a
  hand-written wrapper over `gated`/`store` calls.
- Authorization lives in [`auth/policy.go:14-96`](../../auth/policy.go),
  a hand-maintained switch over tool names; gating is by capability
  scope (tier is dropped — [`U-genericization.md`](U-genericization.md)
  "Capability-vs-tier"). Today proxyd injects signed identity headers
  via [`proxyd/main.go:590-604`](../../proxyd/main.go) and in-container
  agents carry no token — the target model below gives agents an
  `authd`-minted capability token for sibling `/v1/*` calls.

Agents and operators see different sets of operations. The choice of
which surface gets a feature is accidental, not principled.

## The principle

1. **One handler per resource action.** No duplicated logic between
   `ipc/ipc.go` and any `/v1/*` HTTP handler.
2. **Two faces, declared next to the resource.** A `Resource` declares
   its REST endpoints AND its MCP tools; one registration wires both.
3. **`Caller` is surface-agnostic.** Builds on
   [`auth.Identity`](../../auth/README.md) (`Sub`, `Scope`; folder read
   via the arizuko helper over `Identity.Extra["folder"]`, since `auth/`
   is folder-agnostic — [`1-auth-standalone.md`](1-auth-standalone.md));
   handlers read `Caller`, not `*http.Request` or `mcp.ToolRequest`.
4. **Policy is declarative.** A `ScopePred` per action lives next to
   the resource; handler dispatches by action, policy is checked first.

## Caller and Resource shape

```go
type Caller struct {
    Sub    string
    Name   string
    Folder string             // resolved by the arizuko helper from Identity.Extra["folder"]; auth/ stays folder-agnostic
    Scope  []types.Scope      // capability list; authz is scope-match (no tier)
    Claims map[string]string  // JWT claims for ACL row predicates
}

type Resource struct {
    // --- transport half (this spec) ---
    Name      string
    Endpoints []Endpoint   // REST faces
    MCPTools  []MCPTool    // MCP faces
    Authz     func(c Caller, action Action, args Args) (scope string, params map[string]string, err error)
    Handler   func(ctx context.Context, x Execution) (any, error)
    Store     *store.Store // when set: adapter opens tx, audit row in same tx

    // --- row-schema half (5/36 is authoritative) ---
    // RowType reflect.Type; Table string; PKFields []string; Scope ScopeFn;
    // Hooks Hooks; SkipApplyRebuild bool — the engine fields that drive
    // SQL CRUD, YAML round-trip, and OpenAPI emission. See
    // 36-yaml-manifests.md § "The row-schema half of resreg.Resource".
}

// Execution carries the surface-agnostic context the handler runs in.
// Tx is non-nil only for mutating actions on store-backed resources;
// forwarders and read-only paths see Tx == nil.
type Execution struct {
    Caller    Caller
    Action    Action
    Resource  string
    Args      Args
    TurnID    string  // X-Turn-Id header (REST) or _meta.turn_id (MCP)
    RequestID string  // X-Request-Id (REST) or _meta.request_id (MCP)
    SourceIP  string  // REST only
    Surface   string  // "rest" | "mcp"
    Tx        *sql.Tx
}

type Endpoint struct { Path, Verb string; Action Action; Status int }
type MCPTool  struct { Name, Description string; Action Action; Args []MCPArg }
type Action   string  // "list" | "get" | "create" | "update" | "delete"
```

`Authz` returns `(scope, params, err)`: the adapter calls
`auth.Authorize(Store, callerToAuth(c), "<Name>:<action>", scope, params)`
as the canonical ACL gate (see [`../4/9-acl-unified.md`](../4/9-acl-unified.md)).
Returning `err` short-circuits — validation failures map to 400 without
ever consulting auth. `Surface` belongs to the adapter invocation site,
not the caller; it lives on `Execution`, not `Caller`.

**Per-invocation caller resolution.** The MCP adapter takes a
`callerFor(ctx, req) (Caller, error)` resolver, invoked every call —
never captured at registration. Privilege confusion in shared MCP
servers is structurally precluded: each agent's request resolves to
its own principal at call time.

## Execution context

`Execution.Tx` is the contract between the adapter and the handler:

- **Mutating action + `Resource.Store` set.** Adapter calls
  `Store.DB().BeginTx(ctx, nil)`, threads the `*sql.Tx` into `Execution`,
  invokes the handler, then writes the audit row via
  `audit.EmitInTx(tx, ...)` _in the same tx_, then commits. On any
  handler error the tx rolls back and a non-tx audit row records the
  failure (slog + `audit.Emit`). On audit-insert failure the mutation
  rolls back — per spec contract, the audit row IS the mutation.
- **Read-only action.** Handler runs without a tx (`Tx == nil`); slog
  line emitted; no `audit_log` row (volume).
- **Forwarder (`Resource.Store == nil`).** Adapter never opens a tx;
  the handler is expected to call downstream over HTTP/IPC. The
  downstream daemon writes the audit row. Avoids double-logging.

Audit row insertion is the adapter's responsibility. Handlers don't
have to remember; they only run the mutation against `Execution.Tx`
when it's non-nil.

REST verbs and MCP tool names collapse to one `Action` per handler
branch. New action: one handler branch + one endpoint + one tool + one
policy row. New surface for an existing action: one row, no code.

**Action naming convention.** The Go constant is the short verb
(`Create`, `Update`, `Delete`, `List`, `Get`). The composed string
`<Resource.Name>.<Action>` (e.g. `groups.create`) is what surfaces
outside the codebase: OpenAPI `operationId`, MCP tool names where
they follow the default pattern, audit-log `action=` fields, metrics
labels, permission-editor rows. The short constant is for switches
and tables; the composed string is the operator-facing contract and
survives URL renames + handler-function renames.

### MCP tool-name partitioning

MCP tools accumulate. Without a partitioning rule the catalog grows
flat and per-turn enumeration cost grows linearly. Two rules:

1. **Prefixes derive from the source file the tool is registered in,
   not from invented marketing categories.** Source-of-truth files
   today are `ipc/ipc.go`, `ipc/inspect.go`, `ipc/connector.go` (per-
   channel registrations). The prefix is the file's domain, not a
   re-grouped abstraction. Examples:

   | Source file                | Current tools (no prefix today)                                                                                                                    | Prefix             |
   | -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------ |
   | `ipc/inspect.go`           | `inspect_routing`, `inspect_tasks`, `inspect_session`                                                                                              | already `inspect_` |
   | `ipc/connector.go` loop    | `send`, `reply`, `like`, `delete`, `post`, per-channel verbs                                                                                       | `chan.`            |
   | `ipc/ipc.go` routes block  | `list_routes`, `set_routes`, `add_route`, `delete_route`                                                                                           | `routes.`          |
   | `ipc/ipc.go` tasks block   | `schedule_task`, `list_tasks`                                                                                                                      | `tasks.`           |
   | `ipc/ipc.go` group block   | `register_group`, `escalate_group`, `delegate_group`                                                                                               | `groups.`          |
   | `ipc/ipc.go` tokens block  | `invite_create`, `issue_chat_link`, `issue_webhook`, `list_tokens`, `revoke_token`                                                                 | `tokens.`          |
   | `ipc/ipc.go` web block     | `set_web_host`, `get_web_host`, `set_web_route`, `del_web_route`, `list_web_routes`                                                                | `web.`             |
   | `ipc/ipc.go` session block | `fork_topic`, `engage`, `disengage`, `reset_session`, `inject_message`, `set_observe_window`, `set_group_open`, `observe_group`, `unobserve_group` | `session.`         |
   | `ipc/ipc.go` panes block   | `pane_set_prompts`, `pane_set_title`                                                                                                               | `pane.`            |
   | `ipc/ipc.go` cost block    | `log_external_cost`                                                                                                                                | `cost.`            |

   The prefix matches what the engine in `5/36` already produces for
   engine-managed resources (e.g. `routes.list`, `acl.create`).

2. **Freeze growth of MCP tools per source file.** When a new
   capability appears, the default home is a skill (per
   `../6/A-hierarchical-skills.md`), not a new MCP tool. New MCP
   tools land only when the capability is a stable primitive that
   every agent needs (file I/O, container ops, inspect\_\*). Domain
   workflows go into skills.

Backwards compatibility: existing flat names (`send`, `reply`,
`inspect_routing`, …) keep working as aliases for the prefixed names
for one release. After that, only the prefixed forms are documented.

Beyond freezing growth: most tools should not load eagerly at all.
Core messaging/inspect stay eager; connector + management tools defer
behind the Tool Search Tool. The eager/deferred split + cache
rationale lives in
[`../6/A-hierarchical-skills.md`](../6/A-hierarchical-skills.md)
§"Tools side: deferred disclosure".

## Token / auth model

Both surfaces produce a `Caller` consumed identically.

| Surface | Identity carrier                                                                                                    | Verifier                                  | Scope source                                          |
| ------- | ------------------------------------------------------------------------------------------------------------------- | ----------------------------------------- | ----------------------------------------------------- |
| REST    | `Authorization: Bearer <jwt>` (OAuth session per [`35-proxyd-standalone.md` "Login flow"](35-proxyd-standalone.md)) | [`auth.VerifyHTTP`](../../auth/README.md) | `user_groups` ACL + grants snapshot at authd issuance |
| MCP     | Capability token at agent socket bind ([`ipc/README.md` "Capability token"](../../ipc/README.md))                   | `auth.VerifyToken`                        | grants snapshot for `(folder)` at authd issuance      |

A platform token is an ES256 JWT signed by `authd` — the **sole signer**
([`1-auth-standalone.md`](1-auth-standalone.md)). Daemons verify offline
against `authd`'s public JWKs (`/v1/keys`); none hold the signing key.
Token body:

```json
{
  "sub": "user:abc123" | "agent:atlas/main" | "key:k_42",
  "scope": ["groups:read", "tasks:write", "messages:send", "groups:*", ...],
  "arz/folder": "atlas/main",
  "iat": 1735000000,
  "exp": 1735003600,
  "iss": "authd"
}
```

`arz/folder` is the namespaced folder claim per
[`1-auth-standalone.md`](1-auth-standalone.md); `auth/` treats it as
opaque and surfaces it via `Identity.Extra["folder"]` — it is never a
first-class `auth.Identity` field. It scopes the token to a subtree: an
`atlas/main` token can operate on `atlas/main/*` resources but not on
`rhias/*`. Root tokens omit it. There is no `tier` — authorization is
scope-match over `scope` ([`U-genericization.md`](U-genericization.md)
"Capability-vs-tier"); folder bounds the subtree, scopes bound the verbs.

Scopes are minted from grant rules at issuance time (snapshot), so
revoking grants requires token expiry or an explicit revocation list.
Short TTLs are the default; revocation lists deferred until needed.

**One signer, many triggers.** `authd` derives the scope set from grants
through one function and signs the token:

```go
// Inside authd — the only place that signs.
func (a *Authd) mintScopes(identity Identity, store GrantStore, narrow Narrow) []string
```

`Narrow` parameterizes the _trigger context_ (e.g. onbod invite passes an
invite-restricted subset; a dashd-created API key passes its declared
scope-list). One renderer, one signer; drift lives in `Narrow`, not in
the scope-from-grants logic.

### Issuance triggers (all mint through authd)

Other daemons do not sign; they call `authd /v1/tokens` with the trigger
context and receive a signed token, or delegate the user-facing flow to
`authd` directly.

| Trigger surface                 | Triggers on                         | Token shape                        | How                                              |
| ------------------------------- | ----------------------------------- | ---------------------------------- | ------------------------------------------------ |
| **proxyd**                      | OAuth login                         | user session, scopes from grants   | delegates login to `authd` (it mints)            |
| **runed** (the execution plane) | Agent container spawn / socket bind | agent capability, folder-scoped    | brokers a downscoped token from `authd` at spawn |
| **onbod**                       | Invite redemption / admission       | initial user session, narrow scope | requests token from `authd` with invite narrow   |
| **dashd**                       | API key creation (operator action)  | long-lived, narrow scope           | requests token from `authd` with key narrow      |

`runed` ([`P-runed.md`](P-runed.md)) hosts the per-tenant MCP socket and
brokers the agent token from `authd` at container spawn, embedding
`(folder, grants snapshot)`. The token is passed into the container as an
env var or via the MCP socket handshake. Agents use it for any HTTP call
to a sibling daemon's `/v1/*`. The former `mcpd` is folded into `runed` —
there is no separate MCP-host daemon.

`auth.Verify(token, jwks) → Identity{Sub, Scope, Extra}` lives in the
shared `auth/` library (folder surfaces as `Identity.Extra["folder"]`,
not a first-class field). Every daemon imports it and verifies offline.
No daemon implements its own verification, and no daemon signs.

Per-request auth at every `/v1/*` endpoint:

```go
ident, err := auth.VerifyHTTP(r, jwks)  // ES256 sig + exp + iss against cached JWKs
if !auth.HasScope(ident, "tasks", "write") { return 403 }      // honors "tasks:*", never "*:*"
if !identity.FolderContains(ident, taskFolder) { return 403 }  // arizuko helper over Identity.Extra["folder"]
// proceed
```

**Central signer, distributed verify.** `authd` is a new daemon and the
single source of mint, revocation, and audit; every other daemon is a
consumer. See [`1-auth-standalone.md`](1-auth-standalone.md) for the
crypto (ES256 + JWKs) and the standalone-first sequencing.

## Daemon ownership of `/v1/*`

Each daemon owns its tables and serves the matching API. No
cross-daemon reaches into another's storage.

| Daemon     | Owns                                                                                                                                                                       | Serves                                                                                                                                                                        |
| ---------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **routd**  | groups, routes, messages, sessions, channels, web_routes, route_tokens, grants, acl/user_groups, secrets, network_rules, cost_caps (residual config + conversation tables) | `/v1/groups`, `/v1/routes`, `/v1/messages`, `/v1/sessions`, `/v1/channels`, `/v1/web_routes`, `/v1/route_tokens`, `/v1/grants`, `/v1/acl`, `/v1/secrets`, `/v1/network_rules` |
| **runed**  | spawns, run history, mcp_tokens (hosts the agent MCP socket)                                                                                                               | `/v1/runs`; federates conversation-command tools back to routd's `/v1/turns/*`                                                                                                |
| **authd**  | signing keys, JWKs, sessions (sole signer)                                                                                                                                 | `/v1/tokens`, `/v1/keys`, `/auth/*` login                                                                                                                                     |
| **timed**  | scheduled_tasks, task_run_logs                                                                                                                                             | `/v1/tasks`                                                                                                                                                                   |
| **webd**   | vhosts (reads `web_routes` + `route_tokens` from routd; serves the `/chat`,`/hook` surfaces)                                                                               | `/v1/vhosts` (chat reads stay on existing `/api/*`; web-route CRUD via routd)                                                                                                 |
| **onbod**  | invites, admissions, auth_users, onboarding_gates                                                                                                                          | `/v1/invites`, `/v1/users`, `/v1/onboarding_gates`                                                                                                                            |
| **proxyd** | proxyd_routes (operator-composed enforcement point; delegates mint to authd)                                                                                               | `/auth/*` (existing); login delegates to authd, which mints                                                                                                                   |
| **dashd**  | nothing — aggregator UI calling the above                                                                                                                                  | HTML/HTMX over `/v1/*` of others                                                                                                                                              |

The first four rows are the products of the `gated` split
([`U-genericization.md`](U-genericization.md) Phase C, [`E-routd.md`](E-routd.md),
[`P-runed.md`](P-runed.md)): `authd` signs (extracted standalone first),
then `routd` (conversation engine + sole message appender) and `runed`
(execution plane: queue, container lifecycle, MCP socket — the former
`mcpd` folded in) carve out in one multi-DB cutover. This spec's
"single handler, two faces" contract is daemon-placement-agnostic: each
resource is owned by exactly one of these daemons and reached via both
its `/v1/*` REST face and the MCP face that `runed` federates.

`grants` lives with `routd` (it inherits gated's schema authority for the
conversation tables). If a future split puts grants elsewhere, the
issuance flow doesn't change — `authd` queries the grants owner at mint
time.

**proxyd routes** are not in the ownership table: they're declared
per-daemon in `template/services/<name>.toml` `[[proxyd_route]]`
blocks, aggregated by `compose/compose.go` at compose-generate time,
and consumed by proxyd via `PROXYD_ROUTES_JSON`. See
[`35-proxyd-standalone.md`](35-proxyd-standalone.md) "Per-daemon
route declarations". proxyd remains the verifier — it doesn't "own"
a routes table; it executes the operator-composed one.

### MCP federation

The MCP socket terminates in `runed` ([`P-runed.md`](P-runed.md)) — the
execution plane that hosts the per-tenant socket. `runed` owns no
resource tables, so every tool that touches a table is an **HTTP
forward** carrying the agent's capability token: conversation-command
tools (`reply`/`send`/routes/grants/…) forward to `routd`; tasks forward
to `timed`; invites to `onbod`:

```
agent → runed MCP socket: tools/call(pause_task, ...)
       → runed validates token scope (tasks:write)
       → runed HTTP-PATCH timed/v1/tasks/{id} {status: paused}
              with Authorization: Bearer <agent-token>
       → timed verifies token, checks scope, executes, returns
       → runed returns result to agent as JSON-RPC
```

Single MCP socket per agent. `runed` is a thin API gateway for the
agent — every owned operation is an HTTP forward to the daemon that owns
the table (`routd` for conversation/routing, `timed` for tasks, `onbod`
for invites). The forwarder shape is `Resource{Store: nil}` — the
adapter skips the tx/audit
dance and the destination daemon writes the audit row.
`webd/routes_mcp.go` is the canonical example.

### Dashboard becomes an aggregator

`dashd` holds an operator session token (minted by `authd` at login,
which proxyd delegates to) and makes `/v1/*` calls to routd, timed,
webd, onbod to render its pages. Adds write paths (forms posting to
`POST/PATCH/DELETE` of the relevant daemon) wherever today's UI is
read-only.

| dashd page        | Old (direct DB)                        | New (federated API)                                                                   |
| ----------------- | -------------------------------------- | ------------------------------------------------------------------------------------- |
| `/dash/groups/`   | reads `groups`, `routes`               | `routd/v1/groups`, `routd/v1/routes`                                                  |
| `/dash/tasks/`    | reads `scheduled_tasks`                | `timed/v1/tasks` (+ form → `POST timed/v1/tasks`)                                     |
| `/dash/activity/` | reads `messages` LIMIT 50              | `routd/v1/messages?limit=50&order=desc`                                               |
| `/dash/status/`   | reads `groups`, `sessions`, `channels` | `routd/v1/groups`, `routd/v1/sessions`, `routd/v1/channels`                           |
| `/dash/memory/`   | direct fs read/write                   | new resource on whichever daemon owns the group fs (likely routd): `routd/v1/files/*` |
| `/dash/profile/`  | reads `auth_users`                     | `onbod/v1/users/{sub}`                                                                |

dashd never touches tables directly after this refactor.

## Scope vocabulary

`<resource>:<verb>[:own_group]`. Builds on the `<resource>:<verb>`
shape with one addition — the `:own_group` suffix.

- `<resource>:read` / `<resource>:write` — admin.
- `<resource>:read:own_group` / `<resource>:write:own_group` — scoped
  to the caller's folder subtree read from `Identity.Extra["folder"]`
  (the arizuko `identity.go` helper; `auth/` is folder-agnostic per
  [`1-auth-standalone.md`](1-auth-standalone.md)).
- `<resource>:*` — all verbs on a resource (operator shortcut, useful
  when verb count grows). No `*:*` global wildcard — operators carry
  the enumerated list (≤20 strings at current resource count). Two
  matching paths hurt audit reasoning more than one short list does.

The scope vocabulary is the operator-token shorthand; the
authoritative gate is `auth.Authorize` over the unified ACL — see
[`../4/9-acl-unified.md`](../4/9-acl-unified.md). resreg's `Authz`
callback derives `(scope, params)` per-action; the adapter then
delegates to `auth.Authorize`, which uses `auth.HasScope` (honors
`ns:*`, never `*:*` — [`1-auth-standalone.md`](1-auth-standalone.md))
as its scope-match primitive. There is no _second_ authorization path
competing with the ACL gate; the ACL row table is the single source of
truth.

## Per-resource access matrix

Backing-table owners are the post-split daemons (see "Daemon ownership of
`/v1/*`" above and "Resource ownership across daemons" below); the residual
config tables (`grants`, `secrets`, `acl`/`user_groups`, `network_rules`)
land in `routd`, which inherits gated's schema authority.

| Resource            | `read`   | `write`  | `read:own_group` | `write:own_group`                                                 | Backing tables                                                    |
| ------------------- | -------- | -------- | ---------------- | ----------------------------------------------------------------- | ----------------------------------------------------------------- |
| `grants`            | operator | operator | agent + user     | agent + user                                                      | `grants` (routd)                                                  |
| `routes`            | operator | operator | —                | —                                                                 | `routes` (routd)                                                  |
| `secrets`           | operator | operator | —                | user (`/dash/me/secrets`, [`specs/6/Y`](../6/Y-secret-broker.md)) | `secrets` (routd)                                                 |
| `scheduled_tasks`   | operator | operator | agent + user     | agent + user                                                      | `scheduled_tasks` (timed)                                         |
| `chats`             | operator | operator | agent + user     | — (operator-only)                                                 | `messages` (routd)                                                |
| `group_folders`     | operator | operator | —                | —                                                                 | `groups` (routd)                                                  |
| `egress_allowlist`  | operator | operator | —                | agent                                                             | crackbox register ([`specs/11/10`](../11/10-crackbox-arizuko.md)) |
| `user_groups` (ACL) | operator | operator | —                | —                                                                 | `user_groups` (routd)                                             |
| `invites`           | operator | operator | agent w/ scope   | agent w/ scope (`invites:write:own_group`)                        | `invites` (onbod)                                                 |

Rationale: `routes`/`group_folders`/`user_groups` are operator-only on
both axes — the agent can't reach into its own ACL or topology; that's
the trust boundary. `secrets:write:own_group` is user-via-dashboard
only; the agent never reads or rotates secrets (invariant in
[`specs/6/Y`](../6/Y-secret-broker.md); the broker resolves
folder/user secrets inside the tool handler on the host, the container
never sees them). `egress_allowlist:write:own_group` lets the
agent add a host to its own allowlist (today's crackbox register
pattern). `chats:write:own_group` is blank — agent uses send/reply/post
verbs, not direct `chats` writes (those are a separate resource family,
not in this matrix).

## Single source of truth

Registration mirrors [`auth.Mount`](../../auth/README.md)'s pattern:

```go
func RegisterResource(r Resource, mux *http.ServeMux, mcp *server.MCPServer) {
    for _, e := range r.Endpoints {
        mux.Handle(e.Verb+" "+e.Path, restAdapter(r, e))
    }
    for _, t := range r.MCPTools {
        mcp.RegisterTool(mcpAdapter(r, t))
    }
}
```

Adapters are short shims that decode args + build `Caller` from the
verified token, then call `r.Handler`. Each daemon's `main.go` calls
`RegisterResource` per owned resource. Routing per daemon is proxyd's
job ([`35-proxyd-standalone.md` "Per-daemon route declarations"](35-proxyd-standalone.md)).

OpenAPI emerges from the same registry (engine-generated, no codegen):
walking `r.Endpoints` produces the spec; MCP `tools/list` walks
`r.MCPTools`.
Drift is structurally impossible.

## Audit + observability

Every adapter logs one row per request with a fixed shape:

```
caller=<sub> resource=<name> action=<verb>
surface=rest|mcp|cli target=<folder> result=<allowed|denied|error>
```

`action` is the cross-surface stable correlator. A single
`grep 'resource=groups action=create'` returns work regardless of
whether it arrived via `POST /v1/groups`, MCP `groups.create`, or
`arizuko group add`. URL renames and handler-function renames don't
break audit because the Action constant is the contract; the surfaces
are derivations.

Metrics labels follow the same convention:
`requests_total{resource="groups",action="create",surface="mcp",result="allowed"}`.
The permission editor in dashd iterates `(Resource × Action)` pairs to
present every operation any caller can perform — without walking
handler functions or grepping URLs. "What can scope X do?" is one
loop:

```go
for _, r := range registry {
    for _, action := range r.Actions() {       // (Resource × Action) pairs
        if auth.Authorize(store, caller, r.Name+":"+action, ...) == nil { /* show row */ }
    }
}
```

The check is `auth.Authorize` over the unified ACL — the same gate the
adapters call, not a parallel `r.Policy` predicate table (the `Resource`
type carries `Authz`, not a `Policy` map).

This is the operational reason `Action` exists as a separate type
rather than implicit in handler-function identity — handler names
churn, URL paths churn, the enum is the stable axis.

## OAuth single-login

For the human caller, REST is the only relevant surface. One OAuth
flow produces one session JWT. No second login for "the MCP side";
MCP is the agent's surface, the agent is a different principal
(`sub: "agent:<folder>"`, not `user:<sub>`).

## Inventory — today's writes

Every store write below is a candidate for `resreg` exposure. Columns:
**Today** = where it's invoked from; **MCP** = is there an existing
MCP tool; **REST** = is there an existing endpoint.

| Operation                           | Store call                                                     | Today                           | MCP                                                          | REST          |
| ----------------------------------- | -------------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------ | ------------- |
| Group create                        | `PutGroup` (`store/groups.go:20`)                              | onbod/SetupGroup, CLI           | `register_group`                                             | —             |
| Group delete                        | `DeleteGroup` (`store/groups.go:47`)                           | CLI                             | —                                                            | —             |
| Route add / set / delete            | `AddRoute`/`SetRoutes`/`DeleteRoute` (`store/routes.go`)       | CLI, agent MCP, dashd           | `add_route`/`set_routes`/`delete_route` (`ipc/ipc.go:1252`+) | —             |
| User grant / ungrant                | `Grant`/`Ungrant` (`store/auth.go:175`)                        | CLI (`arizuko grant`)           | —                                                            | —             |
| Action grants (folder rule overlay) | `SetGrants` (`store/grants.go:17`)                             | agent MCP                       | `set_grants`                                                 | —             |
| Secret put / delete                 | `SetSecret`/`DeleteSecret` (`store/secrets.go:50`)             | dashd (`/dash/me/secrets`), CLI | —                                                            | dashd-private |
| Invite create / revoke              | `CreateInvite`/`RevokeInvite` (`store/invites.go`)             | CLI, onbod                      | —                                                            | onbod         |
| Identity create / link / unlink     | `CreateIdentity`/`LinkSub`/`UnlinkSub` (`store/identities.go`) | CLI                             | —                                                            | —             |
| Onboarding gates                    | `PutGate`/`DeleteGate`/`EnableGate` (`store/onboarding.go`)    | CLI                             | —                                                            | —             |
| Egress allowlist                    | `AddNetworkRule`/`RemoveNetworkRule` (`store/network.go`)      | crackbox register, CLI          | partial (register)                                           | —             |
| Web routes                          | `SetWebRoute`/`DelWebRoute` (`store/web_routes.go`)            | agent MCP                       | `set_web_route`/`del_web_route` (`ipc/ipc.go:1786`+)         | —             |
| Scheduled tasks                     | `schedule_task` family                                         | agent MCP                       | `schedule_task`+                                             | —             |
| Cost caps                           | `SetFolderCap`/`SetUserCap` (`store/cost_log.go:74`)           | CLI                             | —                                                            | —             |
| ACL rows (per 4/9)                  | (`acl` table writes)                                           | n/a (new)                       | —                                                            | —             |

Columns with `—` are the gap. Most operator concepts are either
CLI-only with direct store calls (`cmd/arizuko/*.go`) or MCP-only with
no REST sibling. The shape is bimodal; the principle above is to make
it uniform.

### Resource declarations to add

For each row above without a `resreg.Resource`, the declaration shape
is a small struct literal. Catalog of new resources:

Owning daemon is the post-split owner; `routd` holds the residual config +
conversation tables that were gated's.

| Resource          | Actions                                                                         | Owning daemon                                     | Scope predicates                                                |
| ----------------- | ------------------------------------------------------------------------------- | ------------------------------------------------- | --------------------------------------------------------------- |
| `groups`          | list/get/create/update/delete                                                   | routd                                             | `admin` at scope ⊇ folder; `*` operator                         |
| `acl`             | list/get/create/delete                                                          | routd                                             | `admin` at scope ⊇ row.scope; `*` operator                      |
| `secrets`         | list/get/create/delete (no read of value via MCP — agent broker rule preserved) | routd                                             | folder-`admin` at scope, plus user-owned writes via dashd OAuth |
| `invites`         | list/get/create/revoke                                                          | onbod                                             | `admin` at scope ⊇ targetGlob                                   |
| `identities`      | list/get/create/link/unlink                                                     | onbod                                             | self for own sub; `*` for cross-user link                       |
| `gates`           | list/get/put/delete/enable                                                      | onbod                                             | `*` operator                                                    |
| `network_rules`   | list/get/create/delete                                                          | routd                                             | folder-`admin` at scope                                         |
| `cost_caps`       | list/get/set                                                                    | routd                                             | `*` operator; self-read for own user                            |
| `scheduled_tasks` | (already partial — finish symmetry)                                             | timed                                             | folder-`admin` at scope                                         |
| `web_routes`      | (already MCP — add REST mirror)                                                 | routd (served via webd read; see ownership table) | folder-`admin` at scope                                         |

New action = one struct literal addition + one handler function. The
handler is the only behavior; everything else is registration. Authz
delegates to `auth.Authorize`; for store-backed resources the adapter
threads a `*sql.Tx` in `Execution` so the mutation + audit row commit
as a unit.

### CLI evolution — `cmd/arizuko/*.go`

Today: `arizuko grant`, `arizuko invite`, `arizuko group add`, etc.
call `store.*` directly. The CLI binary opens `messages.db` and
writes rows. Bypasses every authorization concern and audit trail.

Target: each command becomes a thin client of the local MCP socket
(`/srv/data/arizuko_<inst>/ipc/root/socket`). The socket already
exists for `arizuko chat`. **Lean: unix-socket-as-capability** —
the socket is unix-domain, owned by the operator UID; presence on
the socket proves operator capability. Implies an ACL row
`(folder:operator_cli, '*', '**')` seeded at `arizuko create`. The
OAuth path remains available for remote CLI use later (call `/v1/*`
over HTTPS instead of MCP over the local socket).

### dashd evolution

Today: dashd is the operator web UI. Read paths query the shared DB
directly; the few write paths (`/dash/me/secrets`) call
`store.SetSecret` directly.

Target: dashd is a pure aggregator — it owns no DB. After the `gated`
split the tables live in `routd`/`runed`/`timed`/`onbod`, each behind
its own DB, so dashd has nothing to read directly. Both its reads and
its mutating handlers go through the owning daemon's `/v1/*`: reads via
`GET /v1/<resource>`, writes via the `resreg` POST/PATCH/DELETE
endpoints. dashd is an internal consumer of the same surface external
callers use.

## Anti-patterns — what should NOT go via MCP

Some operations look like state changes but should not be exposed as
MCP tools. Each has the same shape: hot path, high-volume internal
event, or stream rather than CRUD verb.

- **Inbound message ingestion.** The gateway poll loop
  (`gateway/gateway.go:502+`) writes `messages` rows per inbound.
  Per-message hot-path; pushing it through `resreg` would force a
  capability check and audit log per inbound. Not in scope. The agent
  can `inject_message` for synthetic sends — that IS an MCP tool
  (audited, low-volume).
- **Cost-log writes** (`store/cost_log.go:20`). Every Claude API call
  emits a row. Per-call, not per-operator-action. Stays as a direct
  store write from `gateway` and `timed`.
- **Agent cursor advancement.** Internal bookkeeping, not user-facing.
- **Streaming surfaces.** Slink message stream, agent live output —
  not CRUD/RPC. SSE / WebSocket sits next to `resreg`, not inside it.
- **Auth session creation** (`store/auth.go:119`). The session is
  minted by `authd` (the sole signer), verified by the `auth/` library.
  Substrate every other tool consumes, not a user-tool itself.
- **Migrations.** Schema changes are file-driven (`store/migrations/`)
  and run by `gated` at startup. Not a resource.

The rule: if it's user-initiated, audit-worthy, and fits an
allow/deny answer, it belongs in `resreg`. If it's a high-rate
side effect of normal operation, it does not.

## Auth shape for management operations

Under unified ACL (`specs/4/9-acl-unified.md`):

- **Operator human** — `(google:114operator, '*', '**')`. One row.
  All resources, all actions, everywhere.
- **Folder admin** — `(google:114alice, 'admin', 'atlas/**')`. Can
  manage routes/grants/secrets under `atlas/`, no further.
- **Operator agent** — `(folder:atlas, 'admin', 'atlas/**')`. The
  agent at the world root can administer its own subtree (delegate
  routes, set child grants). Same authority shape as the human
  folder admin, different principal namespace.
- **Leaf agent** — no `admin` rows; only `mcp:<tool>` rows derived
  from its capability-scope defaults. Same as today.

`auth.Authorize` is the only check. resreg's per-resource `Authz`
callback derives `(scope, params)` from the call and delegates —
there is no parallel predicate machinery. The
`<resource>:<verb>[:own_group]` shorthand is the operator-token-minting
affordance over the same ACL rows.

## Resource ownership across daemons

`groups` is routd's; `invites` is onbod's; `web_routes` is routd's too
(webd reads it through routd's `/v1/web_routes` and serves the `/chat`,
`/hook` surfaces, per [`E-routd.md`](E-routd.md)).
Each registers its own resources. The MCP socket terminates in `runed`
(which owns no resource tables), so MCP calls to `invites.*` forward to
onbod over HTTP, `groups.*` to routd, and so on. Pattern (shipped
2026-05-25): the forwarder is a `Resource{Store: nil}` whose `Handler`
does an HTTP call downstream; the adapter skips the tx/audit dance, and
the destination daemon writes the audit row.
`webd/routes_mcp.go` is the canonical example.

## Phased rollout

| Phase | Deliverable                                                                                                                                                                                                                     |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A     | `Caller`, `Resource`, `Endpoint`, `MCPTool`, `ScopePred` types in `auth/` (alongside `auth.VerifyHTTP` against authd's JWKs per [`1-auth-standalone.md`](1-auth-standalone.md)). `RegisterResource` helper. No behavior change. |
| B     | High-priority resources via `resreg`: `acl`, `groups`, `secrets`, `invites`. Operator-facing core; the agent-facing tools already exist.                                                                                        |
| C     | Backfill missing REST mirrors for existing agent MCP tools (`set_grants`, `register_group`, `add_route`, `set_web_route`). One PR per resource.                                                                                 |
| D     | Migrate `routes`, `scheduled_tasks` one at a time; each migration deletes the hand-written tool in `ipc/ipc.go` and the direct DB call.                                                                                         |
| E     | Cutover `cmd/arizuko/*.go` to call the local MCP socket. Deletes direct `store.*` calls from `cmd/`. CLI becomes a thin client.                                                                                                 |
| F     | Cutover dashd write handlers. `/dash/me/secrets` becomes a `secrets.create` dispatch. Drops the dashd-private REST path.                                                                                                        |
| G     | Deprecate hand-written tools in `ipc/ipc.go` that have a registry equivalent; delete after one release.                                                                                                                         |

Each phase is independent. Stopping at C still leaves the system in
a saner state (REST parity); E+F are the structural wins.

## Coverage matrix

Resources to verify uniform coverage on (refine during implementation;
the live state is in `resreg/resources/*.go` after the 5/36 engine
landed):

| Resource           | MCP tool                                           | REST endpoint          | Engine-managed? |
| ------------------ | -------------------------------------------------- | ---------------------- | --------------- |
| `groups`           | `groups.list`, `groups.get`, ...                   | `/v1/groups`           | yes (5/36)      |
| `acl`              | `acl.list`, `acl.add`, ...                         | `/v1/acl`              | yes (5/36)      |
| `acl_membership`   | `acl_membership.add`, ...                          | `/v1/acl_membership`   | yes (5/36)      |
| `routes`           | `routes.list`, `routes.add`, ...                   | `/v1/routes`           | yes (5/36)      |
| `web_routes`       | `web_routes.set`, ...                              | `/v1/web_routes`       | yes (5/36)      |
| `scheduled_tasks`  | `tasks.create`, `tasks.list`, ...                  | `/v1/scheduled_tasks`  | yes (5/36)      |
| `secrets`          | `secrets.list`, `secrets.add`, ...                 | `/v1/secrets`          | yes (5/36)      |
| `network_rules`    | `network_rules.add`, ...                           | `/v1/network_rules`    | yes (5/36)      |
| `proxyd_routes`    | `proxyd_routes.list`, ...                          | `/v1/proxyd_routes`    | yes (5/36)      |
| `onboarding_gates` | `gates.list`, `gates.enable`, ...                  | `/v1/onboarding_gates` | yes (5/36)      |
| `invites`          | `invites.create`, `invites.list`, `invites.revoke` | `/v1/invites`          | imperative      |
| `route_tokens`     | `tokens.create`, `tokens.list`, `tokens.revoke`    | `/v1/route_tokens`     | imperative      |
| `chats`            | `chats.list`, `chats.get`, ...                     | `/v1/chats`            | runtime query   |
| `messages`         | per [`C-message-mcp.md`](C-message-mcp.md)         | `/v1/messages`         | runtime query   |

Engine-managed resources get both surfaces automatically per
[`36-yaml-manifests.md`](36-yaml-manifests.md): one `resreg.Resource`

- `RowType` registration produces SQL CRUD, REST handler, MCP tool,
  and YAML round-trip. Imperative resources (tokens) follow the same
  handler shape but are not full-rebuild candidates. Runtime queries
  (`chats`, `messages`, `audit_log`) need bespoke filter/pagination
  DSL; see [`C-message-mcp.md`](C-message-mcp.md).

## Implementation pattern (one resource at a time)

```go
// handler — pure logic, no transport
type ChatsService struct { store Store; clock Clock }

func (s *ChatsService) List(ctx context.Context, actor Actor, q ChatsQuery) ([]Chat, error) {
    if err := acl.Authorize(actor, "chats:list", q.Scope); err != nil { return nil, err }
    if err := validateChatsQuery(q); err != nil { return nil, err }
    chats, err := s.store.ListChats(ctx, q)
    if err != nil { return nil, err }
    audit.Emit(ctx, "chats.list", actor, q, len(chats))
    return chats, nil
}

// MCP wrapper — auth from the capability token (auth.VerifyToken), JSON-RPC marshalling
func (h *MCPHandler) handleListChats(ctx context.Context, req mcp.Req) mcp.Resp { ... }

// REST wrapper — auth from Bearer JWT (auth.VerifyHTTP against authd JWKs), HTTP marshalling
func (h *RESTHandler) handleGetChats(w http.ResponseWriter, r *http.Request) { ... }
```

Both wrappers are thin. The handler is the only place business
logic lives. For engine-managed resources, this skeleton is
generated by the engine; bespoke handlers (runtime queries,
streaming) follow it by hand.

## Per-resource integration testing

Per-resource integration test that exercises both wrappers against
the same handler instance. Asserts:

1. Same input → same output across MCP and REST.
2. Same auth failure → same error code (translated per protocol).
3. Same validation failure → same error code.
4. Same audit event emitted (one row in `audit_log`, NOT two).

### Audit contract

Every state-changing handler MUST write exactly one `audit_log` row in
the SAME database transaction as the resource mutation; if the audit
row write fails, the mutation MUST roll back. Read-only handlers emit
slog telemetry only — no audit row. Field schema:
[`I-tool-call-logging.md`](I-tool-call-logging.md).

## Acceptance

- A new `Resource{}` value added to the registry is automatically
  reachable via both `POST /v1/<resource>` AND `<resource>.create` MCP
  tool — no code beyond the struct literal.
- Auth tests:
  - Agent token with `grants:write:own_group` and
    `Identity.Extra["folder"] = "atlas/support"` can `PATCH /v1/grants/{id}`
    for a grant under `atlas/support/*` AND call `grants.update` over
    MCP with the same id; both 200.
  - Same token cannot update a grant under `rhias/*`; both 403.
  - Operator token with `grants:write` can do both via REST; same
    handler, different `Caller.Scope`.
- The handler under test has zero `if surface == ...` branches
  (grepped in CI).
- `4-openapi-discoverable.md`'s generated spec lists every endpoint the
  registry knows about; MCP `tools/list` lists every tool; both sets
  agree on (resource × action).

## What this spec is not

- Not streaming. SSE endpoints (slink message stream, agent live
  output) stay as-is — not CRUD/RPC.
- Not rate limits ([`specs/10/4-rate-limits.md`](../10/4-rate-limits.md)).
- Not transport addition. REST + MCP only. No GraphQL, no gRPC.
- Not per-tenant policy variants. One global policy table per resource.
- Not a permission-model overhaul. Grants (per
  [`GRANTS.md`](../../GRANTS.md)) keep producing the scope snapshot;
  this spec declares how the snapshot is consumed.

## Reconciliations

- **vs [`specs/3/5-tool-authorization.md`](../3/5-tool-authorization.md)**:
  that spec's per-action matrix becomes the scope minter — at issuance,
  `folder` + grant rules → set of `<resource>:<verb>[:own_group]`
  scopes. The matrix authors which scopes `authd` mints; this spec
  authors how scopes are checked. No `tier` participates
  ([`U-genericization.md`](U-genericization.md) "Capability-vs-tier").
- **vs [`auth/policy.go`](../../auth/policy.go)** today: hand-maintained
  9-case per-tool switch. After Phase G it is **deleted** — not thinned.
  The per-resource `Authz` callback delegating to `auth.Authorize` over
  the unified ACL is the only authorization site. The switch survives in
  CHANGELOG only.

## Open (parked)

- **`:own_group` matching under nested folders.** Subtree containment
  is the lean. Pin when the arizuko `identity.go` folder helper (the
  folder-match logic moved out of `auth/` per
  [`1-auth-standalone.md`](1-auth-standalone.md)) lands.
- **Bulk endpoints.** Many POSTs, bulk only on demand.
- **Action verbs vs CRUD shape.** `messages.send` / `groups.escalate`
  are action-shaped. Inherit Google's `:verb` convention.
- **`ScopePred` concrete shapes** land per-resource in Phase B.
- **Token TTL & revocation.** Short TTL (1h) is the default;
  revocation list deferred. Long-lived API keys (dashd-issued) need a
  revocation table — when, where?
- **Cross-daemon transactions.** Some operations span tables (e.g.
  "create group → seed skills → register routes"). Once ownership
  is split, these become saga-shaped. Acceptable since each step is
  idempotent; flag if not.
- **MCP socket lifecycle vs token TTL.** Container can outlive its
  token if a turn runs longer than TTL. Refresh via the MCP host?
  Accept the rare expiry? Deferred — short-lived turns mostly avoid
  this.
- **dashd hosting decision.** Keep dashd as Go HTML server, or fold
  HTML rendering into webd? Lean: keep.
- **Token TTL refresh against authd.** `authd` is the decided central
  signer ([`1-auth-standalone.md`](1-auth-standalone.md)), extracted
  standalone first. Open here is only the refresh ergonomics —
  centralized revocation list + audit live in `authd`; the question is
  whether long turns re-fetch a token from `authd` mid-run or accept
  rare expiry.
- **Streaming endpoints.** SSE on REST and MCP notifications on the
  agent side. Today they diverge. Defer; not CRUD-shaped.
- **Pagination shape.** MCP tools return arrays today; REST uses
  cursor pagination on some routes. Harmonize or leave per-resource.

## Code pointers

- `auth/` ([`README.md`](../../auth/README.md)) — gains `Caller`,
  `Resource`, `Endpoint`, `MCPTool`, `ScopePred`, `RegisterResource`,
  `VerifyHTTP`, `FetchKeys`, `HasScope` per Phase A. `auth/` stays
  folder-agnostic; the folder-match helper lives in arizuko's
  `identity.go` over `Identity.Extra["folder"]`
  ([`1-auth-standalone.md`](1-auth-standalone.md)). The verify-side
  single source of truth for token format; minting lives in `authd`.
- [`auth/policy.go:14-96`](../../auth/policy.go) — current `Authorize`
  switch; Phase G replaces with registry lookup.
- [`ipc/ipc.go:32-120`](../../ipc/ipc.go) — `GatedFns`/`StoreFns` plus
  per-tool registrations; resource-action tools migrate to
  `RegisterResource`, shrinking the file. After the split this MCP host
  is `runed` ([`P-runed.md`](P-runed.md)), which gains an authd
  token-brokering client (not a local minter) and an HTTP-forward client
  for cross-daemon tools (conversation tools → routd).
- [`proxyd/main.go:590-634`](../../proxyd/main.go) — signed-identity
  header path; the `Caller` builder for REST. OAuth login delegates to
  `authd`, which mints the scope-carrying token; proxyd verifies.
- `routd`/`runed` (post-split, from today's [`gated/`](../../gated/)),
  [`timed/`](../../timed/), [`onbod/`](../../onbod/),
  [`webd/`](../../webd/) — each gains a small `v1.go` for its owned
  resources; calls `RegisterResource` per owned resource.
- [`dashd/`](../../dashd/) — replaces direct `store.*` calls with
  `/v1/*` HTTP calls using the operator's session token.
- `core/grants.go` — stays as the rule evaluator; called by `authd`
  at mint time to compute scopes from rules.
