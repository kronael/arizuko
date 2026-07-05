---
status: draft
depends:
  [5/5-uniform-mcp-rest, 5/41-ext-mcp, 5/45-openapi-mcp, specs/4/9-acl-unified]
moved_from: specs/8/index.md §1 (was "phase 8 action 1"; pulled to phase 5)
---

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
              one resreg.Resource (Handler + Endpoints + MCPDoc)
                    │                                  │
       deriveMCPTools                         openapi.go (x-mcp-when)
                    ▼                                  ▼
     MCP tools (agent, scope-gated)     REST /v1/<res> + annotated doc
                    │                                  │
                    ▼                                  ▼
                the agent                    dashd HTML + external CLIs
                                             (call the REST face — no
                                              hand-rolled CRUD)
```

- **One handler, two faces.** One `resreg.Resource` per cold-tier
  management resource, one `auth.Authorize` gate. The agent's MCP tools are
  `deriveMCPTools`'d from its `Endpoints`+`MCPDoc`; the REST doc is emitted
  from the same `MCPDoc` — never a second hand-written tool list.
- **Hot-tier stays MCP-only** in `ipc/ipc.go` (`reply`/`send`/`inspect_*`) —
  no REST resource to derive from; hand-authored.
- **One owner + federation**: each resource's table lives in exactly one
  daemon's DB; cross-daemon reads call the owner's face over HTTP — the owner
  resolves by compose service naming (`<DAEMON>_URL`), never a second
  `store.Open`. This retires `messages.db` as a shared 8th DB.

## Current state (under-adopted)

- `resreg` OpenAPI + `apply` + 10 resource declarations: live. ✓
- One resource has real dual-dispatch from a single `resreg.Resource`:
  **`proxyd_routes`** (proxyd's REST `/v1/proxyd_routes` + `webd/routes_mcp.go`
  MCP forwarder + `MCPDoc` — the only `resreg/resources/*.go` with an `MCPDoc`).
  Message-routing `routes` is NOT unified: it still runs two hand-rolled
  surfaces — REST in `routd/routes_http.go` and MCP tools in `ipc/ipc.go`.
  The rollout (pilot `routes`, then replicate) has not started.
- Everything else: agent tools hand-rolled in `ipc/ipc.go` (~45),
  dashd CRUD hand-rolled per admin page, resource read via direct DB open.
- No federation: `proxyd` opens two DB files; `messages.db` is shared.

## Blocker: resreg does not model the AGENT socket (found by the web_routes pilot, 2026-07-05)

The two-face mechanism is proven ONLY on the OPERATOR socket. `proxyd_routes`
works because its MCP face lives on **webd's operator socket**, where callers
carry `**`/operator ACL rows. The **agent socket** (`ipc.buildMCPServer`,
per-folder in-container agent) authorizes differently, and `resreg.invoke`
does not model it:

- **Authz gap**: the agent path keys `mcp:<tool>` with
  `AuthorizeOpts{Folder,WorldFolder,Tier}` → gets the TIER-DEFAULT-GRANTS
  fallback (`grants/grants.go`). `resreg.invoke` hardcodes
  `auth.Authorize("<name>:<action>", …)` with EMPTY opts → no fallback →
  DENIES every folder agent. (Proven: current path allows `mcp:set_web_route`
  for a tier-0 folder; resreg path denies `web_routes:create`.)
- **Visibility gap**: `granted()`/`MatchingRules` gate which tools a tier even
  SEES in `tools/list`. `resreg.MCPTools` `AddTool`s unconditionally → widens
  visibility.
- **Layering**: `ipc` doesn't import `store`/`resreg` by design — driving
  registration from routd needs a new `ipc.ServeMCP` post-build seam.

Opposite failure modes; no `Authz`/`MCPNames` tweak satisfies both. This is
THE work of 5/44 for agent-facing resources — a resreg framework enhancement,
not a mechanical migration. Decision (A: make `invoke` MCP-surface-aware +
`MCPTools` consult `MatchingRules`; vs B: share only the Handler, keep agent
MCP registration hand-rolled behind `granted()`) is recorded in
`.ship/plan-5-44-rollout.md`. `MCPNames` override (flat agent tool names)
shipped as the first enabler (`443dc4d3`).

## The deliverable: MCP management of the whole platform, written once

The goal is **agent-first platform management** — every cold-tier management
resource (`routes`, `acl`, `groups`, `secrets`, `scheduled_tasks`,
`network_rules`, `web_routes`, `route_tokens`, `onboarding_gates`,
`proxyd_routes`) reachable via **MCP** (agent, scope-gated) AND **REST**
(human, OAuth-gated), one handler. That's `5/45`'s principle; this spec
finishes the coverage and collapses the bespoke surfaces onto it.

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
  served by `ipc/ipc.go` agent tools (`add_route`/`set_routes`/`list_routes`)
  - `dashd/routes_admin.go` (direct DB). routd emits OpenAPI only — no
    REST/MCP dispatch. **This is the pilot target.**
- **HTTP-proxy** `proxyd_routes` (proxyd's table): proxyd resreg REST
  `/v1/proxyd_routes` + `webd/routes_mcp.go` MCP forwarder. **Already full
  dual-dispatch — the exemplar to replicate, not a surface to collapse.**

## Migration (pilot → replicate)

1. **Pilot message-routing `routes`** — routd owns the table + already
   declares the resreg resource for OpenAPI. Author its REST face on routd's
   mux (`/v1/routes`, one `auth.Authorize` gate) + annotate the OpenAPI
   (`x-mcp-when`). `dashd/routes_admin` calls that REST face; the agent's
   `routes` MCP tools are `deriveMCPTools`'d from the resource's endpoints,
   replacing ipc.go's bespoke `add_route`/`set_routes`. First close the parity
   risks — `RoutesRow` vs full `core.Route` columns, the folder-scope auth
   resolver, audit_log location. (Name collision already fixed: `proxyd_routes`,
   `aab3487a`.) (`.ship/plan-mcp-rest-unification.md`)
2. **Replicate** to `acl`, `network_rules`, `scheduled_tasks`, `groups`,
   `web_routes` — one resource per pass, same shape.
3. **One owner + federation** — fold each resource's table to its owner
   daemon; repoint non-owner reads to the owner's face; retire
   `messages.db` and the `store/migrations` twin once no reader remains.

Each step is independently shippable and independently revertible.

## Acceptance

- A resource is served by exactly one `resreg` handler; its MCP tool,
  its `/v1/<res>` REST endpoint, and its OpenAPI entry all derive from it.
- `dashd` admin page for that resource has no CRUD SQL — it calls the face.
- `ipc/ipc.go` has no bespoke tool for that resource.
- Its table lives in one DB; no second daemon `store.Open`s it.
- `make test` green per step; `arizuko apply` round-trips the resource.

## Out of scope

- The `5/5` mechanism itself (shipped).
- Data-model sharpening (`8/2`) and git-as-truth (`8/3`) — the other two
  phase-8 actions, still phase 8; do not smuggle them in here.
