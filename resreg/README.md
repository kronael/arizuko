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

`engine.go` drives the reads and writes, YAML parse/emit, and
OpenAPI schema generation off `Resource.RowType` reflection (struct
field → column via `json:` tag). `resreg/resources/` declares one Go
file per cold-tier resource — a `Row` struct plus an `init()` block
calling `resreg.Register`. The 10 resources registered today: `acl`,
`acl_membership`, `groups`, `network_rules`, `onboarding_gates`,
`proxyd_routes`, `routes`, `scheduled_tasks`, `secrets`, `web_routes`.
Token resources (`invites`, `route_tokens`) are parked out of v1
manifests — CLI/MCP only.

## Two shapes, and the resource declares which

A resource's rows are either SQL columns or `objstore` items, and
`Resource.EntitySortKey` is the whole declaration: naming `RowType` fields
there makes `Table` an entity, every other `db:` tag an attribute name, and
those fields' values the item's sort key. `ScanAll` is still the single row
reader and it branches on that declaration — never on what the storage answers,
and never one path as a fallback for the other (`entity.go`, spec
[`6/37`](../specs/6/37-store-model.md) step 6, BUGS `L259`).

The coordinate is a LIST, because two resources have no single-field identity:
`network_rules` is keyed `(folder, target)` — 15 rows over 10 targets on krons,
so `target` alone would collide five of them — and `installed_packages` is
`(folder, name)`. `Resource.EntityKeySep` joins them and no value of any of them
may carry it: a write that does is REFUSED, which is the only end that can see
two rows joining onto one key, and the read refuses a stored key that does not
split into as many parts as the coordinate has. The separator is declared per
resource because which byte no value carries is a fact about the rows — `/`
separates a Slack team, user and thread (`store/pane_sessions`) and can never
separate a folder from anything, because a folder path is made of them. An
EMPTY part is a coordinate: `folder: ""` is how both composite resources spell
an instance-wide row.

The PARTITION is not declared. It is `Resource.DB`, the owner subsystem, because
every read the engine makes is per subsystem and the partition is the coordinate
a read filters on. `SubsystemRoutd`/`SubsystemOnbod` ARE `store`'s owner names,
so the daemon's own storage layer writes items at the coordinate the engine
reads them from.

The sort key is SEPARATE from `PKFields` because they answer different
questions: this is the item's coordinate, `PKFields` is the manifest identity
`Diff` matches on. Separate, but not free — `PKFields` must name every field of
the sort key, and `Register` refuses a declaration that does not. The
coordinate is unique by construction, so a PK covering it is unique too; a
narrower one files two items under one identity and `Diff` reports whichever it
read last. `route_tokens` named only `jid` while krons held 43 tokens over 27
jids, so 16 live ingress credentials answered to one identity (BUGS `L260`).

What the object path REFUSES, it refuses at `Register`, which is process start,
and each refusal carries the reason it cannot cross:

| declaration                                                                    | why not                                                                                                                                                                        |
| ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `Hooks.AfterScan`, `Hooks.ValidateRow`, `Hooks.ColumnOverride`                 | `ColumnOverride.Read` is a SQL expression, `.Write` is the bind conversion absent-is-null already makes unnecessary, and `ValidateRow` takes a `*sql.Tx` this path never opens |
| a `RowFilter` on a field of the coordinate                                     | a filter is a `Where` over ATTRIBUTES and the coordinate is the key; a key predicate reaches a leading fragment, never one field in the middle                                 |
| an attribute that is not a string, an int, or a field with a declared encoding | every attribute value is text, so a bool, a slice or a map declares its encoding in the `db:` tag (`codec.go`)                                                                 |
| `PKFields` that does not name every field of the coordinate                    | the coordinate is unique by construction and the manifest identity has to be at least as fine, or `Diff` files two items under one PK and reports one of them (`L260`)         |

Two declarations the object path APPLIES rather than refuses, both because an
entity has no engine between the caller and the item.

