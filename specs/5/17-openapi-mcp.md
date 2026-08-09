---
status: shipped
depends:
  [1-auth-standalone, 5/E-routd, 8-yaml-manifests, specs/5/32-acl-unified]
---

# specs/5/17 — one handler, two faces: MCP for the agent, REST for humans

> **DECISION.** Every cold-tier management resource is authored **once** as
> one in-process `resreg.Resource` — logic, tx, audit, and arg-derivation in
> a single handler — and wears **two faces from that one handler**: the
> agent's **MCP tools** (derived by `resreg.deriveMCPTools`) reached over
> the in-container per-folder socket, and the human/external **REST**
> `/v1/*` surface whose `/openapi.json` is annotated (`x-mcp-when`) so
> browsers, CLIs, and external tools get a self-describing doc. resreg owns
> the plumbing and tool-spec generation but carries **zero auth policy** —
> authorization is an **injected `Gate`** the mounting daemon binds per
> surface. Hot-tier agent actions (`reply`, `send`, `inspect_*`) stay
> MCP-only: there is no operator REST resource to mirror.

The mechanism ships. Resource-by-resource adoption is
[`5/16`](16-mcp-rest-unification.md) — **16 rolls it out, 17 is the
mechanism**; migration steps live there, not here.

## The model: two tiers, by design

- **Cold-tier (operator config)** — `routes`, `acl`, `groups`, `secrets`,
  `scheduled_tasks`, `network_rules`, `web_routes`, `route_tokens`,
  `onboarding_gates`, `proxyd_routes`. ONE handler per `(resource,
action)`, two faces. Never a second hand-authored tool list.
- **Hot-tier (agent runtime)** — `reply`, `send`, `like`, `delete`, `post`,
  `diary`, session control (`fork_topic`, `engage`, `disengage`,
  `reset_session`, `inject_message`), inspect (`inspect_routing`,
  `inspect_tasks`, `inspect_session`). **MCP-only by design**, authored in
  `ipc/ipc.go`. These are agent-to-conversation primitives; an operator
  REST mirror adds nothing — operators do not `reply` to chats. NEVER fold
  them into the cold-tier interop.

## One `MCPDoc`, both faces

A resource's `MCPDoc` (per-action agent-facing prose) is the single source
feeding both faces: `deriveMCPTools` walks the resource's `Endpoints` and
turns each action with an `MCPDoc` entry into a `<resource>.<action>` tool
with args reflected from `RowType`; `openapi.go` folds the same `MCPDoc`
into the emitted doc as the operation `description` plus an `x-mcp-when`
vendor extension.

**Strict, no fallback:** an action with no `MCPDoc` entry gets neither an
MCP tool nor an annotation. That is the way to keep an operation REST-only
— there is deliberately no silent fallback to the SDK-facing `summary`.
OpenAPI 3.1 permits `x-*` on any object, so tooling that ignores
`x-mcp-when` still sees a normal doc.

**This spec owns OpenAPI emission** (moved here from `5/8` 2026-08-04 —
that spec is authoritative for the row-schema half of `resreg.Resource`
instead: `RowType`, `Table`, `PKFields`, `Scope`, `Hooks`). The same
`RowType` reflection that derives MCP tools emits an OpenAPI 3.1 document
per daemon — no `huma`, no `swag`, no codegen (`resreg/openapi.go`).
Every HTTP-serving daemon mounts `GET /openapi.json`, including daemons
owning no resources, so the aggregator page
(`/pub/arizuko/reference/openapi.html`) lists them uniformly. **The
endpoint is public and mounts BEFORE auth middleware** — schemas
describe surface, not data — and is cached for the process lifetime.

**The advertised SET comes from the daemon's mux, not from a list.**
`OpenAPIHandler(daemon, mux)` takes the routing table; `RegisterREST`
mounts each face as a `*restMount` stamped with its `(resource, endpoint)`,
and `MountedResources` keeps only the registry resources that mux resolves
to one of those. A daemon therefore cannot advertise what it does not
mount. Each daemon used to hand over a list of resource names instead,
which drifted three times (BUGS `F21`/`F27`/`F32`, cause closed as `F33`).

