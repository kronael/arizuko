---
status: partial
depends:
  [5/5-uniform-mcp-rest, 5/41-ext-mcp, 5/45-openapi-mcp, specs/4/9-acl-unified]
moved_from: specs/8/index.md §1 (was "phase 8 action 1"; pulled to phase 5)
---

> **Status (2026-07-06).** Agent-MCP faces: all 5 cold-tier resources migrated
> onto one `resreg.Resource` via the injected Gate. REST second faces folded onto
> the shared handler: `web_routes`, `acl`, `routes`, `scheduled_tasks`.
> Containment for the tier-structural handlers (`routes`, `scheduled_tasks`) is
> decoupled into a routd-internal per-face `containFn` (agent → tier
> `AuthorizeStructural`, REST → `ownsFolder`), which closed a live cross-tenant
> task-management leak (`0d25b687`). `network_rules` keeps containment in its
> Gate already and is agent-only (no REST twin) — the clean model, nothing to
> fold. OpenAPI now emits each resource's real mounted `Endpoints` (`7c14efd6`),
> so the doc no longer drifts to a PK-CRUD guess. The dashd tool-browser now
> renders the migrated facade tools (`df9ebad3` + `d5023c60`). Remaining: the
> one-owner + federation phase.

# specs/5/44 — MCP+REST unification (finish the adoption)

[`5/45-openapi-mcp`](45-openapi-mcp.md) is the **mechanism** — one
`resreg.Resource` per `(resource, action)` wearing two faces: the agent's
MCP tools (derived by `deriveMCPTools`) and the human's annotated REST
`/openapi.json`. This spec is the **adoption program**: migrate every
resource onto that mechanism and delete the parallel hand-rolled surfaces.
Orthogonal to `5/45` — mechanism vs rollout.

## The target

One cold-tier handler, **two faces from one `resreg.Resource`**, one owner:

```
       one in-process resreg.Resource (Handler + Endpoints + MCPDoc)
                    │                                  │
       deriveMCPTools + injected Gate         openapi.go (x-mcp-when)
                    ▼                                  ▼
      MCP tools (agent, tier-gated)     REST /v1/<res> + annotated doc
                    │                                  │
                    ▼                                  ▼
                the agent                    dashd HTML + external CLIs
                                             (call the REST face — no
                                              hand-rolled CRUD)
```

- **One handler, two faces.** One in-process `resreg.Resource` per
  cold-tier management resource; authorization is an **injected `Gate`**,
  not resreg policy — operator REST defaults to `auth.Authorize`, the
  agent socket injects its `mcp:`+tier `db.Authorize`. The agent's MCP
  tools are `deriveMCPTools`'d from its `Endpoints`+`MCPDoc`; the REST doc
  is emitted from the same `MCPDoc` — never a second hand-written tool
  list.
- **Hot-tier stays MCP-only** in `ipc/ipc.go` (`reply`/`send`/`inspect_*`) —
  no REST resource to derive from; hand-authored.
- **One owner + federation**: each resource's table lives in exactly one
  daemon's DB; cross-daemon reads call the owner's face over HTTP — the owner
  resolves by compose service naming (`<DAEMON>_URL`), never a second
  `store.Open`. This retires `messages.db` as a shared 8th DB.