`Hooks.BeforeInsert` fills a row's declared defaults — `effect='allow'`,
`status='active'` — so `itemFor` runs it before it renders the item, exactly as
it fills a `StampedField`. It takes the ROW and nothing else, which is what lets
one declaration serve both shapes.

`RowFilter` narrows the manifest-visible projection, and it is a declared
predicate: `objstore.Cmp` terms the object path hands to `Query.Where` verbatim
and the column path renders as bound SQL. One declaration, two readers. Here a
delete set is a READ, so the same `Where` narrows the scan, the wholesale wipe
AND the scoped delete — which is the whole reason it must not be a reader-side
predicate, because such a predicate cannot narrow a write. The terms are a
conjunction of `Eq` and `Ne`, so a predicate needing an `OR` has no form and the
answer is an attribute the writer sets: `acl_membership.added_by` is NOT NULL
(routd migration 0047), and `itemFor` writes a filtered attribute even when it is
empty, because an absent attribute answers no comparison and a row without one
would fall out of its own projection.

A `ScopeSpec` is the scoped delete, and on this path it is a QUERY plus one
guarded delete per item — an entity has no table to delete from and no
predicate delete. Which query the DECLARATION decides, at `Register`, because
where the scope value lives is what can select on it: an attribute is a `Where`
term (an EMPTY scope is `Absent`, because an empty string is an absent
attribute), a sort key is an exact key, and the first field of a composite
coordinate is a `begins_with` on `scope+sep` — exact, because no value may carry
the separator, which is what makes `network_rules`' `(folder, target)` purgeable
by folder at all. A scope in the MIDDLE of a coordinate is refused: a key
operator reaches a leading fragment and a coordinate is not an attribute.

Those deletes ride INSIDE `objstore.Replace`'s claim rather than beside it. The
delete set is read and then written, so a second replace taking the scope in
that window has to stop them — otherwise they land on a projection somebody else
is part-way through replacing. `ApplyOpts.PruneScopes` joins the mentioned
scopes exactly as on the column path, so the cross-subsystem rollback still
reaches the folders a forward apply created. Without all of this a folder purge
leaves the next tenant of the path the last one's egress rules, cron tasks, held
actions and web routes (BUGS `L253`).

A field whose Go type is not text declares its ENCODING in the `db:` tag, after
the column name — `db:"preserve_headers,json"`. The set is closed (`json`,
`bool`), never a caller's function, so a resource cannot invent a rendering only
it can read; an unknown name stops the process at `Register`, and so does one on
a string or an int, which both shapes already render the same way.

