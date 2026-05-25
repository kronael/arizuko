# resreg

Resource registry: one `Handler` per `(Resource, Action)`, wrapped by
two auto-adapters so REST and MCP reach the same code. Spec:
[`specs/5/5-uniform-mcp-rest.md`](../specs/5/5-uniform-mcp-rest.md).

Resources using it today:

- proxyd's runtime route table — `proxyd/resource.go` (store-backed,
  tx-bound audit).
- webd's operator-side MCP forwarder for `routes.*` —
  `webd/routes_mcp.go` (forwarder; `Resource.Store == nil`; proxyd
  writes the audit row downstream).

`ipc/ipc.go` migration is pending — a larger surgical pass tracked
under [`specs/5/D-mcp-everywhere.md`](../specs/5/D-mcp-everywhere.md).

## Surface

- `RegisterREST(mux, r, build)` — emits HTTP handlers for every
  endpoint declared on the resource. `build(*http.Request) (Caller, error)`
  resolves identity per request.
- `MCPTools(srv, r, callerFor)` — emits matching MCP tools. `callerFor`
  is invoked **per call**, not at registration time — privilege
  confusion in shared MCP servers is structurally precluded.

## Types

- `Resource{Name, Endpoints, MCPTools, Authz, Handler, Store}` — one
  literal per resource per daemon.
- `Action` — short verb constant (`list`, `get`, `create`, `update`,
  `delete`, or resource-specific). `Action.Mutates() bool` is the
  read-vs-write classifier.
- `Caller{Sub, Name, Folder, Tier, Claims}` — surface-agnostic
  principal. `Claims` carries JWT claims the ACL row predicates match
  (e.g. `operator=1`).
- `Execution{Caller, Action, Resource, Args, TurnID, RequestID,
SourceIP, Surface, Tx}` — everything a handler needs. `Tx` is
  non-nil only for mutating actions on store-backed resources.
- `Handler func(ctx context.Context, x Execution) (any, error)` — the
  one code path both surfaces invoke.

## Authorization

`Resource.Authz(c, action, args)` returns `(scope, params, err)`. The
adapter then calls
`auth.Authorize(Store, callerToAuth(c), "<Name>:<action>", scope, params)`
— the canonical ACL gate from
[`specs/4/9-acl-unified.md`](../specs/4/9-acl-unified.md). No parallel
predicate machinery; the ACL rows are the source of truth.

Returning `err` from `Authz` short-circuits the call (e.g. validation
failure → 400) without touching `auth.Authorize`.

## Tx-bound audit

Mutating actions (`create` / `update` / `delete`) on store-backed
resources (`Resource.Store != nil`) run inside a SQL transaction. The
adapter:

1. Opens `tx := Store.DB().BeginTx(ctx, nil)`.
2. Threads `tx` into `Execution.Tx`; invokes `Handler`.
3. On handler success: writes one `audit_log` row via
   `audit.EmitInTx(ctx, tx, event)` in the **same tx**, then commits.
4. On handler error: rolls back; writes a non-tx `audit_log` row
   (outcome=`error`) via `audit.Emit`.
5. On audit-insert failure: rolls back the mutation — per spec
   contract, the audit row IS the mutation.

Read-only actions emit slog only; no DB row. Forwarder resources
(`Store == nil`) skip the tx + audit dance — the downstream daemon
writes the row. Field schema:
[`specs/5/I-tool-call-logging.md`](../specs/5/I-tool-call-logging.md).
Table: [`specs/6/F-audit-stream.md`](../specs/6/F-audit-stream.md).

## Adding a resource

1. Declare typed `Resource` literal in your daemon: `Name`,
   `Endpoints`, `MCPTools`, `Authz`, `Handler`, `Store`.
2. Implement one `Handler` that switches on `x.Action`. Run mutations
   via `x.Tx` when `Store` is set.
3. Wire from `main.go`: `resreg.RegisterREST(mux, r, build)` and
   `resreg.MCPTools(srv, r, callerFor)`.

`proxyd/resource.go` is the canonical store-backed example;
`webd/routes_mcp.go` is the canonical forwarder example.
