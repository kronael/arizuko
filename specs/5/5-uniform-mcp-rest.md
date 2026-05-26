---
status: draft
depends: [V-platform-api, 1-auth-standalone, 35-proxyd-standalone]
---

# Uniform REST + MCP per resource

> **Canonical principle.** Closed across all resources in
> [`../7/1-mcp-rest-unification.md`](../7/1-mcp-rest-unification.md) —
> the phase 7 spec carries the coverage matrix, per-resource handler
> pattern, audit contract, and acceptance criteria.

**Every operator action accessible via both REST (outside, OAuth-gated)
AND MCP (inside, tier-gated), wrapped over a single handler.** One
resource, one handler, two faces. Auth is the only thing that differs.

Sibling principle to [R-platform-api.md](R-platform-api.md): that spec
defines the federated `/v1/*` surface and token model; this spec makes
explicit that the MCP tool surface is the same registry viewed through
a different auth lens — never a second hand-written tree.

## Why

Today's surface is split asymmetrically:

- `dashd` reads `groups`, `routes`, `messages` direct from the shared
  DB ([`R-platform-api.md` "Dashboard"](R-platform-api.md)); no write
  paths exist.
- `ipc/ipc.go` registers the action tools the agent can call
  ([`ipc/README.md` "Tool surface"](../../ipc/README.md)), each a
  hand-written wrapper over `gated`/`store` calls.
- Authorization lives in [`auth/policy.go:14-96`](../../auth/policy.go),
  a hand-maintained switch over tool names; tiers are gates, not scopes.
  proxyd injects identity headers via
  [`proxyd/main.go:590-604`](../../proxyd/main.go); the agent never
  carries a token.

Agents and operators see different sets of operations. The choice of
which surface gets a feature is accidental, not principled.

## The principle

1. **One handler per resource action.** No duplicated logic between
   `ipc/ipc.go` and any `/v1/*` HTTP handler.
2. **Two faces, declared next to the resource.** A `Resource` declares
   its REST endpoints AND its MCP tools; one registration wires both.
3. **`Caller` is surface-agnostic.** Builds on
   [`auth.Identity{Sub, Scope, Folder, Tier}`](../../auth/README.md);
   handlers read `Caller`, not `*http.Request` or `mcp.ToolRequest`.
4. **Policy is declarative.** A `ScopePred` per action lives next to
   the resource; handler dispatches by action, policy is checked first.

## Caller and Resource shape

