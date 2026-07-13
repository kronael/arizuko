---
status: shipped
depends: [1-auth-standalone, 5/E-routd, 8-yaml-manifests, specs/4/9-acl-unified]
---

# specs/5/17 — one handler, two faces: MCP for the agent, REST for humans

> Every cold-tier management resource is authored **once** as one
> in-process `resreg.Resource` — logic, tx, audit, and arg-derivation in
> a single handler — and wears **two faces from that one handler**: the
> agent's **MCP tools** (derived from the resource's `Endpoints`+`MCPDoc`
> by `resreg.deriveMCPTools`, flat names via `MCPNames`, SHIPPED
> `da0bc6a8`/`443dc4d3`) reached over the in-container per-folder socket,
> and the human/external **REST** `/v1/*` surface whose `/openapi.json`
> is **annotated** (`x-mcp-when`, SHIPPED `52f92004`) so browsers, CLIs,
> and external tools get a self-describing doc. resreg owns the plumbing
> and the tool-spec generation but carries **zero auth policy**:
> authorization is an **injected `Gate`** the mounting daemon binds per
> surface — operator REST defaults to scope-based `auth.Authorize` (no
> tier); the agent socket injects a tier-aware `mcp:`+`db.Authorize`
> gate. Hot-tier agent actions (`reply`, `send`, `inspect_*`) stay
> MCP-only — no REST resource to mirror. Absorbs the former `5/5`
> reality-record; [`5/16`](16-mcp-rest-unification.md) is the rollout.

**Mechanism (`shipped`); adoption tracked in `5/16`:** the mechanism ships — `deriveMCPTools`,
`MCPNames`, `x-mcp-when`, the injected `Gate` seam (`resreg.invoke` now
calls `Resource.Gate`, defaulting to the operator `auth.Authorize`), and
truthful `Endpoints`-driven OpenAPI (`7c14efd6`). `proxyd_routes` remains
the cross-daemon forwarder exemplar (`Store: nil`, webd operator socket,
`**` ACL row, global — no folder-containment). The
[`5/16`](16-mcp-rest-unification.md) rollout has migrated the five
in-process cold-tier resources (`routes`, `acl`, `scheduled_tasks`,
`network_rules`, `web_routes`) onto one `resreg.Resource` each: the agent
socket injects a `mcp:`+tier `Gate` (the tier-default grant a folder agent
needs) and per-resource folder-containment lives in each handler/gate.
The dashd tool-browser renders the migrated facade tools (`df9ebad3` +
`d5023c60`). `groups` and `route_tokens` are folded on the AGENT face only
(`ef3d4f99`, `96ca858f`) — `route_tokens`' REST face is still hand-rolled
(`routd/server.go handleToken*`) and there is NO `/v1/groups` REST twin;
onbod's `onboarding_gates` (`4bd09532`) and `/v1/invites` (`154cd17f`) REST
faces are folded. Remaining: the route_tokens REST fold + groups twin; the
AGENT invite tools (`invite_create`/`list`/`revoke`) stay hand-rolled —
their caller-bound issuer + list-then-delete revoke-ownership don't fit a
single resreg forward without onbod's DELETE gaining an `issued_by` scope
(see BUGS.md); and one-owner + federation.

## The model

Two tiers, by design — not a pending migration:

- **Cold-tier (operator config)** — `routes`, `acl`, `groups`,
  `secrets`, `scheduled_tasks`, `network_rules`, `web_routes`,
  `route_tokens`, `onboarding_gates`, `proxyd_routes`. ONE handler per
  `(resource, action)`, wearing two faces: the **agent** calls the
  derived **MCP tools** over its MCP socket; the **human** calls the
  **REST** `/v1/*` endpoints (OAuth-gated browser, external CLIs). Both
  faces come from the one `resreg.Resource` — `deriveMCPTools` renders
  the MCP tools from its `Endpoints`+`MCPDoc`, `openapi.go` emits the
  annotated REST doc from the same `MCPDoc`. Never a second
  hand-authored tool list.