Derivation checks mount IDENTITY and must never check path presence: THREE
daemons serve `GET /v1/sessions` over three different tables (authd's
resreg resource, runed's `session_log`, routd's `core.SessionRecord`), and
proxyd answers every path from a `/` catch-all — a path probe would make
those daemons publish schemas they do not own. Because the set is read at
first REQUEST rather than at construction, `/openapi.json` may be mounted
on the mux it documents.

## Caller and Execution

Both faces decode to one surface-agnostic `Caller` and run in one
surface-agnostic `Execution` — a handler never touches `*http.Request` or
`mcp.ToolRequest`. Definitions: `resreg/resreg.go`.

Two contracts inside `Execution`:

- **`Tx` is the adapter↔handler contract.** It is non-nil only for a
  mutating action on a `Store`-backed resource; the adapter opens it, the
  handler mutates, the one audit row lands in the same tx, and any handler
  error rolls it back.
- **The caller resolver runs per call**, never captured at registration, so
  a shared MCP socket cannot confuse one agent's privileges for another's.

Single-sourcing `RowType`+`Endpoints` in one `resreg/resources/<name>.go`
imported by both the CLI and the mounted handler is what makes the
"drift is structurally impossible" claim actually hold. Without it the
CLI-manifest declaration and the mounted handler (`routd/*_resource.go`)
can diverge and OpenAPI emits a phantom fixed-CRUD convention instead of
the real `Endpoints` — the deferred "one owner + federation" work in
[`5/16`](16-mcp-rest-unification.md).

## Auth model — resreg holds no policy; each surface injects its gate

Authorization is an **injected `Gate`** on the `Resource`: resreg calls it,
the mounting daemon binds it. The `Gate` DEFAULTS to `auth.Authorize`, so
the operator REST path needs no per-daemon edit; the agent socket overrides
it with a folder-bound gate (`routd/agent_gate.go`).

| Surface                         | Identity source                                      |
| ------------------------------- | ---------------------------------------------------- |
| Human / REST (`/v1/<name>`)     | `Authorization: Bearer <jwt>`, proxyd-stamped folder |
| Agent / MCP (per-folder socket) | the in-container socket, folder-bound at spawn       |

**There is exactly one authorization evaluator.** `auth.Authorize`
(`auth/authorize.go:25`) matches ACL rows over the caller's expanded
principal set, deny-wins, and returns false when nothing matches — no
fallback, denied loud. Both surfaces reach the same substrate; what differs
is the identity source and the target the gate binds, not the decision
procedure.

<!-- CORRECTED 2026-08-02: earlier revisions of this spec described the
     agent surface as "tier-based by design", with auth.AuthorizeWith
     falling back to grants.DeriveRules + CheckAction under
     AuthorizeOpts{Folder, WorldFolder, Tier}. That path is DELETED — the
     `grants/` package no longer exists, and auth/authorize_test.go
     `TestAuthorizeWith_NoTierFallback` pins its removal (5/33 phase e).
     Depth-derived tiers are gone; do not reintroduce the framing. -->

A platform token is an ES256 JWT signed by `authd`, the **sole signer**
([`1-auth-standalone.md`](1-auth-standalone.md)). Every daemon verifies
offline against `authd`'s JWKs; none mint their own. The namespaced folder
claim `arz/folder` is treated opaquely by `auth/` (surfaced via
`Identity.Extra["folder"]`), keeping that package folder-agnostic.

Scope vocabulary (operator surface): `<resource>:<verb>[:own_group]`.
`:own_group` scopes to the caller's folder subtree; `<resource>:*` covers
all verbs on a resource. There is **no `*:*` global wildcard** — operators
carry the enumerated list. The scope string is minting shorthand over ACL
rows, not a second authorization path.

