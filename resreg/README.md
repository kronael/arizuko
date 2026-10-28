# resreg

resreg implements the resource registry, and a daemon uses it by declaring a resource once and mounting it on REST and MCP. The declaration is a typed `Resource` literal with a `RowType` struct. resreg serves it through REST, MCP, OpenAPI and YAML with one `Handler` per resource action. resreg owns the handler adapters, the batched write and audit row, argument derivation and MCP tool-spec generation. It takes one injected authorization `Gate` per surface. It decides no auth policy of its own, so it is not a second authz server.

## One handler, two faces

Every cold-tier resource is written once and served twice: REST for people and external tools, MCP for the agent. Both faces run one handler, so they cannot drift, and one audit row lands in the batch of the mutation. The hot-tier agent actions have no REST twin, because an operator does not `reply` to a chat.

<!-- twin:twofaces -->

```
┌───────────────────────────────────────────────────────────────────────────┐
│ resreg.Resource, declared once in resreg/resources/<name>.go:             │
│ Name, Table, RowType, PKFields, Endpoints, MCPDoc, MCPArgs                │
│ the daemon takes it by resreg.Mount and sets Authz, Handler, Store, Gate  │
└──────────────┬─────────────────────────────────────────────┬──────────────┘
               ▼                                             ▼
┌──────────────────────────────────┐ ┌──────────────────────────────────────┐
│ REST face: RegisterREST          │ │ MCP face: MCPTools                   │
│ /v1/<name>, one op per Endpoint; │ │ deriveMCPTools: one tool per         │
│ /openapi.json from the mounts    │ │ Endpoint that has an MCPDoc,         │
│ caller: the bearer's sub, scope  │ │ <name>.<action> or MCPNames          │
│ and folder claim                 │ │ caller: the turn's folder and sender │
│ Gate: a bearer-scope check, or   │ │ Gate: AuthorizeTurn on the target;   │
│ Authorize on <name>:<action>     │ │ target-folder resources run it in    │
│ target-folder resources add      │ │ containFn behind agentAllowGate      │
│ containFn, one per resource      │ │                                      │
└──────────────┬───────────────────┘ └──────────────────┬───────────────────┘
               └────────────────────┬───────────────────┘
                                    ▼
┌───────────────────────────────────────────────────────────────────────────┐
│ invoke: Authz ► Gate ► Handler ► one GuardedBatch with the handler's      │
│ writes and one audit_log row; a successful read writes no row             │
└───────────────────────────────────────────────────────────────────────────┘
┌───────────────────────────────────────────────────────────────────────────┐
│ beside it: a forwarder (Store nil: no gate, no row here) for secrets on   │
│ routd and proxyd_routes on webd; the hot tier, MCP-only in ipc/ipc.go:    │
│ reply, send, like, inspect_*                                              │
└───────────────────────────────────────────────────────────────────────────┘
```

<!-- /twin -->

- **The identity is `(name, action)`, and each surface spells it.** REST is `/v1/<name>`. The OpenAPI `operationId` is `<action>_<name>`, with the verb first and an underscore, because client generators mangle a dotted id. The MCP tool is `<name>.<action>` unless `MCPNames` sets a flat name for the action (`deriveMCPTools` in `resreg.go`). The audit row carries the two as separate columns.
- **`MCPDoc` is the switch for MCP.** An action with no `MCPDoc` entry gets no MCP tool and no `x-mcp-when` annotation.
- **`MCPOnly` is the switch for REST.** An endpoint marked `MCPOnly` gets no REST route and no OpenAPI operation, and `deriveMCPTools` still derives its tool. So each resource keeps one endpoint list.
- **A handler binds the folder it receives.** A handler that takes a `jid`, `folder` or `run_id` binds it to the folder of the caller. It refuses a value outside that folder. It answers 404 where the value would reveal that a resource exists. `POST /v1/messages` is cross-folder by design, because an adapter posts for every chat it serves.
- **`secrets` is a forwarder.** `invoke` copies every argument into `audit_log`, and the redaction pattern misses a bare `value` field. A store-backed `secrets` would write the plaintext into its own audit row. So the handler calls the sealing writer, which writes a row that is safe for values.
- **A resource with a `RowType` names a table.** `initMeta` in `engine.go` panics on an empty `Table`. So a queue kept in files cannot be a resreg resource.

