# resreg

Resource registry: one `Handler` per `(Resource, Action)`, wrapped by
auto-adapters so REST, MCP, OpenAPI, and YAML reach the same code from
one typed `Resource` literal + `RowType` struct. resreg owns the
handler, its tx + audit, argument derivation, and MCP tool-spec
generation — plus one INJECTED authz `Gate` per surface. It decides no
auth policy of its own: it is not a second authz server. Spec:
[`specs/5/17-openapi-mcp.md`](../specs/5/17-openapi-mcp.md) (the canonical
unified handler model — REST authored, MCP derived) and
[`specs/5/8-yaml-manifests.md`](../specs/5/8-yaml-manifests.md) (the
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

- `apply <instance> <manifest.yaml> [--force] [--as-folder <folder>]` —
  state-based apply, one tx per owner DB (scoped DELETE+INSERT). The CAS
  is `resreg.Checksum`, a content hash of the canonical export projection
  recomputed inside the writing tx — no counter, no table, nothing for a
  writer to remember to bump; `--force` bypasses it. Prints the plan delta
  before committing. `--as-folder` re-scopes a single-folder manifest via
  `Resource.Retarget` (`cmd/arizuko/retarget.go`).
- `plan <instance> <manifest.yaml>` — non-mutating diff vs live config
  (`resreg.Plan`): per-resource add/update/unchanged/remove by PK.
- `get <instance> <resource>` — emit one resource's live rows as a
  YAML fragment (`resreg.GetResource`) that re-applies to a no-op.
- `export <instance> [out.yaml]` — dump the store as one
  canonical-ordered YAML doc per subsystem.

Two guards wrap every apply, in `cmd/arizuko/apply.go` rather than in the
engine, because both span documents the engine only ever sees one at a time:

- **The missing-group rule** — `KnownFolders` + `ValidateFolderRefs` refuse,
  before the first tx opens, a manifest naming a folder that is neither a
  `groups` row in the document set nor already live. `--force` does not
  override it.
- **Cross-subsystem pre-image rollback** — `routd.db` and `onbod.db` share no
  transaction, so a failure on the second restores the first from a pre-image
  taken with `ExportSnapshot` and replayed through `Apply` itself.

Secrets, route tokens, invites and onboarding admissions are
`SkipApplyRebuild` (export/diff only, never DELETE+INSERTed). They still
enter the content hash, since it hashes whatever the export projection shows;
their VALUES travel only in the archive (`resreg/archive.go`), never in a
config manifest.

## Live REST/MCP resources

- proxyd's runtime route table — `proxyd/resource.go` (store-backed,
  tx-bound audit).
- webd's operator-side MCP forwarder for `routes.*` —
  `webd/routes_mcp.go` (forwarder; `Resource.Store == nil`; proxyd
  writes the audit row downstream).
- routd's cold-tier agent tools — `routes`, `web_routes`,
  `network_rules`, `scheduled_tasks`, `acl` (one `*_resource.go` each),
  mounted on the agent MCP socket via a `postBuild` seam with the
  agent `Gate` injected, and — where a REST twin exists (`routes`,
  `web_routes`, `scheduled_tasks` as `/v1/tasks`, `acl`) — the same
  handler on the operator REST face with a scope/folder `Gate`. This is
  the spec 5/16 fold. The hot-tier tools (`reply`/`send`/`inspect_*`)
  stay hand-authored in `ipc/ipc.go` — no REST twin.

Spec: [`specs/5/16-mcp-rest-unification.md`](../specs/5/16-mcp-rest-unification.md).

## OpenAPI emission

`OpenAPIHandler(daemon, mux)` (`openapi.go`) emits an OpenAPI 3.1 JSON
doc for **whatever that mux serves**. `MountedResources(mux)` derives the
advertised set: `RegisterREST` mounts each face as a `*restMount` stamped
with its `(resource, endpoint)`, and only registry resources the mux
resolves to one of those are documented. Schemas come from the same
`RowType` reflection (struct field → schema property).

A daemon passes no resource list, so it cannot advertise what it does not
mount — the hand-passed lists were BUGS `F33`, the cause under `F21`/`F27`/
`F32`. Identity, not path presence, is what is checked: three daemons serve
`GET /v1/sessions` over three different tables, and proxyd answers every
path from a `/` catch-all, so a path probe would document other daemons'
schemas. Hand-rolled `mux.HandleFunc` mounts and catch-alls are not
`*restMount` and drop out.

The doc is built on first REQUEST, not at construction, so `/openapi.json`
may be mounted on the mux it documents; it then caches for the process
lifetime. Public — mount before auth. No `huma`, no `swag`, no codegen.
Mounted on `routd`, `runed`, `authd`, timed, onbod, webd, proxyd, dashd.
Aggregator landing: `/pub/arizuko/reference/openapi.html`.

`OpenAPI(daemon, baseURL, resources)` is the underlying emitter, taking
resources directly; tests use it with `resregtest.Mounted(...)`. A resource
with no `Endpoints` falls back to the `/v1/<name>` PK-CRUD convention, which
nothing mounted reaches today (every registry resource declares `Endpoints`).

