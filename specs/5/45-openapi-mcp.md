---
status: active
supersedes: 5/5-uniform-mcp-rest
depends:
  [1-auth-standalone, 5/E-routd, 36-yaml-manifests, specs/4/9-acl-unified]
---

# specs/5/45 — uniform REST+MCP: author REST, derive MCP

> The canonical "one handler, two faces" statement. Every cold-tier
> management resource is authored **once** as a REST handler with an
> annotated `/openapi.json` (`x-mcp-*` fields); the MCP face is
> **derived** from that doc by a generic gateway. One `auth.Authorize`
> gate, two identity sources (OAuth-gated REST, scope-gated MCP).
> Hot-tier agent actions (`reply`, `send`, `inspect_*`) are MCP-only
> by design — no REST resource to derive from. Supersedes
> [`5/5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) for the mechanism;
> [`5/44`](44-mcp-rest-unification.md) is the adoption program that
> rolls this out.

## The model

Two tiers, by design — not a pending migration:

- **Cold-tier (operator config)** — `routes`, `acl`, `groups`,
  `secrets`, `scheduled_tasks`, `network_rules`, `web_routes`,
  `route_tokens`, `onboarding_gates`, `proxyd_routes`. Each is reachable
  via **REST** (human, OAuth-gated) AND **MCP** (agent, scope-gated).
  ONE REST handler per `(resource, action)`; the MCP tool is **derived**
  from the annotated OpenAPI, never a second hand-written tree.
- **Hot-tier (agent runtime)** — `reply`, `send`, `like`, `delete`,
  `post`, `diary`, session control (`fork_topic`, `engage`, `disengage`,
  `reset_session`, `inject_message`), inspect (`inspect_routing`,
  `inspect_tasks`, `inspect_session`). **MCP-only by design**, authored
  in `ipc/ipc.go`. These are agent-to-conversation primitives; an
  operator REST mirror adds nothing (operators don't `reply` to chats).
  NEVER fold them into the cold-tier interop.

The only thing that differs between the two cold-tier faces is the
identity source. The gate is the same.

## Why derive, not hand-author both

Today the shipped exemplar (`proxyd/resource.go` `routesResourceDecl`

- `webd/routes_mcp.go`) authors both faces side by side: a
  `resreg.Resource` carries a hand-written `Endpoints` slice AND a
  hand-written `MCPTools` slice — literally duplicated across two files
  that drift over time. `resreg` collapses this for schema-driven CRUD
  (`RowType` reflection emits both — [36-yaml-manifests.md](36-yaml-manifests.md)
  §"OpenAPI emission"), but action verbs are still hand-registered.

The fix: put the agent-facing description **in the OpenAPI operation**,
so the MCP face derives from the REST face instead of being maintained
beside it. One renderer, many sinks — the OpenAPI doc is the single
render, REST and MCP are two reads of it. This is the arizuko
"one renderer, many sinks" rule applied to the surface itself.

## Why a naive OpenAPI→MCP bridge fails

Mechanical bridges exist (one tool per operation, `operationId` as name,
`summary` as description). They fail arizuko's `mcp_tool_naming` rule:

> different intents → different tool names + sharp descriptions; do NOT
> overload with kind/mode params (UNIX `cp`/`mv`/`ln`, not
> `relocate(kind=...)`).

A 1:1 bridge produces one tool per endpoint named from `operationId`
(so two intents sharing an endpoint collapse to one tool), the SDK-facing
`summary` as description (useless as agent "when to use this" guidance),
and every path/query/body param flattened into the input (including the
folder, which must come from the caller's scoped token, not a model-set
value). The differentiator is NOT the mechanical conversion — that's
commodity — it's the **annotation layer** that carries what the bridge
can't infer.

## The annotation vocabulary (`x-mcp-*`)

OpenAPI 3.1 allows vendor extensions (`x-*`) on any object. The gateway
reads these per operation; tooling that ignores them sees a normal
OpenAPI doc.

| Field          | On        | Meaning                                                                                     |
| -------------- | --------- | ------------------------------------------------------------------------------------------- |
| `x-mcp-tool`   | operation | MCP tool name. Default: `operationId`. The intent-named verb, not the URL.                  |
| `x-mcp-desc`   | operation | The load-bearing "when to use this" prose. **Without it the tool is not emitted** (strict). |
| `x-mcp-scope`  | operation | Scope the MCP caller needs (`messages:send`). Documented next to the op; not a gate.        |
| `x-mcp-hidden` | operation | `true` → REST-only; no MCP tool emitted.                                                    |
| `x-mcp-split`  | operation | List of tool variants derived from one operation (the 1→N case, below).                     |
| `x-mcp-arg`    | parameter | Per-arg override: `hidden`, `default`, `desc`, `rename`. Hidden args never reach the model. |

`x-mcp-desc` is strict-on-purpose: a tool with no agent-facing
description is garbage to the model, so the gateway **refuses to emit
it** rather than fall back to `summary`. This mirrors arizuko's
"strict, not magical" rule — no silent fallback for missing agent-facing
data. REST-only operations opt out explicitly with `x-mcp-hidden: true`;
absence of `x-mcp-desc` on a non-hidden op is a doc error the gateway
reports.

### Arg mapping

A REST operation scatters inputs across `path`, `query`, and
`requestBody`. An MCP tool has one flat JSON input schema. The gateway
flattens all three into one object, keyed by parameter/property name; on
`tools/call` it re-scatters them into the HTTP request (path
substitution, query string, JSON body) per the operation's original
locations. `x-mcp-arg` overrides per arg:

- `hidden: true` — omitted from the tool schema; supplied by the gateway
  from request context (the folder from the caller's scoped token, never
  a model-set value).
- `default: <v>` — optional in the tool schema; the gateway fills the
  default when the model omits it.
- `rename: <name>` / `desc: <text>` — surface a clearer name/description
  than the wire name carries.

`x-mcp-arg` is read in exactly two locations: an OpenAPI `parameter`
object (path/query) and a top-level `requestBody` schema property. It is
NOT read on nested object properties — see the flattening rule.

## Derivation semantics (normative)

The vocabulary is annotation-authored, not inferred: the gateway never
guesses agent ergonomics from an un-annotated doc.

**Flattenable args only.** The gateway flattens an operation's args into
a flat object of **scalars** (string/number/boolean/enum) and
arrays-of-scalars, keyed by name. An operation whose flattened arg set
requires a nested object, `oneOf`/`allOf` discriminator, free-form
`additionalProperties`, or a name collision (the same key from two of
path/query/body, or two `x-mcp-split` variants deriving the same tool
name) is a **doc error** — the gateway refuses to derive a tool rather
than invent a shape. Operations needing rich bodies declare
`x-mcp-hidden: true` or split the body into scalar fields. This bounds
"general OpenAPI" to the subset `resreg` already emits (scalar columns —
[36-yaml-manifests.md](36-yaml-manifests.md) §"Schema-driven CRUD").

**Override merge order.** For a tool, `x-mcp-arg` layers
deterministically: base OpenAPI schema → operation-level `x-mcp-arg` →
`x-mcp-split` variant `args`. Last layer wins per field.

**Requiredness.** An arg that is `hidden`, or carries a `default` or a
context reference, is dropped from the tool schema's `required` list. A
required wire arg with none of these stays required.

**Context references.** `default` may be a literal or a reference from a
small named set: `$caller.*` (identity fields the transport populates —
`$caller.folder`, `$caller.sub`) and `$turn.*` (per-call routing context —
`$turn.last_message_id`). The transport supplies these as **opaque string
context keys** at call time; the gateway treats them as strings, never as
folders or grants. An unrecognized `$`-reference is a load-time doc
error; a recognized reference the transport leaves unset at call time is
a call-time tool error (the gateway never sends an empty value silently).
It is a small named set, not a template language.

**`x-mcp-scope` is documentation, not a gate.** It is surfaced in `tools`
output and may inform a consumer's auth-lens mapping; the gateway never
enforces it. The backend's `auth.Authorize` is the only gate.

**Strict load, no partial surface.** The gateway **fails to start** if
any non-hidden operation is missing `x-mcp-desc` or hits a doc error;
its dry-run mode exits non-zero listing the offending ops. The MCP
surface never silently varies with doc quality — a doc either derives
cleanly or the gateway refuses to serve it.

## The 1→N split — reply vs send (the go/no-go case)

arizuko's outbound message path is **one REST endpoint** (`POST
/v1/messages` writes a `messages` row) but **two MCP tools** with sharply
different descriptions and a different threading default (from the live
registrations in `ipc/ipc.go`):

- `reply` — THE DEFAULT response. Threads under the conversation being
  answered. `reply_to` omitted → threads to the current turn.
- `send` — a FRESH top-level message that is NOT a reply. Only for a
  proactive notification or a message to a different chat.

A 1:1 bridge collapses these into one `createMessage(reply_to?, ...)`
and forces the model to reason about `reply_to` — exactly the overloading
`mcp_tool_naming` forbids. The split annotation:

```yaml
paths:
  /v1/messages:
    post:
      operationId: createMessage
      x-mcp-scope: messages:send
      x-mcp-split:
        - tool: reply
          desc: >
            THE DEFAULT way to respond. Delivers your message threaded to
            the conversation you're answering. Omit reply_to to thread to
            the current conversation automatically. Only reach for `send`
            when you deliberately need a fresh top-level message.
          args:
            reply_to: { default: '$turn.last_message_id' }
        - tool: send
          desc: >
            A fresh top-level message that is NOT a reply. Use ONLY for a
            proactive notification or a message to a different chat. For
            responding to the user use `reply`, which threads.
          args:
            reply_to: { hidden: true } # always top-level
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                jid: { type: string }
                content: { type: string }
                reply_to: { type: string }
```

The gateway emits **two distinct tools** from one operation: `reply(jid,
content, reply_to?)` (default from `$turn.last_message_id`) and
`send(jid, content)` (`reply_to` hidden → backend produces a top-level
message). Both call the same `POST /v1/messages`; threading is decided by
whether `reply_to` is present. The split lives entirely in the
annotation; the REST handler stays single. This is the case the uniform
surface needs, expressed without a single overloaded param — the mechanism
is worth building.

## The gateway

`openapi-mcp` reads an annotated OpenAPI doc and serves an MCP server:

1. **Load + validate.** Parse the doc, walk operations, build the tool
   table: for each non-hidden operation (or each `x-mcp-split` variant)
   with `x-mcp-desc`, emit `{name, description, inputSchema}`. Reject ops
   missing `x-mcp-desc` (strict). Build the reverse map `tool → (method,
path, arg-locations, hidden-arg sources, defaults)`.
2. **`tools/list`.** Return the derived table. The model sees sharp names
   - agent-facing descriptions; it never sees the REST shape.
3. **`tools/call`.** Look up the tool, validate args, fill defaults +
   context-supplied hidden args, re-scatter into an HTTP request, forward
   to the backend, map the response into MCP content (JSON body →
   text/structured; non-2xx → MCP tool error carrying status + body).
4. **Auth.** The gateway forwards the caller's identity/scope to the REST
   backend, which enforces. It does NOT decide authorization —
   `x-mcp-scope` is documentation; the backend's `auth.Authorize`
   ([4/9](../4/9-acl-unified.md)) is the gate.

### Deny / error behavior

- **Missing `x-mcp-desc` on a non-hidden op, or any doc error** — the
  gateway refuses to start; dry-run exits non-zero. No `summary`
  fallback, no partial surface.
- **Unknown tool on `tools/call`** — JSON-RPC error; no HTTP request.
- **Arg validation failure** — JSON-RPC error before any HTTP call.
- **Backend non-2xx** — relayed as an MCP tool error with the status +
  body; `401/403` pass through as denials.
- **Backend unreachable** — MCP tool error; the gateway never fabricates
  a success.

## Auth model — one gate, two identity sources

Both surfaces produce a surface-agnostic `Caller` consumed identically,
and both run the SAME gate: `auth.Authorize` over the unified ACL row
table ([4/9](../4/9-acl-unified.md)). It composes two checks:

1. **Scope / ACL** — "may this principal perform this action at this
   scope". The canonical decision.
2. **Folder containment** — for a subtree-bound resource, the caller's
   folder must own the target's folder (target is the caller's folder or
   a descendant). Empty caller folder = root / service token =
   unrestricted (adapters and `service:routd` legitimately span folders).

The two surfaces differ only in the IDENTITY SOURCE feeding the lens:

| Surface | Identity carrier                                              | Verifier           | Folder source                        |
| ------- | ------------------------------------------------------------- | ------------------ | ------------------------------------ |
| REST    | `Authorization: Bearer <jwt>` (OAuth session, proxyd-stamped) | `auth.VerifyHTTP`  | JWT `arz/folder` → `Extra["folder"]` |
| MCP     | capability token at the agent socket bind (`ipc/README.md`)   | `auth.VerifyToken` | socket-bound token folder            |

A platform token is an ES256 JWT signed by `authd` — the **sole signer**
([1-auth-standalone.md](1-auth-standalone.md)). Every daemon verifies
offline against `authd`'s JWKs (`/v1/keys`); none mint their own. The
token carries `sub`, `scope`, and the namespaced folder claim
`arz/folder` (`auth/` treats it opaquely, surfaced via
`Identity.Extra["folder"]`; the folder-match helper lives in arizuko's
`identity.go`, keeping `auth/` folder-agnostic). Folder bounds the
subtree; scopes bound the verbs. There is no `tier` — authorization is
scope-match (capability tokens via authd downscope).

Scope vocabulary: `<resource>:<verb>[:own_group]`. `:own_group` scopes to
the caller's folder subtree. `<resource>:*` covers all verbs on a
resource; there is no `*:*` global wildcard — operators carry the
enumerated list. The gate is `auth.Authorize` over the ACL rows; the
scope string is the operator-token-minting shorthand over those rows,
not a second authorization path.

**Containment binds per resource to its folder-bearing param:** a `jid`
resolves to its routing-target folder before the check; a `run_id`
resolves to its run's folder (a cross-folder run reads as absent, 404,
not leaked); a bare `folder` param is contained directly. `POST
/v1/messages` stays cross-folder by design (one adapter routes many
folders); cost reporting uses a dedicated `cost:write` scope plus folder
containment. A folder-scoped token MUST NOT read or act cross-folder over
either face.

## One owner per table; federation over HTTP

Each table lives in exactly one daemon's DB; that daemon serves its
`/v1/*` face. Cross-daemon MCP calls become **HTTP forwards** carrying
the agent's capability token — the owner is resolved by compose service
naming (`<DAEMON>_URL`, e.g. `PROXYD_URL=http://proxyd:8080`), NEVER a
lookup registry (identity is configured, not derived — CLAUDE.md).

| Daemon     | Owns / serves                                                                                                      |
| ---------- | ------------------------------------------------------------------------------------------------------------------ |
| **routd**  | groups, routes, messages, sessions, channels, web_routes, route_tokens, grants, acl, secrets, network_rules, tasks |
| **runed**  | spawns, run history — control-plane REST only (`/v1/runs`), no resreg resource (see below)                         |
| **authd**  | signing keys, JWKs, sessions — `/v1/tokens`, `/v1/keys`, `/auth/*` login (sole signer)                             |
| **onbod**  | invites, admissions, auth_users, onboarding_gates                                                                  |
| **proxyd** | proxyd_routes (`/v1/proxyd_routes`); login delegates to authd                                                      |
| **dashd**  | nothing — FS-mounted, reads routd.db directly for display; writes via HTTP to owners                               |

The agent's per-turn MCP socket terminates in `routd`
(`ServeTurnMCP` — hosted in-process, [`E-routd.md`](E-routd.md)). routd
serves its own resources locally; a call to a resource another daemon
owns forwards over HTTP with the agent's token (`invites.*` → onbod,
etc.). The forwarder is a `Resource{Store: nil}` whose handler does an
HTTP call downstream; the adapter skips the tx/audit dance and the
destination daemon writes the audit row. `webd/routes_mcp.go` is the
canonical example. `proxyd_routes` (proxyd's reverse-proxy table) and
`routes` (routd's message-routing table) are two distinct resources with
distinct names — never conflate them (renamed `aab3487a`).

## Resource name = wire identity

The resreg `Name` becomes `/v1/<name>` AND the MCP tool prefix
(`<name>.<action>`). Two daemons NEVER share a name — the name is
globally unique wire identity. The composed `<name>.<action>` string is
the operator-facing contract: OpenAPI `operationId`, MCP tool names,
audit-log `action=` fields, metrics labels, permission-editor rows. URL
renames and handler-function renames don't break it.

## Audit contract

Every state-changing handler MUST write exactly one `audit_log` row in
the SAME transaction as the mutation; if the audit write fails, the
mutation rolls back. Read-only handlers emit slog only — no audit row.
Field shape: `caller=<sub> resource=<name> action=<verb>
surface=rest|mcp target=<folder> result=<allowed|denied|error>`. `action`
is the cross-surface stable correlator — one `grep 'resource=groups
action=create'` returns work whether it arrived via `POST /v1/groups`,
MCP `groups.create`, or `arizuko group add`. Forwarders (`Store == nil`)
skip the row; the downstream daemon writes it (no double-log). Field
schema: [`I-tool-call-logging.md`](I-tool-call-logging.md).

## Anti-patterns — what should NOT go via MCP

Same shape each: hot path, high-volume internal event, or stream rather
than CRUD verb. All carry `x-mcp-hidden: true` (REST stays) or aren't
resources at all:

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

## How arizuko deploys the gateway

The gateway is a **library invoked in-process by `routd`'s
`ServeTurnMCP`**, not a separate daemon — the agent's MCP socket already
lives in routd, and the derivation is a pure function over the annotated
doc. At socket setup routd (a) fetches each owned/federated daemon's
annotated `/openapi.json`, (b) derives the tool table, (c) serves it on
the same per-turn socket. For a resource routd owns, the derived
`tools/call` dispatches locally; for a federated resource it forwards
over the owner's `<DAEMON>_URL`. The container is NEVER handed a raw JWT
— the scoped socket is the capability ([`ipc/README.md`](../../ipc/README.md)
"Capability token").

Hot-tier tools (`reply`, `send`, `inspect_*`) are hand-registered on the
same socket from `ipc/ipc.go`; the derived cold-tier tools and the
authored hot-tier tools coexist on one MCP server. `ipc/ipc.go` loses its
hand-rolled **management** tools (they derive from OpenAPI) and keeps the
hot-tier tools + the unix-socket transport.

## Orthogonal specs (don't fold in)

- [`5/44`](44-mcp-rest-unification.md) — the **adoption program** that
  rolls this mechanism out resource-by-resource (pilot `routes`, then
  replicate). 44 = roll it out; 45 = the mechanism. Migration steps live
  in 44, not here.
- [`5/36`](36-yaml-manifests.md) — the **same cold-tier resources as
  declarative YAML** you `export`/`apply` (config-as-data). A different
  front on the same tables; the row-level CRUD engine underneath both is
  shared plumbing, not a concept named here.
- [`11/17-mcp-firewall.md`](../11/17-mcp-firewall.md) — per-call
  allow/deny **filtering** between agent and MCP server. openapi-mcp
  _produces_ a tool surface; mcp-firewall _gates_ one. Compose: agent →
  firewall → openapi-mcp → REST.

## Acceptance

- A resource is served by exactly one REST handler; its `/v1/<res>`
  endpoint, its OpenAPI entry, and its MCP tool all derive from it —
  no hand-authored `MCPTools` list.
- A doc carrying the reply/send `x-mcp-split`: `tools/list` returns
  **two** tools `reply` and `send` with distinct descriptions; calling
  `reply` forwards `POST /v1/messages` with `reply_to` defaulted,
  `send` forwards it omitted — against a stub backend, no arizuko process.
- A non-hidden operation without `x-mcp-desc` makes the gateway refuse to
  start and dry-run exit non-zero (strict, no `summary` fallback).
- Two operations (or two `x-mcp-split` variants) deriving the same tool
  name make the gateway refuse to start (name collision).
- An `x-mcp-hidden: true` operation produces no tool but is reachable via
  its REST path.
- A hidden arg (`x-mcp-arg: { hidden: true }`) is absent from the tool
  schema and filled by the gateway from call context.
- A backend `403` becomes an MCP tool error carrying the status; the
  gateway fabricates no success.
- Auth parity: an agent token with `grants:write:own_group` and folder
  `atlas/support` can `PATCH /v1/grants/{id}` under `atlas/support/*` AND
  call `grants.update` over MCP — both 200; the same token cannot touch a
  grant under `rhias/*` — both 403.
- Hot-tier tools (`reply`, `send`, `inspect_*`) have no REST twin and no
  OpenAPI entry — MCP-only, by design.

## Open

- **Gateway deployment shape.** This spec pins the arizuko answer:
  in-process library inside routd's `ServeTurnMCP`. A standalone
  `openapi-mcp` binary (own image, CLI `serve`/`tools`) is a valid future
  extraction for reuse outside arizuko — the derivation is a pure
  function — but not required to ship the mechanism. Extract only if a
  non-arizuko consumer materializes.
- **Token TTL & revocation.** Short TTL (1h) default; a revocation table
  for long-lived dashd-issued keys is deferred until needed.
- **Pagination shape.** MCP tools return arrays; REST uses cursor
  pagination on some routes. Harmonize or leave per-resource.