## The engine reads one row type

`engine.go` drives reads and writes, YAML parse and emit, and OpenAPI schema generation from the reflection of `Resource.RowType`. The `db:` tag of a struct field names its attribute. The OpenAPI schema takes the property name from the `json:` tag, or from the `yaml:` tag when no `json:` tag exists. `resreg/resources/` declares one Go file per cold-tier resource: a `Row` struct and an `init()` block that calls `resreg.Register`. `resources/names.go` spells every registered name once, and that file is the list.

`arizuko.yml` is a strict YAML stream with one resource document per owner subsystem. The shared `Manifest` also carries package metadata (`name`, `brand`, `tagline`, `install`) and keyed `env` requirements. It carries no package selection: the `installed_packages` rows select packages. Unknown keys refuse; `SubsystemOf` prevents one resource document from mixing owners. `arizuko package sync` uses the same resource handlers and honors a supplied `checksum`. Only actual service packages provision shared instance environment values.

`Checksum` and `ManifestDigest` are two hashes for two jobs. `Checksum` is the compare-and-swap token: a content hash of the live projection, computed again at read time. `ManifestDigest` is the sha256 of the bytes of the manifest file. It goes only into the audit row of the apply, to correlate the row with the file.

## Every resource is one objstore entity

A resource with an owner subsystem stores its rows as `objstore` items. `Resource.EntitySortKey` declares the shape: it names the `RowType` fields whose values make the sort key of the item. `Table` is the entity, every other `db:` tag is an attribute name, and `Resource.DB` is the partition. `resources.TestEveryOwnedResourceIsEntityShaped` fails when an owned resource declares no `EntitySortKey`. `ScanAll` is the single row reader. A resource with a `RowType` and no owner subsystem is catalog-only: it drives `/openapi.json` and the CLI manifest, and no apply reaches it.

The coordinate is a list, because several resources have no single-field identity. `network_rules` is keyed by folder and target, so two folders can hold a rule for one target. `installed_packages` is keyed by folder and name. `acl`, `acl_membership`, `routes` and `secrets` also use composite coordinates. `Resource.EntityKeySep` joins the fields, and the separator in use is `store.KeySep`, the ASCII unit separator 0x1F. No value of a joined field may carry the separator. A write that does is refused. The write is the only end that can see two rows join onto one key. The read refuses a stored key that does not split into as many parts as the coordinate has. An empty part is a coordinate: `folder: ""` spells an instance-wide row.

The partition is not declared per resource. It is `Resource.DB`, the owner subsystem, because every read of the engine is per subsystem. `SubsystemRoutd` and `SubsystemOnbod` are the owner names of `store`, so the storage layer of a daemon writes items at the coordinate that the engine reads.

The sort key is separate from `PKFields`, because they answer different questions. The sort key is the coordinate of the item. `PKFields` is the manifest identity that `Diff` matches on. `PKFields` must name every field of the sort key, and `Register` refuses a declaration that does not. A narrower identity would file two items under one identity, and `Diff` would report whichever item it read last.

The object path refuses these declarations at `Register`, which is process start. Each refusal has a reason.