**Folder containment is per-resource, not a resreg primitive.** A
folder-scoped caller MUST NOT act cross-folder, so a handler resolving a
`jid`/`folder`/`run_id` param binds it to the caller's folder: a `jid`
resolves to its routing-target folder, a `run_id` to its run's folder (a
cross-folder run reads as absent — 404, not leaked), a bare `folder` is
contained directly. The shipped `proxyd_routes` exemplar carries NO
precedent for this — it is a GLOBAL resource on a `**` ACL row with no
folder-bearing param — so containment is folded into each resource's
handler as `5/16` reaches it. `POST /v1/messages` stays cross-folder by
design (one adapter routes many folders).

## Visibility is a separate view, outside the per-call gate

Which tools a caller even SEES in `tools/list` is orthogonal to whether a
given call is allowed. Visibility is `auth.EffectiveActions`
(`auth/authorize.go:86`): a tool is listed iff the caller holds an allow
row for its action at ANY scope; a lattice grant (`*`, `mcp:*`, `admin`)
reveals every `mcp:` tool. Deny rows are scope-specific and do NOT hide a
tool — the per-call `Authorize` still enforces them.

A naive `resreg.MCPTools` mount that `AddTool`s unconditionally would WIDEN
visibility, so agent-tool registration stays driven by the socket's
`Visible` predicate (`resreg/resreg.go:422`), never by resreg policy. This
is orthogonal to the injected gate and to the per-call firewall of
[`6/12`](../6/12-mcp-firewall.md).

## One owner per table; in-process handler vs cross-daemon forwarder

Each table lives in exactly one daemon's DB; that daemon serves its `/v1/*`
face. `Resource.Store` picks the dispatch shape:

- **In-process handler (`Store` non-nil)** — the owning daemon serves its
  OWN table. resreg opens the tx, the handler mutates, and the single
  `audit_log` row lands in the SAME tx. The agent-MCP facade is this shape
  too — a generated thin front over the same in-process handler, NOT an
  HTTP hop — so "one audit row in the mutation's tx" holds for the agent
  exactly as for REST.
- **Cross-daemon forwarder (`Store: nil`)** — the table lives in ANOTHER
  daemon. resreg skips the local tx, authorization, and audit; the handler
  HTTP-forwards the caller's token to the owner, which runs the in-process
  handler and writes the one audit row (no double-log). `proxyd_routes`'s
  MCP face on webd is this shape.

Never conflate them — the forwarder is for genuinely cross-daemon resources
only. The owner is resolved by compose service naming (`<DAEMON>_URL`),
NEVER a lookup registry: identity is configured, not derived (CLAUDE.md).
The per-daemon ownership table lives in
[`5/16`](16-mcp-rest-unification.md).

## Resource name = wire identity

The resreg `Name` becomes `/v1/<name>` AND the name every other surface
composes on. Two daemons NEVER share a name, so `(name, action)` identifies
one handler platform-wide and URL or handler-function renames don't break it.

**`(name, action)` is the identity; its spelling is per-surface.** There is no
single composed string, and pretending otherwise cost this spec a wrong claim
about its own emitter (BUGS `F28`):

| Surface         | Spelling                | Example                       |
| --------------- | ----------------------- | ----------------------------- |
| REST path       | `/v1/<name>`            | `/v1/routes`                  |
| OpenAPI         | `operationId`           | `list_routes`                 |
| MCP tool        | `<name>.<action>`       | `routes.list`                 |
| `audit_log`     | `resource=` + `action=` | `resource=routes action=list` |
| `acl` grant row | `mcp:<tool name>`       | `mcp:add_route`               |

**`operationId` is `<action>_<name>`, verb-first and underscore-joined**
(`resreg/openapi.go` `endpointOp` + `conventionPaths`). OpenAPI 3.1 §4.8.10.1
says tools MAY use `operationId` to identify an operation "therefore, it is
RECOMMENDED to follow common programming naming conventions" — client
generators turn it into a method name, and `.` is not an identifier character
in Go, Java, Python, or TypeScript. A dotted id would be mangled by the
generator, so the dot cannot survive to the caller anyway. Verb-first also
matches OpenAPI's own examples (`listPets`, `showPetById`).

