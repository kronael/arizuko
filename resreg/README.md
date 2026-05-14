# resreg

Resource registry: one `Handler` per `(Resource, Action)`, wrapped by two
auto-adapters so REST and MCP reach the same code. Spec:
`specs/6/5-uniform-mcp-rest.md`.

Today's only resource is proxyd's runtime route table (spec 6/2 Phase-3).
The package is built so additional resources (grants, scheduled_tasks,
...) drop in as struct literals.

## Surface

- `RegisterREST(mux, r, build)` — emits HTTP handlers for every endpoint
  declared on the resource. `build` constructs the surface-specific
  `Caller` from the request (identity + scope).
- `MCPTools(srv, r, caller)` — emits matching MCP tools. Tool name is
  `<Resource.Name>.<Action>`; same string used in audit logs.

A `Caller` is the surface-agnostic principal — identity, scopes,
`HasScope(string) bool`. Built once per request by the adapter; passed
into every action.

## Types

- `Resource` — name + per-action endpoint + MCP tool descriptor.
- `Action` — short verb constant (`list`, `get`, `create`, `update`,
  `delete`, or a resource-specific shape).
- `Handler[Req, Resp]` — `func(ctx, caller, req) (resp, error)`. The
  one code path both surfaces invoke.
- `Endpoint` — REST method + path template + status mapping.
- `MCPTool` — JSON-schema for the tool args; arg validation lives in
  the adapter.
- `ScopePred` — predicate over the caller's scopes; declared on the
  endpoint, evaluated before the handler runs.

## Adding a resource

1. Define typed request/response structs.
2. Implement one `Handler` per action.
3. Build the `Resource` literal: name, endpoint+tool per action,
   scope predicates.
4. Call `RegisterREST` from your daemon's mux setup and `MCPTools` from
   its MCP server registration.

The same struct literal is enough for both surfaces — no duplicate
plumbing.