| declaration                                                                       | why it is refused                                                                                                                                 |
| --------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Hooks.AfterScan` or `Hooks.ColumnOverride`                                       | an override is a SQL expression with no column to name, and a declared codec decodes after a scan                                                 |
| a `RowFilter` on a field of the coordinate                                        | a filter is a `Where` over attributes and the coordinate is the key; a key predicate reaches a leading fragment and never one field in the middle |
| an attribute that is not a string, an int, or a field with a declared encoding    | every attribute value is text, so a bool, a slice or a map declares its encoding in the `db:` tag (`codec.go`)                                    |
| `PKFields` that does not name every field of the coordinate                       | the manifest identity must be at least as fine as the coordinate, or `Diff` files two items under one identity                                    |
| a sort field that is not a string and declares no `pad` encoding                  | a coordinate is text compared as text, so "10" sorts before "2"                                                                                   |
| a composite `EntitySortKey` without `EntityKeySep`, or a separator with one field | the fields would run together into one key, or nothing would use the separator                                                                    |

The object path applies three declarations.

`Hooks.BeforeInsert` fills the declared defaults of a row, for example `effect='allow'` or `status='active'`. `itemFor` runs it on a copy of the row before it renders the item, and it fills a `StampedField` the same way. The hook takes the row and nothing else.

`Hooks.ValidateRows` refuses a row set that an apply must not store. It takes the whole set and not one row. It runs before the delete that precedes the writes, so a refused manifest leaves the subsystem untouched.

`RowFilter` narrows the manifest-visible projection. It is a declared predicate: `objstore.Cmp` terms that the engine passes to `Query.Where`. A delete set is a read, so the same `Where` narrows the scan, the wholesale wipe and the scoped delete. A reader-side predicate could not narrow a write. The terms are a conjunction of `Eq` and `Ne`, so a predicate that needs an `OR` has no form. The answer is an attribute that the writer sets. For example, `acl_membership` excludes the pairing edges with `added_by != 'pairing'`, and `route_tokens` keeps only `kind = 'route'`. `itemFor` writes a filtered attribute even when it is empty. An absent attribute answers no comparison, and a row without it would fall out of its own projection.

A `ScopeSpec` declares the scoped delete. On the object path it is a query plus one guarded delete per item, because an entity has no predicate delete. The declaration decides the query at `Register`, because the place where the scope value lives decides what can select on it.

- An attribute is a `Where` term. An empty scope is `Absent`, because an empty string is an absent attribute.
- A sort key is an exact key.
- The first field of a composite coordinate is a `begins_with` on `scope+sep`. It is exact because no value may carry the separator. This is what makes `network_rules` purgeable by folder.
- A scope in the middle of a coordinate is refused, because a key operator reaches a leading fragment.

These deletes run inside the claim of `objstore.Replace`. The delete set is read and then written. Without the claim, a second replace could take the scope in that window. Its deletes would land on a projection that another replace has half replaced. `ApplyOpts.PruneScopes` joins the mentioned scopes, so the cross-subsystem rollback also reaches the folders that a forward apply created. Without scoped deletes, a folder purge would leave egress rules, cron tasks, held actions and web routes behind. The next tenant of the path would inherit them.

A field whose Go type is not text declares its encoding in the `db:` tag after the attribute name, for example `db:"preserve_headers,json"`. The set is closed: `json`, `bool` and `pad`. A resource cannot invent a rendering that only it can read. An unknown name stops the process at `Register`, and so does an encoding on a string or an int field. `pad` renders an int as a ten-digit zero-padded decimal, so byte order and numeric order agree. Only a field of the coordinate may declare it. `routes` uses it for `Seq`.

A `StampedField` is filled by the storage layer, because an entity has no engine between the caller and the item. The layout is `core.TS`, the fixed-width form. The engine compares a stored instant as text, and the fixed width makes byte order and time order the same. `AbsentStamps` names the subset of stamped fields that the writer does not invent, because absence is a value the row states. `groups.updated_at` is one: a group that nobody changed has none. The declaration stays on the resource. It tells an operator manifest that the field is not theirs to set, so `Diff` ignores it and it is not a `create` or `update` argument. A stamped number that a row leaves unset gets the number after the largest that the set already states, in `mintNumbers`. `routes.id` is such a number, because `delete_route(id)` addresses it.

## An apply replaces a subsystem under a claim

`Apply` and `ExportSnapshot` use `objstore.Replace` and `objstore.ReadConsistent` (`objstore/README.md`). `Apply` checks the checksum of the live projection against the checksum in the manifest, unless `force` is set or the apply takes over a claim. It then deletes and puts the rows of each resource that the manifest mentions, in catalog order, and writes one audit row.

**An apply has no pre-image rollback inside one subsystem.** A marker-gated sequence has no transaction. A replace that dies after it has written leaves the subsystem part-applied with its claim held. `arizuko package sync` reports a held claim before it writes.

**Running the same apply again gives the claim back.** The claim records the intent of the apply, `applyClaim`. The intent derives from the document and the scopes that it prunes. So the same apply takes over its own claim and rolls forward. It converges because every delete set is read live from the manifest scopes under the claim. The apply deletes what is there and puts the document over it. The result matches a clean run, whether the dead apply stopped at the first row or the last. An apply of a different document is refused, because its delete set covers only the rows it names. A caller using `Claim.Take` asserts that the interrupted work is not coming back.

A subsystem must be of one shape. `requireEntities` checks this before any write: a mixed subsystem would leave the operator unable to say which half an apply landed in.

A daemon prepares its database with `resreg.EnsureSubsystem`, beside the `CreateEntity` calls it already makes. The call creates the entity behind each resource, the entity of the version marker, and the marker. It creates nothing for a subsystem that no entity-backed resource names, so the set follows the registrations and not a second list.

`arizuko package`, `plan`, `get` and `export` are the operator CLI over this engine. Their flags are in `cmd/arizuko/README.md`. The CAS is `resreg.Checksum`, a content hash of the canonical export projection, so no counter or table exists that a writer could forget to bump.

Two guards wrap every apply. They live in `cmd/arizuko/apply.go` and not in the engine, because both span documents, and the engine sees one document at a time.

- **The missing-group rule.** `KnownFolders` and `ValidateFolderRefs` refuse a manifest before the first write. They refuse it when it names a folder that is no `groups` row in the document set and is not live.
- **Cross-subsystem pre-image rollback.** Two owners share no transaction. So when the second apply fails, the CLI restores the first from a pre-image that `ExportSnapshot` took, and replays it through `Apply` itself.

Resources with `SkipApplyRebuild` are export and diff only. `Apply` never deletes and inserts them. They are `secrets`, `route_tokens`, `installed_packages`, `github_sources`, `gdrive_files`, `invites` and `onboarding`. They still enter the content hash, because it hashes whatever the export projection shows. Their values travel only in the archive (`resreg/archive.go`) and never in a config manifest.

## Which resources are live

- proxyd serves its runtime route table in `proxyd/resource.go`. The resource is store-backed, and its audit row rides the batch.
- webd serves an operator-side MCP forwarder for the route tools in `webd/routes_mcp.go`. The forwarder has `Resource.Store == nil`, and proxyd writes the audit row downstream.
- routd mounts the resources of `routd/*_resource.go` and `routd/pending_actions_http.go`: `acl`, `acl_membership`, `audit`, `chats`, `dead_letters`, `gdrive_files`, `github_sources`, `groups`, `installed_packages`, `network_rules`, `pending_actions`, `route_tokens`, `routes`, `scheduled_tasks`, `secrets` and `web_routes`. Each mounts on the agent MCP socket with the agent `Gate` injected. Where a REST twin exists, the same handler also runs on the operator REST face with a scope and folder `Gate`. The hot-tier tools `reply`, `send` and `inspect_*` stay hand-authored in `ipc/ipc.go` and have no REST twin.
- authd mounts `audit`, `signing_keys` and `sessions`. onbod mounts `invites`, `onboarding_gates`, `onboarding` and `audit`. runed mounts `audit`.

## OpenAPI emission

`OpenAPIHandler(daemon, mux)` in `openapi.go` emits an OpenAPI 3.1 JSON document for **whatever that mux serves**. `MountedResources(mux)` derives the advertised set. `RegisterREST` mounts each face as a `*restMount` that carries its resource and its endpoint. The document describes only the registry resources that the mux resolves to one of those mounts. Request schemas come from the same `RowType` reflection.

**A response body is declared and never inferred.** `Handler` returns `any`, so reflection cannot see what an action answers with. `Resource.Responses` maps an action to the Go type that its handler returns, or to a slice of that type, and `components/schemas` carries that type. An action with no entry documents `"OK"` and no body schema at all. That is a stated gap, which a generated client handles. A wrong struct would be mis-decoded without a signal. The engine never derives the shape from the verb, such as `array<RowType>` for a list or `RowType` for GET and POST, because mounted operations answer with other types. `GET /v1/routes` answers with `[]router.RouteView`. `GET /v1/tasks` answers with `[]core.Task`, which has no json tags, so the properties use Go field names. The route-token mints answer with `{token,jid,url}`. The mutations of onbod answer with `{"ok":true}`.

The declared type is the one declaration, and the handler is its second reader. The handler constructs that same struct, so a renamed field moves the document and the wire together. Only a handler that returns a different type can still drift. `resregtest.AssertDocumentedResponse` catches that case by serving a real request, and `routd/openapi_response_test.go` runs it over the mux of routd.

A daemon passes no resource list, so it cannot advertise what it does not mount. The check is on identity and not on path presence. Three daemons serve `GET /v1/sessions` over three different tables. proxyd answers every path from a `/` catch-all. So a path probe would document the schemas of other daemons. Hand-rolled `mux.HandleFunc` mounts and catch-alls are not `*restMount` and drop out of the document.

The document is built on the first request and not at construction. So `/openapi.json` may be mounted on the mux that it documents. The document is then cached for the lifetime of the process. The path is public, so mount it before auth. resreg uses no `huma`, no `swag` and no codegen. routd, runed, authd, timed, onbod, webd, proxyd and dashd all mount it. The aggregator landing page is `/pub/arizuko/reference/openapi.html`.

`OpenAPI(daemon, baseURL, resources)` is the underlying emitter and takes resources directly. Tests use it with `resregtest.Mounted(...)`. It emits one operation per declared `Endpoint` and nothing else. A resource with no `Endpoints` contributes only its schema. No `/v1/<name>` operation is invented from a CRUD convention, and `resources.TestEveryResourceDeclaresEndpoints` keeps that case from arising.

MCP tools come from `deriveMCPTools` over the `Endpoints` whose `Action` has an `MCPDoc` entry. The args of each tool are reflected per action, through `MCPArgs`, so a read-one tool does not carry the create-body fields. The `description` of each such operation in OpenAPI carries the same `MCPDoc` prose and the `x-mcp-when` annotation. `proxyd_routes` rides this path end to end. It exports a `redirect_to` field, a redirect target as an alternative to `backend`. Its REST endpoint set is single-sourced as `resources.ProxydRoutesEndpoints`, the one list that both `proxyd` (`proxyd/resource.go`) and the `webd` MCP forwarder (`webd/routes_mcp.go`) register. So the two faces cannot drift.

## Surface

- `RegisterREST(mux, r, build)` emits HTTP handlers for every endpoint that the resource declares, except the `MCPOnly` ones. `build(*http.Request) (Caller, error)` resolves the identity per request.
- `MCPTools(srv, r, callerFor, visible)` emits the matching MCP tools. The engine calls `callerFor` on every call and never at registration, so privilege confusion in shared MCP servers cannot happen. `visible(name)` gates which tools the caller may see in `tools/list`. routd passes `auth.EffectiveActions`, which asks whether the caller holds the action at any scope. A nil `visible` shows all tools.

## Types

- `Resource{Name, Endpoints, MCPDoc, MCPArgs, MCPNames, Authz, Gate, Handler, Store, RowType, Table, PKFields, Responses, EntitySortKey, EntityKeySep, Scope, Hooks, DB, RowFilter, SkipApplyRebuild, StampedFields, AbsentStamps}` is one literal per resource. `Gate` is the injected authorization decision. A nil `Gate` selects the operator `defaultGate`, and the agent socket overrides it with its own gate. `deriveMCPTools` derives the MCP tools from `Endpoints`, using `MCPDoc` for the prose of each action and `MCPArgs` for its args. No hand-authored `MCPTools` list exists.
- `Action` is a short verb constant: `list`, `get`, `create`, `update`, `delete`, or a resource-specific verb. `Action.Mutates() bool` classifies read against write.
- `Caller{Sub, Folder, Claims}` is the principal, the same on both surfaces. `Claims` carries the JWT claims that the ACL row predicates match, for example `operator=1`. `ScopeClaimKey` carries the space-delimited scopes of the bearer to the injected `Gate`.
- `Execution{Caller, Action, Resource, Args, TurnID, RequestID, SourceIP, Surface, Writes}` is everything a handler needs. `Writes` is non-nil only for a mutating action on a store-backed resource.
- `Handler func(ctx context.Context, x Execution) (any, error)` is the one code path that both surfaces invoke.

## Authorization

resreg owns the plumbing and never the policy. `Resource.Authz(c, action, args)` derives `(scope, params)` from the request. That is argument extraction and not an allow or deny decision. The injected `Resource.Gate(x, scope, params)` makes the decision.

- A nil `Gate` selects `defaultGate`, the operator gate: `auth.Authorize(Store, caller, "<Name>:<action>", scope, params)` over the ACL rows, which `auth/README.md` describes. That is a scope and ACL match with no tier. `proxyd_routes` mounts it. The operator REST faces of routd, onbod and authd inject a bearer-scope `Gate` instead. It checks `<name>:<verb>` or `<name>:<verb>:own_group` and contains the request by the folder claim of the token.
- The daemon that mounts a resource on the agent socket, routd, injects `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)`. That call reaches the same `auth.Authorize` over the same `acl` rows. The identity source differs, the socket folder against the JWT, but the policy does not. It is the same handler with a different gate.

Forwarder resources (`Store == nil`) skip the gate, because the downstream daemon authorizes. An error from `Authz` ends the call before the gate, for example a validation failure with status 400. No parallel predicate machinery exists, and the ACL rows stay the source of truth.

## A mutation and its audit row land together

A mutating action on a store-backed resource (`Resource.Store != nil`) hands its writes over and does not perform them. The adapter works in four steps.

1. It puts a fresh `store.WriteSet` on `Execution.Writes` and invokes `Handler`.
2. The handler reads what it needs and buffers the writes it decided on. The `store.…Into` methods append and do not write. A write whose condition may refuse goes in through `WriteSet.AddGuarded` together with the error that the refusal means, because the handler is gone when the answer arrives. A set holds one such write, because a batch reports one bit of refusal.
3. When the handler succeeds, `Store.Apply` sends the whole set and one `audit_log` row (`audit.WriteFor`) as one `objstore.GuardedBatch`. Either both land or neither does.
4. When the handler fails, or the batch is refused or fails, nothing was written. One `audit_log` row with outcome `error` goes in alone through `audit.Emit`. A refused batch answers with the declared error of the handler, status included.

Every call gets one slog line (`logCall`). A successful read writes no row. A denied or failed read does, so a refused read leaves a trace. A forwarder resource skips the batch and the audit row, because the downstream daemon writes the row. The field schema is in `audit/README.md`.

## Adding a resource

1. Declare the shape once in `resreg/resources/<name>.go`: `Name`, `Table`, `RowType`, `PKFields`, `EntitySortKey`, `Endpoints` and `MCPDoc`, `MCPArgs` and `MCPNames` where needed. Add the name to the const block in `resreg/resources/names.go`. In the daemon, take the resource with `r := resreg.Mount(resources.<X>Name)` and set only `Authz`, `Handler`, `Store` and the injected `Gate`. Never build a `resreg.Resource` literal in the daemon. That is a second declaration of the same resource, and `resources/name_source_test.go` fails on it.
   - The `Name` of a resource is its wire identity: `/v1/<name>` and the MCP tool prefix. `names.go` spells it and nothing else does.
   - `Mount` panics on an unregistered name, so a resource that a daemon serves but no catalog holds stops the binary at boot.
   - Registration panics on an empty `Table` for a resource with a `RowType`, so state kept in files cannot be a resreg resource.
   - One name is shared on purpose: routd, runed, authd and onbod each mount `audit`. That keeps the rule, because all four serve one declaration in `resources/audit.go` over one row shape, `audit.Row`, each over its own `audit_log`. The declaration has `GET /v1/audit` and `query_audit`, no write face and no `/{id}`. One path on four owners lets dashd fan one request out and merge the answers.
2. Implement one `Handler` that switches on `x.Action`. When `Store` is set, buffer a mutation on `x.Writes`: read first, decide, then hand the writes over. The adapter sends them.
3. Wire the resource from `main.go` with `resreg.RegisterREST(mux, r, build)` and `resreg.MCPTools(srv, r, callerFor, visible)`.

`proxyd/resource.go` is the canonical store-backed example, and `webd/routes_mcp.go` is the canonical forwarder example.

Some things are not resources, because making one would add a face that nobody may use. They are inbound ingestion (`POST /v1/messages`), cost reads, SSE streams, auth session creation and store setup. The run control of runed is REST-only by design.