The MCP face is where the dotted form belongs — dots ARE conventional in tool
names — and even there it is only `deriveMCPTools`' DEFAULT: every folded
resource overrides it via `MCPNames` to the flat name the live agent calls
(`add_route`, `list_acl`, `schedule_task`), which is also what `acl` grant rows
carry as `mcp:<tool>`. `audit_log` never composes at all: `resource=` and
`action=` are separate columns, which is what keeps
`grep 'resource=groups action=register'` returning the work from either face.

`proxyd_routes` (proxyd's reverse-proxy table) and `routes` (routd's
message-routing table) are two distinct resources with distinct names;
conflating them cost a live drift (renamed `aab3487a`).

## Audit contract

Every state-changing handler MUST write exactly one `audit_log` row in the
SAME transaction as the mutation; if the audit write fails, the mutation
rolls back. Read-only handlers emit slog only. Field shape: `caller=<sub>
resource=<name> action=<verb> surface=<mcp|rest> target=<folder>
result=<allowed|denied|error>`.

`action` is the stable correlator — one `grep 'resource=routes action=add'`
returns the work whether it arrived via the agent's `add_route` tool,
`POST /v1/routes`, or `arizuko route add`. Forwarders skip the row;
the downstream daemon writes it. Field schema:
[`I-tool-call-logging.md`](I-tool-call-logging.md).

## Anti-patterns — what should NOT be a resource

The rule: **user-initiated, audit-worthy, allow/deny-shaped → resreg.
High-rate side effect of normal operation → not.**

- **Inbound message ingestion** — per-message hot path; poll loops write
  `messages` rows directly. (`inject_message` for synthetic sends IS a tool
  — audited, low-volume.)
- **Cost-log writes** — per-Claude-call, direct store write. Not an
  operator action.
- **Streaming surfaces** — slink stream, agent live output. Not CRUD; SSE
  sits next to resreg ([`J-sse.md`](J-sse.md)), never inside it.
- **Auth session creation** — minted by `authd`. Substrate, not a tool.
- **Migrations** — file-driven, run by the owning daemon at startup.
- **runed run-control** (`/v1/runs*`) — REST-only BY DESIGN: the internal
  routd→runed execution control plane. Agents never spawn or kill runs.
  runed exposes no resreg resource; the absence is correct, not a
  uniformity gap.

## Orthogonal specs (don't fold in)

- [`5/16`](16-mcp-rest-unification.md) — the adoption program.
- [`5/8`](8-yaml-manifests.md) — the same cold-tier resources as
  declarative YAML. A different front on the same tables.
- [`6/12`](../6/12-mcp-firewall.md) — per-call allow/deny filtering between
  agent and MCP server; orthogonal to both the injected gate and the
  visibility view.

## Acceptance

- A cold-tier resource is served by exactly one `resreg.Resource`; its MCP
  tool and its `/v1/<res>` REST endpoint + OpenAPI entry all derive from
  that one handler — no hand-authored `MCPTools` list.

  **One carve-out on the path: `scheduled_tasks` serves `/v1/tasks`.**
  `timed` calls `GET /v1/tasks` (`timed/dash.go:82`) and the fire loop shares
  the prefix (`/v1/tasks/due`, `/runlog`, `/{id}/reschedule` —
  `timed/split.go:205,219,230`), both across the container boundary, and the
  two daemons restart independently. Renaming half of one control surface
  against a running fleet is the drift, not the fix. The name is still the wire
  identity everywhere else — MCP tool prefix, `operationId`, audit `resource=`.
  A new resource does NOT get this exemption: it serves `/v1/<name>`.

- `GET /openapi.json` carries `description` + `x-mcp-when` for each action
  with an `MCPDoc` entry; an action without one has no MCP tool and no
  annotation but stays reachable via REST.
- Both gates deny cross-folder — `atlas/support` cannot touch a target
  under `rhias/*` — through the same handler.
- Hot-tier tools have no REST twin and no OpenAPI entry.

Deferred: short token TTL (1h) + a revocation table for long-lived
dashd-issued keys; pagination harmonization (MCP arrays vs REST cursor
pagination), left per-resource for now.