Per-action derivation: MCP tools are `deriveMCPTools`'d from the
`Endpoints` whose `Action` has an `MCPDoc` entry, and each tool's args
are reflected **per action** (via `MCPArgs`) so a read-one tool doesn't
carry create-body fields and vice-versa. Each such operation's OpenAPI
`description` carries the same `MCPDoc` prose + the `x-mcp-when`
annotation. `proxyd_routes` is the first resource to ride this end to
end: it exports a `redirect_to` field (a redirect target as an
alternative to `backend`), and its REST endpoint set is single-sourced
as `resources.ProxydRoutesEndpoints` — the one list both `proxyd`
(`proxyd/resource.go`) and the `webd` MCP forwarder (`webd/routes_mcp.go`)
register, so the two faces can't drift.

## Surface

- `RegisterREST(mux, r, build)` — emits HTTP handlers for every
  endpoint declared on the resource. `build(*http.Request) (Caller, error)`
  resolves identity per request.
- `MCPTools(srv, r, callerFor, visible)` — emits matching MCP tools.
  `callerFor` is invoked **per call**, not at registration time —
  privilege confusion in shared MCP servers is structurally precluded.
  `visible(name)` gates which tools the caller may even see in
  `tools/list` — `auth.EffectiveActions`, i.e. does the caller hold the
  action at any scope (nil → all visible).

## Types

- `Resource{Name, Endpoints, MCPDoc, MCPArgs, Authz, Gate, Handler,
Store, RowType}` — one literal per resource per daemon. `Gate` is the
  injected authz decision (nil → operator `defaultGate`; agent socket
  overrides with its own gate). MCP tools are DERIVED
  from `Endpoints` (`deriveMCPTools`) using `MCPDoc` (per-action prose) +
  `MCPArgs` (per-action args); there is no hand-authored `MCPTools` list.
- `Action` — short verb constant (`list`, `get`, `create`, `update`,
  `delete`, or resource-specific). `Action.Mutates() bool` is the
  read-vs-write classifier.
- `Caller{Sub, Name, Folder, Claims}` — surface-agnostic principal.
  `Claims` carries JWT claims the ACL row predicates match
  (e.g. `operator=1`).
- `Execution{Caller, Action, Resource, Args, TurnID, RequestID,
SourceIP, Surface, Tx}` — everything a handler needs. `Tx` is
  non-nil only for mutating actions on store-backed resources.
- `Handler func(ctx context.Context, x Execution) (any, error)` — the
  one code path both surfaces invoke.

## Authorization

resreg owns the plumbing, never the policy. `Resource.Authz(c, action,
args)` derives `(scope, params)` from the request — argument extraction,
not an allow/deny decision. The decision belongs to the injected
`Resource.Gate(x, scope, params)`:

- nil → `defaultGate`: the OPERATOR gate,
  `auth.Authorize(Store, caller, "<Name>:<action>", scope, params)` over
  the ACL rows ([`specs/5/32-acl-unified.md`](../specs/5/32-acl-unified.md))
  — scope/ACL match, no tier. This is what operator REST mounts.
- The daemon mounting the resource on the AGENT socket (routd) injects
  `db.Authorize(sub, folder, "mcp:"+tool, params)`, which calls the same
  `auth.Authorize` over the same `acl` rows — the difference is the
  identity source (socket folder vs JWT), not the policy. Same handler,
  different gate; resreg is not a second authz server.

Forwarder resources (`Store == nil`) skip the gate — the downstream
daemon authorizes. Returning `err` from `Authz` short-circuits (e.g.
validation failure → 400) without reaching the gate. No parallel
predicate machinery; the ACL rows remain the source of truth.

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
Table: [`specs/8/F-audit-stream.md`](../specs/5/I-tool-call-logging.md).

## Adding a resource

1. Declare the SHAPE once in `resreg/resources/<name>.go` — the catalog
   registration (`Name`, `Table`, `RowType`, `PKFields`, `Endpoints`,
   `MCPDoc`/`MCPArgs`/`MCPNames`), with its name added to the const block
   in `resreg/resources/names.go`. Then declare the mounted `Resource`
   literal in your daemon, IMPORTING that shape — `Name:
resources.<X>Name`, `Endpoints: resources.<X>Endpoints`, … — and adding
   only `Authz`, `Handler`, `Store` and the injected `Gate`.
   A resource's `Name` is its wire identity (`/v1/<name>` + the MCP tool
   prefix); it is spelled in `names.go` and nowhere else, so the catalog
   and the mount cannot disagree about what the resource is called.
   `resources/name_source_test.go` fails on a re-introduced string literal.
2. Implement one `Handler` that switches on `x.Action`. Run mutations
   via `x.Tx` when `Store` is set.
3. Wire from `main.go`: `resreg.RegisterREST(mux, r, build)` and
   `resreg.MCPTools(srv, r, callerFor, visible)`.

`proxyd/resource.go` is the canonical store-backed example;
`webd/routes_mcp.go` is the canonical forwarder example.
