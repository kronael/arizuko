---
status: spec
depends: [R-platform-api, 1-auth-standalone, 2-proxyd-standalone]
---

# Uniform REST + MCP per resource

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
    Identity auth.Identity   // Sub, Folder, Tier, Audience, Issuer, Expires
    Scope    []string        // "<resource>:<verb>[:own_group]"
    Surface  string          // "rest" | "mcp" — diagnostic only; policy never reads it
}

type Resource struct {
    Name      string
    Endpoints []Endpoint                      // REST faces
    MCPTools  []MCPTool                       // MCP faces
    Policy    map[Action]ScopePred
    Handler   func(Caller, Action, Args) (Result, error)
}

type Endpoint  struct { Path, Verb string; Action Action }
type MCPTool   struct { Name string; Action Action }
type Action    string                         // "create" | "update" | "delete" | "list" | "get"
type ScopePred interface{ Allows([]string) bool }
```

REST verbs and MCP tool names collapse to one `Action` per handler
branch. New action: one handler branch + one endpoint + one tool + one
policy row. New surface for an existing action: one row, no code.

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
- `*:*`, `<resource>:*` — admin shortcuts.

`HasScope(c, "grants", "write")` returns true iff caller has
`grants:write` OR (`grants:write:own_group` AND target folder ⊆
`Identity.Folder`). The folder check piggybacks on the suffix; no new
primitive.

## Per-resource access matrix

| Resource            | `read`   | `write`  | `read:own_group` | `write:own_group`                                                      | Backing tables                                                  |
| ------------------- | -------- | -------- | ---------------- | ---------------------------------------------------------------------- | --------------------------------------------------------------- |
| `grants`            | operator | operator | agent + user     | agent + user                                                           | `grants` (gated)                                                |
| `routes`            | operator | operator | —                | —                                                                      | `routes` (gated)                                                |
| `secrets`           | operator | operator | —                | user (`/dash/me/secrets`, [`specs/9/11`](../9/11-crackbox-secrets.md)) | `secrets` (gated)                                               |
| `scheduled_tasks`   | operator | operator | agent + user     | agent + user                                                           | `scheduled_tasks` (timed)                                       |
| `chats`             | operator | operator | agent + user     | — (operator-only)                                                      | `messages` (gated)                                              |
| `group_folders`     | operator | operator | —                | —                                                                      | `groups` (gated)                                                |
| `egress_allowlist`  | operator | operator | —                | agent                                                                  | crackbox register ([`specs/9/10`](../9/10-crackbox-arizuko.md)) |
| `user_groups` (ACL) | operator | operator | —                | —                                                                      | `user_groups` (gated)                                           |
| `invites`           | operator | operator | agent (tier ≤ 1) | agent (tier ≤ 1)                                                       | `invites` (onbod)                                               |

Rationale: `routes`/`group_folders`/`user_groups` are operator-only on
both axes — the agent can't reach into its own ACL or topology; that's
the trust boundary. `secrets:write:own_group` is user-via-dashboard
only; the agent never reads or rotates secrets (invariant in
[`specs/9/11`](../9/11-crackbox-secrets.md); the broker resolves
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

## OAuth single-login

For the human caller, REST is the only relevant surface. One OAuth
flow produces one session JWT. No second login for "the MCP side";
MCP is the agent's surface, the agent is a different principal
(`sub: "agent:<folder>"`, not `user:<sub>`).

## Phased rollout

| Phase | Deliverable                                                                                                                                                                                          |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A     | `Caller`, `Resource`, `Endpoint`, `MCPTool`, `ScopePred` types in `auth/` (alongside `auth.Mint` per [`1-auth-standalone.md`](1-auth-standalone.md)). `RegisterResource` helper. No behavior change. |
| B     | Migrate `grants` end-to-end: both `/v1/grants` and `grants.add`/`grants.update`/`grants.remove` MCP tools call the same handler.                                                                     |
| C     | Migrate `routes`, `scheduled_tasks`, `invites` one at a time; each migration deletes the hand-written tool in `ipc/ipc.go` and the direct DB call.                                                   |
| D     | Deprecate hand-written MCP tools that lack a REST mirror (or vice versa). Each unmatched tool becomes a registry entry or is removed.                                                                |
| E     | `arizuko <resource> <action>` CLI becomes a thin REST client over the operator session.                                                                                                              |

Phase A unblocks
[`R-platform-api.md` Phase 2 (gated `/v1/*`)](R-platform-api.md).

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
- Not rate limits ([`specs/8/4-rate-limits.md`](../8/4-rate-limits.md)).
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
  per-tool switch. After Phase B, per-tool logic moves into per-action
  `ScopePred`; `Authorize` becomes a thin wrapper looking up the
  resource by tool name.

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