```go
type Caller struct {
    Sub    string
    Name   string
    Folder string
    Tier   int
    Claims map[string]string  // JWT claims for ACL row predicates
}

type Resource struct {
    Name      string
    Endpoints []Endpoint   // REST faces
    MCPTools  []MCPTool    // MCP faces
    Authz     func(c Caller, action Action, args Args) (scope string, params map[string]string, err error)
    Handler   func(ctx context.Context, x Execution) (any, error)
    Store     *store.Store // when set: adapter opens tx, audit row in same tx
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

## Token / auth model

Both surfaces produce a `Caller` consumed identically.

| Surface | Identity carrier                                                                                                  | Verifier                                  | Scope source                                                                      |
| ------- | ----------------------------------------------------------------------------------------------------------------- | ----------------------------------------- | --------------------------------------------------------------------------------- |
| REST    | `Authorization: Bearer <jwt>` (OAuth session per [`2-proxyd-standalone.md` "Login flow"](2-proxyd-standalone.md)) | [`auth.VerifyHTTP`](../../auth/README.md) | `user_groups` ACL + grants at proxyd login                                        |
| MCP     | Capability token at agent socket bind ([`ipc/README.md` "Capability token"](../../ipc/README.md))                 | `auth.VerifyToken`                        | Folder tier per [`specs/3/5-tool-authorization.md`](../3/5-tool-authorization.md) |

Both carry the same JWT shape (HS256, `AUTH_SECRET`,
[`R-platform-api.md` "Token model"](R-platform-api.md)). Mint sites
differ; verify path is one function.

## Scope vocabulary

`<resource>:<verb>[:own_group]`. The `:own_group` suffix is the only
addition to
[`R-platform-api.md`'s `<resource>:<verb>` shape](R-platform-api.md);
[its "Open" wildcard lean](R-platform-api.md) stays namespace-only.

- `<resource>:read` / `<resource>:write` — admin.
- `<resource>:read:own_group` / `<resource>:write:own_group` — scoped
  to caller's `Identity.Folder` subtree (matches today's
  [`auth.MatchesFolder`](../../auth/README.md) planned check).
- `<resource>:*` — all verbs on a resource (operator shortcut, useful
  when verb count grows). No `*:*` global wildcard — operators carry
  the enumerated list (≤20 strings at current resource count). Two
  matching paths hurt audit reasoning more than one short list does.

The scope vocabulary is the operator-token shorthand; the
authoritative gate is `auth.Authorize` over the unified ACL — see
[`../4/9-acl-unified.md`](../4/9-acl-unified.md). resreg's `Authz`
callback derives `(scope, params)` per-action; the adapter then
delegates to `auth.Authorize`. No parallel `HasScope`-style predicate
machinery; the ACL row table is the single source of truth.

## Per-resource access matrix

| Resource            | `read`   | `write`  | `read:own_group` | `write:own_group`                                                        | Backing tables                                                    |
| ------------------- | -------- | -------- | ---------------- | ------------------------------------------------------------------------ | ----------------------------------------------------------------- |
| `grants`            | operator | operator | agent + user     | agent + user                                                             | `grants` (gated)                                                  |
| `routes`            | operator | operator | —                | —                                                                        | `routes` (gated)                                                  |
| `secrets`           | operator | operator | —                | user (`/dash/me/secrets`, [`specs/11/11`](../11/11-crackbox-secrets.md)) | `secrets` (gated)                                                 |
| `scheduled_tasks`   | operator | operator | agent + user     | agent + user                                                             | `scheduled_tasks` (timed)                                         |
| `chats`             | operator | operator | agent + user     | — (operator-only)                                                        | `messages` (gated)                                                |
| `group_folders`     | operator | operator | —                | —                                                                        | `groups` (gated)                                                  |
| `egress_allowlist`  | operator | operator | —                | agent                                                                    | crackbox register ([`specs/11/10`](../11/10-crackbox-arizuko.md)) |
| `user_groups` (ACL) | operator | operator | —                | —                                                                        | `user_groups` (gated)                                             |
| `invites`           | operator | operator | agent (tier ≤ 1) | agent (tier ≤ 1)                                                         | `invites` (onbod)                                                 |

Rationale: `routes`/`group_folders`/`user_groups` are operator-only on
both axes — the agent can't reach into its own ACL or topology; that's
the trust boundary. `secrets:write:own_group` is user-via-dashboard
only; the agent never reads or rotates secrets (invariant in
[`specs/11/11`](../11/11-crackbox-secrets.md); the broker resolves
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
job ([`2-proxyd-standalone.md` "Per-daemon route declarations"](2-proxyd-standalone.md)).

OpenAPI emerges from the same registry
([`4-openapi-discoverable.md`](4-openapi-discoverable.md)): walking
`r.Endpoints` produces the spec; MCP `tools/list` walks `r.MCPTools`.
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
    for action, pred := range r.Policy {
        if pred.Allows(callerScope) { /* show row */ }
    }
}
```

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

| Resource          | Actions                                                                         | Owning daemon | Scope predicates                                                |
| ----------------- | ------------------------------------------------------------------------------- | ------------- | --------------------------------------------------------------- |
| `groups`          | list/get/create/update/delete                                                   | gated         | `admin` at scope ⊇ folder; `*` operator                         |
| `acl`             | list/get/create/delete                                                          | gated         | `admin` at scope ⊇ row.scope; `*` operator                      |
| `secrets`         | list/get/create/delete (no read of value via MCP — agent broker rule preserved) | gated         | folder-`admin` at scope, plus user-owned writes via dashd OAuth |
| `invites`         | list/get/create/revoke                                                          | onbod         | `admin` at scope ⊇ targetGlob                                   |
| `identities`      | list/get/create/link/unlink                                                     | gated         | self for own sub; `*` for cross-user link                       |
| `gates`           | list/get/put/delete/enable                                                      | onbod         | `*` operator                                                    |
| `network_rules`   | list/get/create/delete                                                          | gated         | folder-`admin` at scope                                         |
| `cost_caps`       | list/get/set                                                                    | gated         | `*` operator; self-read for own user                            |
| `scheduled_tasks` | (already partial — finish symmetry)                                             | timed         | folder-`admin` at scope                                         |
| `web_routes`      | (already MCP — add REST mirror)                                                 | webd          | folder-`admin` at scope                                         |

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

Target: dashd's mutating handlers are thin shims over `resreg`
endpoints. Reads stay direct queries to the DB (cheap, read-only,
no audit need) or migrate to `GET /v1/<resource>` symmetrically.
Writes via registry; reads direct for dashd's own UI. The `/v1/*`
REST surface is for external consumers; dashd is internal.

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
  minted by `auth.Mint`, persisted by the auth library. Substrate
  every other tool consumes, not a user-tool itself.
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
  from tier defaults. Same as today.

`auth.Authorize` is the only check. resreg's per-resource `Authz`
callback derives `(scope, params)` from the call and delegates —
there is no parallel predicate machinery. The
`<resource>:<verb>[:own_group]` shorthand is the operator-token-minting
affordance over the same ACL rows.

## Resource ownership across daemons