The codec applies to BOTH shapes and that is the point: the text a column holds
and the text an attribute holds are the SAME text, so a migration carrying a
table from one to the other cannot change a value on the way. It is what
`proxyd_routes`' two `Hooks` do today — a `BeforeInsert` that marshals into a
shadow column and an `AfterScan` that unmarshals back — so dropping them at the
cutover without this would strip the preserved headers from every proxied
route, read once per request over 26 live rows. A codec'd COLUMN is `TEXT`,
because the codec's output is the bind value: `preserve_headers TEXT NOT NULL
DEFAULT '[]'` is, while `strip_prefix INTEGER` and `acl.grant_option INTEGER`
can carry one only once they are attributes.

A `StampedField` is filled by the STORAGE LAYER on this path, because an entity
has no engine between the caller and the item — there is no `Insert` to hang a
`BeforeInsert` on. The layout is `core.TS`, not the column path's RFC3339: a
stored instant is compared as text and `core.TS` is the fixed-width form that
makes byte order and time order the same thing (BUGS `L97`). The two paths
therefore stamp different widths, and a table moving from columns to items
carries its old values with it — the migration normalises them. The
declaration STAYS on an entity-backed resource: it is what tells an operator's
manifest the field is not theirs to set, so `Diff` ignores it and it is not a
`create`/`update` argument.

`Apply` and `ExportSnapshot` become `objstore.Replace` and
`objstore.ReadConsistent` (spec [`6/40`](../specs/6/40-store-surface.md) §3 and
§"The consistent multi-read"). **That gives up the pre-image rollback `L44`
carries**: the column path runs every DELETE and INSERT in one transaction, and
a marker-gated sequence has none — a replace that dies after it has written
leaves the subsystem part-applied with its claim held, and recovery is to run
the apply again. `arizuko apply` says so before it writes.

**Running it again is what gives the claim back.** The claim records the apply's
INTENT — `applyClaim`, derived from the document and the scopes it prunes — so
the same apply takes its own claim over and rolls forward. It converges because
this is the operation that does: every delete set is read live from the
manifest's own scopes, so deleting whatever is there and putting the document
over it lands where a clean run would, whether the dead apply stopped at the
first row or the last. An apply of a DIFFERENT document is refused, because its
delete set covers only the rows it names; `apply --take-over` is the operator
asserting the interrupted work is not coming back (`L267`).

A subsystem must therefore be ONE shape, checked before any write: a mixed one
would roll the columns back and leave the items part-written, with no way to
tell an operator which half landed. That is what lets onbod's three resources
move without waiting for routd's thirteen.

A daemon prepares its database with `resreg.EnsureSubsystem`, beside the
`CreateEntity` calls it already makes — the entity behind each resource, the
entity the version marker lives in, and the marker itself. It is a no-op for a
subsystem still on the column path, so the set follows the registrations rather
than a second list beside them.

`arizuko apply`/`plan`/`get`/`export` are the operator CLI over this engine;
their flags are in `cmd/arizuko/README.md`. The CAS is `resreg.Checksum`, a
content hash of the canonical export projection recomputed inside the writing
transaction — no counter, no table, nothing for a writer to remember to bump.

Two guards wrap every apply, and they live in `cmd/arizuko/apply.go` rather
than in the engine because both span documents the engine only ever sees one
at a time:

- **The missing-group rule** — `KnownFolders` + `ValidateFolderRefs` refuse,
  before the first write, a manifest naming a folder that is neither a
  `groups` row in the document set nor already live. `--force` does not
  override it.
- **Cross-subsystem pre-image rollback** — two owners share no transaction, so
  a failure on the second restores the first from a pre-image taken with
  `ExportSnapshot` and replayed through `Apply` itself.

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
- routd's thirteen — `acl`, `audit`, `dead_letters`, `github_sources`,
  `groups`, `membership`, `network_rules`, `packages`, `route_tokens`,
  `routes`, `scheduled_tasks`, `secrets`, `web_routes` (one `*_resource.go`
  each). Each mounts on the agent MCP socket through a `postBuild` seam with
  the agent `Gate` injected, and — where a REST twin exists — the same
  handler on the operator REST face with a scope/folder `Gate`. That is the
  spec 5/16 fold. The hot-tier tools (`reply`/`send`/`inspect_*`) stay
  hand-authored in `ipc/ipc.go` — no REST twin.
- authd's three — `audit`, `signing_keys`, `sessions` — and onbod's four:
  `invites`, `onboarding_gates`, `onboarding`, `audit`.

Spec: [`specs/5/16-mcp-rest-unification.md`](../specs/5/16-mcp-rest-unification.md).

## OpenAPI emission

`OpenAPIHandler(daemon, mux)` (`openapi.go`) emits an OpenAPI 3.1 JSON
doc for **whatever that mux serves**. `MountedResources(mux)` derives the
advertised set: `RegisterREST` mounts each face as a `*restMount` stamped
with its `(resource, endpoint)`, and only registry resources the mux
resolves to one of those are documented. Request schemas come from the same
`RowType` reflection (struct field → schema property).

**A response body is declared, never inferred.** `Handler` returns `any`, so
reflection cannot see what an action answers with; `Resource.Responses` maps an
action to the Go type its handler returns (or a slice of it) and that type is
what `components/schemas` carries. An action with no entry documents `"OK"` and
**no body schema at all** — a stated gap, which a generated client handles, in
place of a wrong struct, which it silently mis-decodes. The shape is never
derived from the verb (list → `array<RowType>`, GET/POST → `RowType`),
because mounted operations answer with other types: `GET /v1/routes` with
`[]router.RouteView`, `GET /v1/tasks` with `[]core.Task` (Go field names — that
struct has no json tags), the route-token mints with `{token,jid,url}`, onbod's
mutations with `{"ok":true}` (BUGS `L120`).

The declared type is the one declaration and the handler is its second reader:
it constructs that same struct, so a renamed field moves the doc and the wire
together. Only a handler returning a DIFFERENT type can still drift, which is
what `resregtest.AssertDocumentedResponse` serves a real request to catch —
`routd/openapi_response_test.go` runs it over routd's own mux.

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
resources directly; tests use it with `resregtest.Mounted(...)`. One operation
per declared `Endpoint` and nothing else — a resource with no `Endpoints`
contributes only its schema. No `/v1/<name>` operation is invented from a
PK-CRUD convention; `resources.TestEveryResourceDeclaresEndpoints` keeps the
case from arising.

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
  `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)`, which calls the same
  `auth.Authorize` over the same `acl` rows — the difference is the
  identity source (socket folder vs JWT), not the policy. Same handler,
  different gate; resreg is not a second authz server.

Forwarder resources (`Store == nil`) skip the gate — the downstream
daemon authorizes. Returning `err` from `Authz` short-circuits (e.g.
validation failure → 400) without reaching the gate. No parallel
predicate machinery; the ACL rows remain the source of truth.

## Batched audit

A mutating action (`create` / `update` / `delete`) on a store-backed
resource (`Resource.Store != nil`) HANDS ITS WRITES OVER rather than
performing them. The adapter:

1. Puts a fresh `store.WriteSet` on `Execution.Writes`; invokes `Handler`.
2. The handler READS what it needs and buffers the writes it decided on —
   `store.…Into` methods append, they do not write. A write whose
   condition may refuse goes in through `WriteSet.AddGuarded` together
   with the error that refusal means, because the handler is gone by the
   time the answer is known. One such write per set: a batch reports one
   bit.
3. On handler success: `Store.Apply` sends the whole set plus one
   `audit_log` row (`audit.WriteFor`) as ONE `objstore.GuardedBatch`, so
   either both land or neither does — the guarantee the transaction gave,
   on an interface that has none.
4. On handler error, or a refused/failed batch: nothing was written, and
   one `audit_log` row (outcome=`error`) goes in alone via `audit.Emit`.
   A refused batch answers with the handler's own declared error, status
   included.

Read-only actions emit slog only; no DB row. Forwarder resources
(`Store == nil`) skip the batch + audit dance — the downstream daemon
writes the row. Field schema:
[`specs/5/I-tool-call-logging.md`](../specs/5/I-tool-call-logging.md).
Table: [`specs/5/I-tool-call-logging.md`](../specs/5/I-tool-call-logging.md).

## Adding a resource

1. Declare the SHAPE once in `resreg/resources/<name>.go` — the catalog
   registration (`Name`, `Table`, `RowType`, `PKFields`, `Endpoints`,
   `MCPDoc`/`MCPArgs`/`MCPNames`), with its name added to the const block
   in `resreg/resources/names.go`. In your daemon, obtain it with
   `r := resreg.Mount(resources.<X>Name)` and set only `Authz`, `Handler`,
   `Store` and the injected `Gate`. Never build a `resreg.Resource` literal
   there — that is a second declaration of the same resource, and
   `resources/name_source_test.go` fails on one.
   A resource's `Name` is its wire identity (`/v1/<name>` + the MCP tool
   prefix); it is spelled in `names.go` and nowhere else. `Mount` panics on an
   unregistered name, so a resource served but absent from every catalog
   stops the binary at boot.
2. Implement one `Handler` that switches on `x.Action`. BUFFER a mutation
   on `x.Writes` when `Store` is set — read first, decide, hand the writes
   over; the adapter sends them.
3. Wire from `main.go`: `resreg.RegisterREST(mux, r, build)` and
   `resreg.MCPTools(srv, r, callerFor, visible)`.

`proxyd/resource.go` is the canonical store-backed example;
`webd/routes_mcp.go` is the canonical forwarder example.
