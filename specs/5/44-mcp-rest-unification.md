---
status: active
depends: [5/5-uniform-mcp-rest, 5/41-ext-mcp, specs/4/9-acl-unified]
moved_from: specs/8/index.md §1 (was "phase 8 action 1"; pulled to phase 5 as active work)
---

# specs/5/44 — MCP+REST unification (finish the adoption)

`5/5-uniform-mcp-rest.md` shipped the **mechanism** — `resreg`: one handler
per `(Resource, Action)`, two faces (MCP for agents, REST for humans),
OpenAPI reflected off `RowType`. This spec is the **adoption program**:
migrate every resource onto that mechanism and delete the parallel
hand-rolled surfaces. Orthogonal to `5/5` — mechanism vs rollout.

## The target

One resource, one declaration, three faces, one owner:

```
resreg.Resource  →  RowType + Handler per Action (list/get/create/update/delete)
       │
   ┌───┼───────────────┬──────────────┐
   ▼   ▼               ▼              ▼
  MCP (agent)     REST (human)     OpenAPI
  ipc socket      /v1/<res>        (reflected)
       │              │
       ▼              ▼
   the agent      dashd HTML + external CLIs
                  (render pages; CALL the REST face — no hand-rolled CRUD)
```

- **MCP-first**: the `(Resource, Action)` handler is the agent contract;
  REST is the same handler as the impedance match; OpenAPI is reflected.
  One auth gate (`auth.Authorize`) bound at registration, both faces.
- **One owner + federation**: each resource's table lives in exactly one
  daemon's DB. Cross-daemon reads call the owner's face over HTTP —
  never a second `store.Open` of a shared DB file. This is what retires
  `messages.db` as a shared 8th database.

## Current state (under-adopted)

- `resreg` OpenAPI + `apply` + 10 resource declarations: live. ✓
- Dual-dispatch faces wired for `routes`/`proxyd_routes` only.
- Everything else: agent tools hand-rolled in `ipc/ipc.go` (~45),
  dashd CRUD hand-rolled per admin page, resource read via direct DB open.
- No federation: `proxyd` opens two DB files; `messages.db` is shared.

## The deliverable: declare once, both faces auto-generated

resreg already auto-derives three things from `RowType` for engine-managed
resources (`resreg/resources/*.go` — `Register(Resource{RowType, Table})`):
the SQL schema, the generic CRUD handler (`engine.go` reflection over `db:`
tags), and OpenAPI. But the **face lists are still hand-written** —
`RegisterREST` iterates `r.Endpoints`, `MCPTools` iterates `r.MCPTools`, both
hand-declared (proxyd writes all ten by hand; the catalog resources declare
neither, so they're OpenAPI-only, unserved).

**5/44 closes that gap.** For an engine-managed resource (`RowType` + the
standard `Actions`), resreg generates the two faces too — the five REST
endpoints (`GET/POST /v1/<name>`, `GET/PATCH/DELETE /v1/<name>/{pk}`) and the
five MCP tools (`<name>.list/get/create/update/delete`) — from the same
declaration. Then a resource is declared **once** (`RowType`, `Table`,
`Authz`) and yields: schema + CRUD handler + OpenAPI + REST face + MCP face,
all reflection-derived, one auth gate. Hand-declared `Endpoints`/`MCPTools`
stay as the **override** for custom/forwarder shapes (proxyd's stateless
route handler) — the escape hatch, not the norm. This is the arizuko way:
derive from the Go struct, no external DSL/codegen (CLAUDE.md Discoverability).

## Two resources share the label `routes` (don't conflate them)

- **message-routing** `routes` (routd's `routes` table: match→target folder):
  served by `ipc/ipc.go` agent tools (`add_route`/`set_routes`/`list_routes`)
  - `dashd/routes_admin.go` (direct DB). routd emits OpenAPI only — no
    REST/MCP dispatch. **This is the pilot target.**
- **HTTP-proxy** `proxyd_routes` (proxyd's table): proxyd resreg REST
  `/v1/routes` + `webd/routes_mcp.go` MCP forwarder. **Already full
  dual-dispatch — the exemplar to replicate, not a surface to collapse.**

## Migration (pilot → replicate)

1. **Pilot message-routing `routes`** — routd already owns the table +
   declares the resreg resource for OpenAPI; wire it to the REST + MCP faces
   the way `proxyd_routes` already is. `dashd/routes_admin` calls routd's
   REST face; agent route tools become `resreg.MCPTools`; delete the two
   bespoke copies. First close the parity risks — `RoutesRow` vs full
   `core.Route` columns, the folder-scope auth resolver, audit_log location.
   Disambiguate the `routes`/`proxyd_routes` name collision. (`.ship/plan-mcp-rest-unification.md`)
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