`groups` is gated's; `invites` is onbod's; `web_routes` is webd's.
Each registers its own resources. The MCP socket terminates in gated,
so MCP calls to `invites.*` must forward to onbod over HTTP. Pattern
(shipped 2026-05-25): the forwarder is a `Resource{Store: nil}` whose
`Handler` does an HTTP call downstream; the adapter skips the
tx/audit dance, and the destination daemon writes the audit row.
`webd/routes_mcp.go` is the canonical example.

## Phased rollout

| Phase | Deliverable                                                                                                                                                                                          |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A     | `Caller`, `Resource`, `Endpoint`, `MCPTool`, `ScopePred` types in `auth/` (alongside `auth.Mint` per [`1-auth-standalone.md`](1-auth-standalone.md)). `RegisterResource` helper. No behavior change. |
| B     | High-priority resources via `resreg`: `acl`, `groups`, `secrets`, `invites`. Operator-facing core; the agent-facing tools already exist.                                                             |
| C     | Backfill missing REST mirrors for existing agent MCP tools (`set_grants`, `register_group`, `add_route`, `set_web_route`). One PR per resource.                                                      |
| D     | Migrate `routes`, `scheduled_tasks` one at a time; each migration deletes the hand-written tool in `ipc/ipc.go` and the direct DB call.                                                              |
| E     | Cutover `cmd/arizuko/*.go` to call the local MCP socket. Deletes direct `store.*` calls from `cmd/`. CLI becomes a thin client.                                                                      |
| F     | Cutover dashd write handlers. `/dash/me/secrets` becomes a `secrets.create` dispatch. Drops the dashd-private REST path.                                                                             |
| G     | Deprecate hand-written tools in `ipc/ipc.go` that have a registry equivalent; delete after one release.                                                                                              |

Each phase is independent. Stopping at C still leaves the system in
a saner state (REST parity); E+F are the structural wins.

## Acceptance

- A new `Resource{}` value added to the registry is automatically
  reachable via both `POST /v1/<resource>` AND `<resource>.create` MCP
  tool — no code beyond the struct literal.
- Auth tests:
  - Agent token with `grants:write:own_group` and
    `Identity.Folder = "atlas/support"` can `PATCH /v1/grants/{id}`
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

- **vs [`R-platform-api.md` "Resource model"](R-platform-api.md)**:
  that spec leaves MCP tool registration as "each daemon's MCP server,
  named for agent ergonomics", with no formal link to `/v1/*`. This
  spec adds the formal link. Principle was implicit; here it's mandatory.
- **vs [`specs/3/5-tool-authorization.md`](../3/5-tool-authorization.md)**:
  the tier × action matrix becomes the scope minter — at socket bind,
  `(folder, tier)` → set of `<resource>:<verb>[:own_group]` scopes.
  Tier model authors which scopes get minted; this spec authors how
  scopes are checked.
- **vs [`auth/policy.go`](../../auth/policy.go)** today: hand-maintained
  9-case per-tool switch. After Phase D it is **deleted** — not thinned.
  The per-action `ScopePred` in the registry is the only authorization
  site (`r.Policy[action](caller, target)`). The switch survives in
  CHANGELOG only.

## Open (parked)

- **`:own_group` matching under nested folders.** Subtree containment
  is the lean. Pin when [`R-genericization.md`](R-genericization.md)
  lands `MatchesFolder`.
- **Bulk endpoints.** Inherit
  [`R-platform-api.md`'s "many POSTs, bulk on demand"](R-platform-api.md).
- **Action verbs vs CRUD shape.** `messages.send` / `groups.escalate`
  are action-shaped. Inherit Google's `:verb` convention from
  [`R-platform-api.md`](R-platform-api.md).
- **`ScopePred` concrete shapes** land per-resource in Phase B.

## Code pointers

- `auth/` ([`README.md`](../../auth/README.md)) — gains `Caller`,
  `Resource`, `Endpoint`, `MCPTool`, `ScopePred`, `RegisterResource`
  per Phase A.
- [`auth/policy.go:14-96`](../../auth/policy.go) — current `Authorize`
  switch; Phase D replaces with registry lookup.
- [`ipc/ipc.go:32-120`](../../ipc/ipc.go) — `GatedFns`/`StoreFns` plus
  per-tool registrations; resource-action tools migrate to
  `RegisterResource`, shrinking the file.
- [`proxyd/main.go:590-634`](../../proxyd/main.go) — signed-identity
  header path; the `Caller` builder for REST. After
  [`R-platform-api.md` Phase 1](R-platform-api.md), this becomes the
  same `auth.VerifyHTTP` call every backend uses.
- [`gated/`](../../gated/), [`timed/`](../../timed/),
  [`onbod/`](../../onbod/) — call `RegisterResource` per owned resource.
