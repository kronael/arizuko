# resreg

Resource registry: one `Handler` per `(Resource, Action)`, wrapped by
auto-adapters so REST, MCP, OpenAPI, and YAML reach the same code from
one typed `Resource` literal + `RowType` struct. Spec:
[`specs/5/5-uniform-mcp-rest.md`](../specs/5/5-uniform-mcp-rest.md) (the
unified handler model) and
[`specs/5/36-yaml-manifests.md`](../specs/5/36-yaml-manifests.md) (the
reflective engine + manifests).

## Reflective engine (shipped)

`engine.go` drives SELECT / INSERT / DELETE, YAML parse/emit, and
OpenAPI schema generation off `Resource.RowType` reflection (struct
field → column via `json:` tag). `resreg/resources/` declares one Go
file per cold-tier resource — a `Row` struct plus an `init()` block
calling `resreg.Register`. The 10 resources registered today: `acl`,
`acl_membership`, `groups`, `network_rules`, `onboarding_gates`,
`proxyd_routes`, `routes`, `scheduled_tasks`, `secrets`, `web_routes`.
Token resources (`invites`, `route_tokens`) are parked out of v1
manifests — CLI/MCP only.

`arizuko apply`/`plan`/`get`/`export` (`cmd/arizuko/apply.go`) are the
operator CLI over the engine:

- `apply <instance> <manifest.yaml> [--force]` — state-based apply
  (DELETE+INSERT in one tx), `config_version` compare-and-swap to
  reject stale manifests (`--force` bypasses); prints the plan delta
  before committing.
- `plan <instance> <manifest.yaml>` — non-mutating diff vs live config
  (`resreg.Plan`): per-resource add/update/unchanged/remove by PK.
- `get <instance> <resource>` — emit one resource's live rows as a
  YAML fragment (`resreg.GetResource`) that re-applies to a no-op.
- `export <instance> [out.yaml]` — dump the store as one
  canonical-ordered YAML doc.

Secrets are `SkipApplyRebuild` (export/diff only, never DELETE+INSERTed)
and excluded from the `config_version` count per spec.

## Live REST/MCP resources

- proxyd's runtime route table — `proxyd/resource.go` (store-backed,
  tx-bound audit).
- webd's operator-side MCP forwarder for `routes.*` —
  `webd/routes_mcp.go` (forwarder; `Resource.Store == nil`; proxyd
  writes the audit row downstream).

`ipc/ipc.go` migration of the agent-facing tool surface is pending — a
larger surgical pass tracked under
[`specs/5/5-uniform-mcp-rest.md`](../specs/5/5-uniform-mcp-rest.md).

## OpenAPI emission

`OpenAPI(daemon, baseURL, resources)` / `OpenAPIHandler(daemon,
resources)` (`openapi.go`) walk the registry and emit an OpenAPI 3.1
JSON doc off the same `RowType` reflection — struct field → schema
property, resource → `/v1/<name>` list/create/update/delete paths. No
`huma`, no `swag`, no codegen. Handler is public (mount before auth)
and caches the blob for the process lifetime. Mounted at
`/openapi.json` on `gated` (`api/api.go`), timed, onbod, webd, proxyd,
dashd. Drift between handler and doc is impossible because both read
the same struct. Aggregator landing:
`/pub/arizuko/reference/openapi.html`.

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
Table: [`specs/7/F-audit-stream.md`](../specs/7/F-audit-stream.md).

## Adding a resource

1. Declare typed `Resource` literal in your daemon: `Name`,
   `Endpoints`, `MCPTools`, `Authz`, `Handler`, `Store`.
2. Implement one `Handler` that switches on `x.Action`. Run mutations
   via `x.Tx` when `Store` is set.
3. Wire from `main.go`: `resreg.RegisterREST(mux, r, build)` and
   `resreg.MCPTools(srv, r, callerFor)`.

`proxyd/resource.go` is the canonical store-backed example;
`webd/routes_mcp.go` is the canonical forwarder example.
