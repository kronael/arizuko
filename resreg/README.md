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
under [`specs/5/5-uniform-mcp-rest.md`](../specs/5/5-uniform-mcp-rest.md).

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

## Schema engine (spec 5/36)

The adapter half above ties a `Handler` to REST + MCP. The **engine
half** (`engine.go`) adds schema-driven CRUD: a tagged Go struct
declares the row shape once, and reflection over `db:`/`yaml:`/`json:`
tags drives SQL, YAML, JSON, and OpenAPI without per-resource code.

A resource opts in by setting `RowType` + `Table` + `PKFields` (+
optional `Scope`, `Hooks`):

```go
type Row struct {
    Seq    int    `db:"seq"    yaml:"seq"    json:"seq"`
    Match  string `db:"match"  yaml:"match"  json:"match"`
    Target string `db:"target" yaml:"target" json:"target"`
}
resreg.Register(resreg.Resource{
    Name: "routes", Table: "routes",
    RowType:  reflect.TypeOf(Row{}),
    PKFields: []string{"Seq", "Match", "Target"},
    Scope:    resreg.ScopeSpec{Field: "Target"},
})
```

Cold-tier resource structs live one-per-file under
[`resources/`](resources/); import the package for its registration
side effects.

Engine methods (all reflection-driven, column list cached at
`Register`):

- `ScanAll(db)` / `Scan(db, where, args…)` → `[]RowType`, ordered by PK.
- `Insert` / `InsertAll(ctx, tx, rows)` — generated `INSERT`, runs
  `Hooks.BeforeInsert` + `Hooks.ValidateRow` in-tx.
- `DeleteScope(ctx, tx, scope)` / `DeleteAll(ctx, tx)`.
- `ParseRows(node)` / `EmitRows(rows)` — YAML decode/encode, PK-sorted.

Package-level apply/export:

- `Apply(ctx, db, manifestVersion, force, manifestRows)` — one
  `BeginTx`; a no-op `UPDATE config_meta` upgrades to a write lock,
  then CAS-checks `config_version` against `manifestVersion` (unless
  `force`), `DeleteAll` + `InsertAll` every registered table (skipping
  `SkipApplyRebuild` resources), bumps the version, commits.
- `Export(db)` → `map[string]any` keyed by resource name + the current
  `config_version`; `EmitYAML` renders it deterministically.
- `ParseYAML(data)` → `(rows-by-name, version, err)`.

CLI wiring: `cmd/arizuko/apply.go` (`apply` single-file, `export`).

### Hooks — semantics the engine can't deduce

`Hooks` carry the per-resource escape hatches (`engine.go`):

- `BeforeInsert` — default timestamps, JSON-encode blobs, derive fields.
- `ValidateRow` — in-tx checks (e.g. `acl_membership` cycle detection).
- `AfterScan` — decode on read (e.g. `proxyd_routes` JSON header array).
- `ColumnOverride` — per-field `Read` SQL expression (`COALESCE(...)`)
  - `Write` binder for nullable columns mapped to non-pointer Go fields.

`SkipApplyRebuild` marks a resource export-only (e.g. `secrets`, whose
`enc_value` blob is set imperatively); `BumpVersion=false` exempts it
from the `config_version` count.

## OpenAPI emission (spec 5/36)

`openapi.go` walks the registry and emits an OpenAPI 3.1 document from
the same `RowType` reflection — no `huma`, no `swag`, no codegen.

- `OpenAPI(daemon, baseURL, resources)` → JSON bytes.
- `OpenAPIHandler(daemon, resources)` → `http.HandlerFunc` that
  lazy-builds + caches the doc for the process lifetime.

Mount on each HTTP daemon **before** auth middleware (the endpoint is
public — it describes API surface, not data). gated/proxyd/onbod own
resources; webd/dashd/timed pass an empty resource list so the
aggregator page (`/pub/arizuko/reference/openapi.html`) can list every
daemon uniformly. Drift between handler and doc is impossible — both
read the same struct.