- **Hot-tier (agent runtime)** — `reply`, `send`, `like`, `delete`,
  `post`, `diary`, session control (`fork_topic`, `engage`, `disengage`,
  `reset_session`, `inject_message`), inspect (`inspect_routing`,
  `inspect_tasks`, `inspect_session`). **MCP-only by design**, authored
  in `ipc/ipc.go`. These are agent-to-conversation primitives; an
  operator REST mirror adds nothing (operators don't `reply` to chats).
  NEVER fold them into the cold-tier interop.

Both cold-tier faces render from the one resource literal. What differs
is the identity source, the transport, AND the injected authorization
gate — resreg supplies the handler; the mounting daemon supplies the
gate (see "Auth model" below).

## One `MCPDoc`, both faces

A resource's `MCPDoc` (per-action agent-facing prose) is the single
source that feeds both faces:

- **MCP face** — `deriveMCPTools` walks the resource's `Endpoints`; each
  endpoint whose `Action` has an `MCPDoc` entry becomes one tool named
  `<resource>.<action>`, with that prose as its description and args from
  `RowType` reflection (SHIPPED `da0bc6a8`). No hand-authored tool list.
- **REST doc face** — `openapi.go` folds the same `MCPDoc` into the
  emitted `openapi.json` as the operation `description` +
  `x-mcp-when` vendor extension (SHIPPED `52f92004`), so the published
  doc is self-describing for browsers, CLIs, and external tools — and can
  seed the MCP derivation without a second authoring pass.

OpenAPI 3.1 allows `x-*` extensions on any object; tooling that ignores
`x-mcp-when` sees a normal OpenAPI doc. An action with no `MCPDoc` entry
gets neither an MCP tool nor an `x-mcp-when` annotation — strict on
purpose (arizuko's "strict, not magical" rule; no silent fallback to the
SDK-facing `summary`), the way to keep an operation REST-only.

## Caller and Resource shape

Both faces decode to one surface-agnostic `Caller`, and the handler runs
in one surface-agnostic `Execution` — never touching `*http.Request` or
`mcp.ToolRequest`:

```go
type Caller struct {
    Sub    string             // "user:abc" | "agent:atlas/main" | "key:k_42"
    Folder string             // from Identity.Extra["folder"]; auth/ stays folder-agnostic
    Scope  []types.Scope      // capability list; scope-match, no tier
    Claims map[string]string  // JWT claims for ACL row predicates
}

// Tx is non-nil only for a mutating action on a Store-backed resource;
// forwarders and read-only paths see Tx == nil.
type Execution struct {
    Caller    Caller
    Action    Action    // "list" | "get" | "create" | "update" | "delete"
    Resource  string
    Args      Args
    TurnID    string    // X-Turn-Id (REST) or _meta.turn_id (MCP)
    RequestID string    // X-Request-Id (REST) or _meta.request_id (MCP)
    Surface   string    // "rest" | "mcp" — set by the adapter, not the caller
    Tx        *sql.Tx
}
```

The `Resource` is the `resreg.Resource` above: its transport half is
`Endpoints` + `MCPDoc` (the two faces) + the injected `Gate` (authz); its
row-schema half (`RowType`, `Table`, `PKFields`, `Scope`, `Hooks`) is
authoritative in [`8-yaml-manifests.md`](8-yaml-manifests.md) and drives
SQL CRUD, YAML round-trip, and OpenAPI emission. `Execution.Tx` is the
adapter↔handler contract: the adapter opens the tx for a mutating action,
the handler mutates, and the one `audit_log` row lands in that same tx
(see [Audit contract](#audit-contract)); any handler error rolls it back.
The caller resolver runs per call — never captured at registration — so a
shared MCP socket can't confuse one agent's privileges for another's.

## Auth model — resreg holds no policy; each surface injects its gate

resreg owns the handler, tx, audit, and arg-derivation plumbing — but
**no authorization policy**. Authorization is an **injected `Gate`** on
the `Resource`: resreg calls it; the mounting daemon binds it per
surface. The `Gate` DEFAULTS to today's operator check — `auth.Authorize`
over the unified ACL rows ([4/9](../4/9-acl-unified.md)) — so the
operator REST path is unchanged and proxyd/webd need no edit; the agent
socket OVERRIDES it. Both surfaces still produce a surface-agnostic
`Caller`; the difference is which gate the mounting daemon injects, not
two authorization servers.

| Surface                         | Identity source                                                            | Injected gate                                                                             |
| ------------------------------- | -------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| Human / REST (`/v1/<name>`)     | `Authorization: Bearer <jwt>` (OAuth session, proxyd-stamped folder claim) | default `auth.Authorize("<name>:<action>", scope, params)` — scope/ACL match, **no tier** |
| Agent / MCP (per-folder socket) | the in-container socket, folder-bound at spawn                             | `db.Authorize(sub, folder, "mcp:"+tool, params)` — the tier-default-grants path           |

**"No tier" is true of the OPERATOR surface only.** Its decision is pure
scope/ACL match. The **agent surface is tier-based by design**:
`auth.AuthorizeWith` falls back to the tier-default grants
(`grants.DeriveRules` + `CheckAction`) for `mcp:*` actions at the agent's
own folder when no explicit ACL row matches (`auth/authorize.go`,
`routd/sibling_db.go`). The `mcp:` action prefix and
`AuthorizeOpts{Folder, WorldFolder, Tier}` are what select that fallback;
the operator gate passes neither, so it never fires. One ACL substrate,
two decisions — chosen by the injected gate.

A platform token is an ES256 JWT signed by `authd` — the **sole signer**
([1-auth-standalone.md](1-auth-standalone.md)). Every daemon verifies
offline against `authd`'s JWKs (`/v1/keys`); none mint their own. The
token carries `sub`, `scope`, and the namespaced folder claim
`arz/folder` (`auth/` treats it opaquely, surfaced via
`Identity.Extra["folder"]`; the folder-match helper lives in arizuko's
`identity.go`, keeping `auth/` folder-agnostic).

Scope vocabulary (operator surface): `<resource>:<verb>[:own_group]`.
`:own_group` scopes to the caller's folder subtree. `<resource>:*` covers
all verbs on a resource; there is no `*:*` global wildcard — operators
carry the enumerated list. The scope string is the operator-token-minting
shorthand over the ACL rows, not a second authorization path.

**Folder containment is per-resource, not a resreg primitive.** A
folder-scoped caller MUST NOT read or act cross-folder: a handler that
resolves a `jid`/`folder`/`run_id` param binds it to the caller's folder
(a `jid` resolves to its routing-target folder; a `run_id` to its run's
folder — a cross-folder run reads as absent, 404, not leaked; a bare
`folder` param is contained directly). The one shipped exemplar carries
NO precedent for this — `proxyd_routes` is a GLOBAL resource on a `**`
ACL row with no folder-bearing param — so containment is folded into each
resource's handler as the [`5/16`](16-mcp-rest-unification.md) rollout
reaches it; there is no resreg-level containment to inherit. `POST
/v1/messages` stays cross-folder by design (one adapter routes many
folders); cost reporting uses a dedicated `cost:write` scope plus
containment.

## Visibility is a separate tier firewall, outside resreg

Which tools a tier even SEES in `tools/list` is a tier concern the
per-call gate does not cover. On the agent socket it is enforced by
`grants.MatchingRules` (`ipc/ipc.go`): a tool whose rules don't match the
folder's tier is never registered on that socket — the menu is filtered,
not just the call. A naive `resreg.MCPTools` mount that `AddTool`s
unconditionally would WIDEN visibility (lower tiers seeing tools they
never had), so agent-tool registration stays driven by the socket's rule
filter (or a `visible(name)` predicate) — never by resreg policy. This is
orthogonal to the injected authorization gate above and to the per-call
firewall of [`12/17`](../12/17-mcp-firewall.md).

## One owner per table; in-process handler vs cross-daemon forwarder

Each table lives in exactly one daemon's DB; that daemon serves its
`/v1/*` face. `Resource.Store` picks the dispatch shape:

- **In-process handler (`Store` non-nil)** — the owning daemon serves its
  OWN table. resreg opens the tx, the handler mutates, and the single
  `audit_log` row lands in the SAME tx (`audit.EmitInTx`). This is every
  same-daemon resource (routd serving `routes`/`acl`/`groups`, onbod
  serving `onboarding_gates`). The agent-MCP facade is this shape too — a
  GENERATED thin front over the same in-process handler, NOT an HTTP hop —
  so the "one audit row in the mutation's tx" invariant holds for the
  agent exactly as for REST.
- **Cross-daemon forwarder (`Store: nil`)** — a resource whose table
  lives in ANOTHER daemon. resreg skips both the local tx and the local
  authorization/audit; the handler HTTP-forwards the caller's capability
  token to the owner, which runs the in-process handler and writes the
  one audit row (no double-log). `proxyd_routes`'s MCP face on webd is
  this shape: webd forwards to proxyd.

Never conflate them — the forwarder is for genuinely cross-daemon
resources only. The owner is resolved by compose service naming
(`<DAEMON>_URL`, e.g. `PROXYD_URL=http://proxyd:8080`), NEVER a lookup
registry (identity is configured, not derived — CLAUDE.md).

| Daemon     | Owns / serves                                                                                                      |
| ---------- | ------------------------------------------------------------------------------------------------------------------ |
| **routd**  | groups, routes, messages, sessions, channels, web_routes, route_tokens, grants, acl, secrets, network_rules, tasks |
| **runed**  | spawns, run history — control-plane REST only (`/v1/runs`), no resreg resource (see below)                         |
| **authd**  | signing keys, JWKs, sessions — `/v1/tokens`, `/v1/keys`, `/auth/*` login (sole signer)                             |
| **onbod**  | invites, admissions, auth_users, onboarding_gates                                                                  |
| **proxyd** | proxyd_routes (`/v1/proxyd_routes`); login delegates to authd                                                      |
| **dashd**  | nothing — FS-mounted, reads routd.db directly for display; writes via HTTP to owners                               |

`proxyd_routes` (proxyd's reverse-proxy table) and `routes` (routd's
message-routing table) are two distinct resources with distinct names —
never conflate them (renamed `aab3487a`).

## Resource name = wire identity

The resreg `Name` becomes `/v1/<name>` AND the OpenAPI `operationId`
prefix (`<name>.<action>`). Two daemons NEVER share a name — the name is
globally unique wire identity. The composed `<name>.<action>` string is
the operator-facing contract: OpenAPI `operationId`, audit-log `action=`
fields, metrics labels, permission-editor rows. URL renames and
handler-function renames don't break it.

## Audit contract

Every state-changing handler MUST write exactly one `audit_log` row in
the SAME transaction as the mutation; if the audit write fails, the
mutation rolls back. Read-only handlers emit slog only — no audit row.
Field shape: `caller=<sub> resource=<name> action=<verb>
surface=<mcp|rest> target=<folder> result=<allowed|denied|error>`.
`action` is the stable correlator — one `grep 'resource=groups
action=create'` returns work whether it arrived via the agent's
`groups.create` MCP tool, `POST /v1/groups` (browser), or `arizuko group
add`. Forwarders (`Store == nil`) skip the row; the
downstream daemon writes it (no double-log). Field schema:
[`I-tool-call-logging.md`](I-tool-call-logging.md).

## Anti-patterns — what should NOT be a resource

Same shape each: hot path, high-volume internal event, or stream rather
than CRUD verb. These stay REST-only (no `MCPDoc` entry → no MCP tool) or
aren't resources at all:

- **Inbound message ingestion** — per-message hot path; the poll loops
  write `messages` rows directly. (`inject_message` for synthetic sends
  IS an MCP tool — audited, low-volume.)
- **Cost-log writes** — per-Claude-call; direct store write from adapters
  and `timed`. Not an operator action.
- **Streaming surfaces** — slink stream, agent live output. Not CRUD/RPC;
  SSE sits next to resreg ([J-sse.md](J-sse.md)), never inside it.
- **Auth session creation** — minted by `authd`, verified by `auth/`.
  Substrate, not a user tool.
- **Migrations** — file-driven (`*/migrations/`), run by the owning
  daemon at startup. Not a resource.
- **runed run-control** (`POST /v1/runs`, `/v1/runs/stop`,
  `GET`/`DELETE /v1/runs/{id}`) — REST-only BY DESIGN: the internal
  routd→runed execution control plane. Agents never spawn or kill runs.
  runed exposes no resreg resource (`OpenAPIHandler("runed", []string{})`
  is empty by design — control verbs, not CRUD). The absence is correct,
  not a uniformity gap.

The rule: user-initiated, audit-worthy, allow/deny-shaped → resreg.
High-rate side effect of normal operation → not.

## Orthogonal specs (don't fold in)

- [`5/16`](16-mcp-rest-unification.md) — the **adoption program** that
  rolls this model out resource-by-resource (pilot `routes`, then
  replicate). 44 = roll it out; 45 = the mechanism. Migration steps live
  in 44, not here.
- [`5/8`](8-yaml-manifests.md) — the **same cold-tier resources as
  declarative YAML** you `export`/`apply` (config-as-data). A different
  front on the same tables; the row-level CRUD engine underneath both is
  shared plumbing, not a concept named here.
- [`12/17-mcp-firewall.md`](../12/17-mcp-firewall.md) — per-call
  allow/deny **filtering** between agent and MCP server. Gates the
  agent's MCP surface — both the hot-tier tools and the derived
  cold-tier ones; orthogonal to the injected authorization gate here and
  to the tier visibility firewall above.

## Acceptance

- A cold-tier resource is served by exactly one `resreg.Resource`; its
  agent-facing MCP tool (`deriveMCPTools`) and its `/v1/<res>` REST
  endpoint + OpenAPI entry all derive from that one handler — no
  hand-authored `MCPTools` list.
- The agent reaches the resource via its derived `<res>.<action>` MCP
  tools over its socket; the human reaches it via `/v1/<res>` REST.
- `GET /openapi.json` carries the operation `description` + `x-mcp-when`
  for each action that has an `MCPDoc` entry; the doc is self-describing
  for browsers and external tools.
- An action with no `MCPDoc` entry has no MCP tool and no `x-mcp-when`
  annotation but stays reachable via its REST path (REST-only).
- Injected gate: the operator REST `Gate` defaults to `auth.Authorize`
  (scope/ACL, no tier); the agent socket injects `db.Authorize` with
  `mcp:`+tier. Both deny cross-folder — `atlas/support` cannot touch a
  target under `rhias/*` — through the same handler, different gate.
- Hot-tier tools (`reply`, `send`, `inspect_*`) have no REST twin and no
  OpenAPI entry — MCP-only, by design.

Deferred: short token TTL (1h) + a revocation table for long-lived
dashd-issued keys; pagination harmonization (MCP arrays vs REST cursor
pagination) left per-resource for now.