(Current state is the **Status** blockquote at the top — one source, no drift.
What follows is the design rationale: why the agent socket needs work resreg
doesn't do, and the `Gate` seam that resolved it.)

## Why the agent socket needs work resreg doesn't yet do

`proxyd_routes` proves the two-face mechanism ONLY on the OPERATOR socket:
its MCP face lives on **webd's operator socket**, callers carry a `**` ACL
row, and it is a GLOBAL forwarder with no folder-containment. The **agent
socket** (`ipc.buildMCPServer`, per-folder in-container agent) authorizes
on a different axis than `resreg.invoke` models today:

- **Tier, not scope.** The agent path keys `mcp:<tool>` with
  `AuthorizeOpts{Folder,WorldFolder,Tier}` → the tier-default-grants
  fallback (`auth/authorize.go`, `grants/grants.go`). `resreg.invoke`
  hardcodes `auth.Authorize("<name>:<action>", …)` with EMPTY opts → no
  fallback → it DENIES every folder agent (current path allows
  `mcp:set_web_route` for a tier-0 folder; a naive resreg path denies
  `web_routes:create`).
- **Visibility is a separate firewall.** `granted()`/`MatchingRules`
  (`ipc/ipc.go`) gate which tools a tier even SEES in `tools/list`. An
  unconditional `resreg.MCPTools` `AddTool` would WIDEN visibility.

## Resolution — the injected `Gate` seam (not "bake agent policy into resreg")

resreg does NOT re-implement `granted()` or tier logic. Instead resreg
gains a **`Gate` seam** on the `Resource` and stays policy-free:

- **resreg** owns the handler, tx, audit, arg-derivation, and tool-spec
  generation, and CALLS an injected `Gate`. The `Gate` DEFAULTS to today's
  operator `auth.Authorize` — so proxyd/webd and every REST mount are
  unchanged.
- **routd** mounts the agent tools and OVERRIDES the `Gate` with the
  proven agent closure: `db.Authorize(sub, folder, "mcp:"+tool, params)`
  (`routd/sibling_db.go`) — the tier-default-grants path intact.
- **In-process, not a second server.** The agent MCP tool is a GENERATED
  thin facade over the SAME in-process handler (`Store` non-nil), so the
  one-audit-row-in-the-mutation's-tx invariant holds. It is neither an
  HTTP forward nor a second authz server.
- **Visibility stays in the socket filter.** `resreg.MCPTools` does not
  decide tier visibility; agent-tool registration stays driven by the
  socket's `MatchingRules` filter (or a `visible(name)` predicate).

This supersedes the earlier open A/B choice (A: re-implement the
tier/`granted()` decision inside `resreg.invoke`; B: share only the
handler, keep agent MCP fully hand-rolled). The `Gate` is the third path —
resreg stays policy-free (A's flaw avoided) while the agent tool bodies
still collapse onto the shared handler (B's duplication removed). Enabler
already shipped: the `MCPNames` override that keeps flat agent tool names
(`add_route`, `set_web_route`) across the migration (`443dc4d3`).

## The deliverable: MCP management of the whole platform, written once

The goal is **agent-first platform management** — every cold-tier management
resource (`routes`, `acl`, `groups`, `secrets`, `scheduled_tasks`,
`network_rules`, `web_routes`, `route_tokens`, `onboarding_gates`,
`proxyd_routes`) reachable via **MCP** (agent, tier-gated) AND **REST**
(human, OAuth-gated), one in-process handler. That's `5/45`'s principle;
this spec finishes the coverage and collapses the bespoke surfaces onto it.

**Written once, not twice** — one `resreg.Resource` is authored; the agent's
MCP tools are `deriveMCPTools`'d from its endpoints and the REST doc emitted
from the same `MCPDoc`. No hand-authored `MCPTools` list per resource;
shipping 5/45 is the mechanism this spec rides.

**Two surfaces stay distinct — don't merge them:**

- **Cold-tier management** (`/v1/*` resources) — REST-authored, MCP-derived.
  `ipc/ipc.go`'s hand-rolled management tools (`list_routes`/`add_route`/…)
  and `dashd`'s hand-rolled CRUD collapse onto this one handler per resource.
- **Hot-tier agent actions** (`reply`, `send`, `engage`, `inspect_*`) —
  **MCP-only by design**; no REST resource to derive from. They stay
  hand-authored in `ipc/ipc.go`. So ipc.go doesn't disappear — it loses its
  management tools, keeps the hot-tier tools + the unix-socket transport.

The orthogonal sibling concern is `5/36` — the **same cold-tier resources as
declarative YAML manifests** you `export`/`apply` (config-as-data). 5/44 is
the runtime REST+MCP surface; 5/36 is the manifest transport. Same tables,
two orthogonal fronts — the row-level plumbing underneath both is not a
concept worth naming.

## Two resources share the label `routes` (don't conflate them)

- **message-routing** `routes` (routd's `routes` table: match→target folder):
  COLLAPSED onto one `routesResource` handler (`routd/routes_resource.go`)
  serving both faces — agent MCP (`add_route`/`set_routes`/`list_routes`) and
  operator REST `/v1/routes` (`routd/routes_http.go` via `resreg.RegisterREST`).
  The design-call that held it back — `{id}` REST addressing vs
  `(seq,match,target)` PK + the seq-0 self-default guard — resolved in the
  shared handler; containment is the per-face `containFn` (agent → tier
  `AuthorizeStructural`, REST → `ownsFolder`).
- **HTTP-proxy** `proxyd_routes` (proxyd's table): proxyd resreg REST
  `/v1/proxyd_routes` + `webd/routes_mcp.go` MCP forwarder. **Already full
  dual-dispatch — the exemplar to replicate, not a surface to collapse.**

## Migration recipe (per resource)

Every cold-tier resource migrates the same shape; each step ships and
reverts independently:

1. **Extract the shared in-process handler** — one handler holding the
   resource's logic, tx, and arg-derivation, folding in the bespoke
   semantics the hand-rolled agent tool carried: `set_routes`' seq-0
   self-default guard, `del_web_route`'s tier-0 delete widening, a
   resource's self-slot / path-claim rule. This handler is the single
   renderer both faces sink to.
2. **Mount REST** via `resreg.RegisterREST` with the DEFAULT `Gate`
   (operator `auth.Authorize`) + `x-mcp-when` OpenAPI annotation; `dashd`'s
   admin page calls this face instead of its direct-DB CRUD.
3. **Keep the agent tool's socket registration** — it stays registered
   through `buildMCPServer` so the tier visibility filter
   (`MatchingRules`) and the `mcp:`+tier authz (`db.Authorize`, via the
   injected `Gate`) stay intact; only its BODY changes to delegate into
   the shared handler. Nothing of the agent's proven auth path moves into
   resreg.
4. **Reconcile the audit shape** — the agent path's `emitSys` /
   `LogIPCAudit` and resreg's `audit.EmitInTx` must write the SAME row for
   the same action, so `grep 'action=<verb>'` returns work regardless of
   surface. Drift here re-splits the surfaces the migration just merged.
5. **Delete only the per-tool BODY.** The shared closures — `registerRaw`,
   `granted`, `authorizeCall`, `authzStructural`, `emitSys` — and every
   hot-tier tool (`reply`/`send`/`inspect_*`) STAY; `ipc.go` keeps the
   unix-socket transport plus the agent's auth/visibility scaffolding.

**Order** — tractable first (both faces already mirror one writer, parity
provable): `web_routes` (where the `Gate` blocker surfaced),
`network_rules` (MCP-only → add a REST face), `onboarding_gates` (near-pure
CRUD on onbod), `scheduled_tasks` (cron parsing in the handler), `secrets`
(write-only: create + delete, no list/get). Then the design-call resources
that need an addressing/scope decision first: `routes` (`{id}` REST key vs
`(seq,match,target)` PK + seq-0 guard), `acl` (`**`→membership branch,
body-DELETE — take the stricter MCP scope-containment, closing a REST
cross-folder hole), `groups` + `acl_membership` (cross-daemon writers,
spawn side-effects — highest risk).

**One owner + federation** (final) — fold each resource's table to its
owner daemon, repoint non-owner reads to the owner's face, and retire
`messages.db` + its `store/migrations` twin once no reader remains.

## REST-face reconciliation (resolved — 2026-07-06)

The agent MCP face of all five cold-tier resources (`web_routes`,
`routes`, `network_rules`, `scheduled_tasks`, `acl`) rides one shared
`resreg.Resource` handler with an injected tier `Gate`. Folding each
resource's **REST** face onto that same handler was blocked not by auth —
the scoped self-service check maps cleanly onto an injected REST `Gate`
(`s.verify.Verify` → `hasAnyScope` + `ownsFolder`, reproduced verbatim) —
but by **wire-contract drift** the then-hand-rolled REST handlers carried
that resreg's conventions did not match. Concretely, on `web_routes` the
fold had to reconcile:

1. `GET /v1/web_routes` is entangled with the `?path_prefix=` owner-lookup
   (same method+path; `ServeMux` can't branch on query) — the aux must move
   to its own endpoint before the resreg list can own that path.
2. `DELETE` changes response (`{deleted:bool}` → `{ok:true}`/404) **and**
   would adopt the shared handler's tier-0 widening — a _new_ cross-folder
   delete for tier-0 REST callers (any top-level folder is tier-0).
3. error body differs (`{error}` vs `{error,message}`); 4. list JSON
   differs (`redirect_to` omitempty); 5. the shared handler acts on
   `Caller.Folder`, but the REST face acts on the client-supplied target
   bounded to the subtree — reconciling needs a body-read in `CallerFromHTTP`.

So the REST-face fold was a **contract decision on a security surface**, not
a mechanical cleanup. **Resolved — full-unify shipped.** The user took the
full-unify call, so `web_routes`/`routes`/`scheduled_tasks`/`acl` adopted
resreg's REST conventions (error/list/delete shapes + tier-0 delete widening +
`?path_prefix=` relocated to `/v1/web_routes/owner`) as the new REST contract;
the tier-structural handlers (`routes`, `scheduled_tasks`) got the per-face
`containFn` decouple (agent → tier `AuthorizeStructural`, REST → `ownsFolder`),
which also closed a live cross-tenant task leak (`0d25b687`). `network_rules`
stays agent-only — no REST twin to fold.

## Acceptance

- A resource is served by exactly one in-process `resreg` handler; its
  agent MCP tool, its `/v1/<res>` REST endpoint, and its OpenAPI entry all
  route through it — REST under the default `Gate`, agent under the
  injected `mcp:`+tier `Gate`.
- `dashd` admin page for that resource has no CRUD SQL — it calls the face.
- `ipc/ipc.go` has no bespoke handler BODY for that resource — its agent
  tool delegates to the shared handler; only the socket registration + the
  tier authz/visibility scaffolding remain.
- Agent and REST write the same-shape `audit_log` row for the same action.
- Its table lives in one DB; no second daemon `store.Open`s it.
- `make test` green per step; `arizuko apply` round-trips the resource.

## Out of scope

- The `5/5` mechanism itself (shipped).
- Data-model sharpening (`8/2`) and git-as-truth (`8/3`) — the other two
  phase-8 actions, still phase 8; do not smuggle them in here.
