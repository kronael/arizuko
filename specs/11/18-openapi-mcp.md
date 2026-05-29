---
status: draft
depends: [1-auth-standalone, U-genericization, 36-yaml-manifests]
---

# openapi-mcp — annotated REST → well-behaved MCP

> Annotate an OpenAPI doc with `x-mcp-*` fields; a generic gateway
> serves it as an MCP server. The annotations carry the
> agent-facing instructions — sharp tool names, when-to-use prose,
> 1→N split, scope mapping — that a naive endpoint→tool bridge can't
> infer. No folders, no grants. arizuko's `/v1/*` daemons annotate
> their `/openapi.json`; openapi-mcp derives `5/5`'s MCP face from it.

## Status

Draft / future. The CRUD half already ships: `resreg`'s reflective
engine ([36-yaml-manifests.md](../5/36-yaml-manifests.md) §"OpenAPI
emission") turns a `RowType` struct into REST + MCP + OpenAPI from one
declaration. This spec extends the **derivation** from CRUD rows to
**action verbs** (send/reply/escalate/…) by carrying their
agent-facing description in the OpenAPI doc, and defines the generic
gateway that renders any annotated doc as MCP. Gated on `authd`
([1-auth-standalone.md](../5/1-auth-standalone.md), the REST JWT gate)
and the per-daemon `/v1/*` structure
([U-genericization.md](../5/U-genericization.md)). Cataloged here, not
yet extracted.

## Problem

A "tool" as the model sees it is a schema (name + description + JSON
args) plus a result channel. MCP is the transport, not a model
capability — the model never learns it's talking MCP, only that it has
tools with names and descriptions. So if a REST API already publishes
its operations as an OpenAPI doc, and that doc carries the
**agent-facing instructions**, a generic gateway can derive good MCP
tools from it. No second tree, no hand-authored MCP wrappers.

[`5/5`](../5/5-uniform-mcp-rest.md) today is "one handler, two faces":
each resource action is reachable via REST (OAuth-gated) and MCP
(scope-gated), but **both faces are hand-authored** — the `Endpoint`
slice and the `MCPTool` slice are written side by side. For
engine-managed CRUD resources, `resreg` already collapses this:
`RowType` reflection emits both. For **action verbs** it does not —
`send`, `reply`, `escalate_group` are still hand-registered in
`ipc/ipc.go` with their descriptions written inline.

The missing piece is a vocabulary that puts the agent-facing
description **in the OpenAPI operation**, so the MCP face derives from
the REST face instead of being maintained beside it. Once that exists,
`5/5` stops hand-authoring the MCP tree for verbs too.

## Why a naive bridge fails

Mechanical OpenAPI→MCP bridges exist in the ecosystem (one tool per
operation, operationId as the name, summary as the description). They
fail arizuko's load-bearing constraint — the `mcp_tool_naming` rule:

> different intents → different tool names + sharp descriptions; do
> NOT overload with kind/mode params (UNIX `cp`/`mv`/`ln`, not
> `relocate(kind=...)`).

A 1:1 bridge produces:

- One tool per endpoint, named from `operationId` (`createMessage`),
  not from intent. Two intents that share an endpoint get one tool.
- The OpenAPI `summary` as the description — written for an SDK
  consumer ("Create a message"), useless as agent "when to use this"
  guidance.
- Every path/query/body param flattened into the tool's input,
  including params the agent must not set (the folder, which comes from
  its scoped token).

The differentiator of this standard is **not** the mechanical
conversion — that's commodity. It's the **agent-ergonomics annotation
layer**: sharp naming, 1→N split, scope/auth-lens mapping, param
hiding. Don't overclaim novelty; claim the annotation vocabulary and
the dual-gate model.

## The annotation vocabulary (`x-mcp-*`)

OpenAPI 3.1 allows vendor extensions (`x-*`) on any object. The
gateway reads these on each operation; tooling that ignores them sees a
normal OpenAPI doc.

| Field          | On        | Meaning                                                                                     |
| -------------- | --------- | ------------------------------------------------------------------------------------------- |
| `x-mcp-tool`   | operation | MCP tool name. Default: `operationId`. The intent-named verb, not the URL.                  |
| `x-mcp-desc`   | operation | The load-bearing "when to use this" prose. **Without it the tool is not emitted** (strict). |
| `x-mcp-scope`  | operation | Scope the MCP caller needs (`messages:send`). Maps to the auth lens (below).                |
| `x-mcp-hidden` | operation | `true` → REST-only; no MCP tool emitted.                                                    |
| `x-mcp-split`  | operation | List of tool variants derived from one operation (the 1→N case, below).                     |
| `x-mcp-arg`    | parameter | Per-arg override: `hidden`, `default`, `desc`, `rename`. Hidden args never reach the model. |

`x-mcp-desc` is strict-on-purpose: a tool with no agent-facing
description is garbage to the model, so the gateway **refuses to emit
it** rather than fall back to `summary`. This mirrors arizuko's
"strict, not magical" rule — no silent fallback for missing
agent-facing data. (REST-only operations declare `x-mcp-hidden: true`
to opt out explicitly; absence of `x-mcp-desc` on a non-hidden op is a
doc error the gateway reports.)

### Arg mapping

A REST operation scatters inputs across `path`, `query`, and
`requestBody`. An MCP tool has one flat JSON input schema. The gateway
flattens all three into one object, keyed by parameter/property name;
on `tools/call` it re-scatters them into the HTTP request (path
substitution, query string, JSON body) per the operation's original
locations. `x-mcp-arg` overrides per arg:

- `hidden: true` — the arg is omitted from the tool schema and supplied
  by the gateway from request context (the folder from the caller's
  scoped token, not a model-set value).
- `default: <v>` — the arg is optional in the tool schema; the gateway
  fills the default when the model omits it.
- `rename: <name>` / `desc: <text>` — surface a clearer name/description
  to the model than the wire name carries.

`x-mcp-arg` is allowed in exactly two locations: on an OpenAPI
`parameter` object (path/query) and on a top-level `requestBody` schema
property. It is **not** read on nested object properties — see the
flattening rule below.

## Derivation semantics (normative)

The vocabulary is annotation-authored, not inferred: the gateway never
guesses agent ergonomics from an un-annotated doc. The contract below
is what makes the derivation deterministic and implementable.

**Flattenable args only.** The gateway flattens an operation's args
into a flat object of **scalars** (string/number/boolean/enum) and
arrays-of-scalars, keyed by name. An operation whose flattened arg set
would require a nested object, `oneOf`/`allOf` discriminator, free-form
`additionalProperties`, or a name collision (the same key from two of
path/query/body, or two `x-mcp-split` variants deriving the same tool
name) is a **doc error** — the gateway refuses to derive a tool for it
rather than invent a shape. Operations needing rich bodies declare
`x-mcp-hidden: true` (REST-only) or split the body into scalar fields
in the annotated `/v1/*` doc. This bounds "general OpenAPI" to the
subset arizuko's `resreg` already emits (scalar columns —
[36-yaml-manifests.md](../5/36-yaml-manifests.md) §"Schema-driven
CRUD").

**Override merge order.** For a tool, `x-mcp-arg` overrides layer
deterministically: base OpenAPI schema → operation-level `x-mcp-arg`
→ `x-mcp-split` variant `args`. Last layer wins per field
(`hidden`/`default`/`rename`/`desc`).

**Requiredness.** An arg that is `hidden`, or carries a `default` or a
context reference, is dropped from the tool schema's `required` list
(the model never supplies it). A required wire arg with none of these
stays required.

**Split semantics are authored, not inferred.** The reply-vs-send
difference (omit `reply_to` → top-level; set it → threaded) is a
property of the **backend**, surfaced by the annotation author who
writes the two `x-mcp-split` descriptions and the `reply_to` override.
The gateway does not infer threading from OpenAPI; it only renders the
authored variants. A generic `openapi-mcp` deriving sharp tools
requires a well-annotated doc, exactly as the §"Problem" framing says.

**Context references.** `default` (and the split `args.default`) may be
a literal or a context reference from a small named set: `$caller.*`
(identity fields the transport populates — `$caller.folder`,
`$caller.sub`) and `$turn.*` (per-call routing context —
`$turn.last_message_id`). The transport supplies these as **opaque
string context keys** at call time; the gateway treats them as
strings, never as folders or grants (this is how "no folders, no
grants" holds — the gateway sees `$caller.folder`'s value as an opaque
id, the same string-only discipline as the sibling components). An
unrecognized `$`-reference is a load-time doc error. A recognized
reference that the transport leaves unset at call time is a call-time
tool error (the gateway does not silently send an empty value).

**`x-mcp-scope` is documentation, not a gate.** It is surfaced in
`tools` output and may inform a consumer's auth-lens mapping, but the
gateway never enforces it — the backend's `auth.Authorize` is the only
gate. It is in the vocabulary because annotation authors want the
required scope recorded next to the operation; it carries no gateway
behavior.

**Strict load, no partial surface.** `serve` **fails to start** if any
non-hidden operation is missing `x-mcp-desc` or hits a doc error above;
`tools` exits non-zero and lists the offending operations. The MCP
surface never silently varies with doc quality — a doc either derives
cleanly or the gateway refuses to serve it. This is the "derive, don't
hand-roll" guarantee: there is no gap where a half-valid doc yields a
partial, drifting tool set.

### Worked example — simple CRUD op

`POST /v1/groups` (create a group) annotated:

```yaml
paths:
  /v1/groups:
    post:
      operationId: createGroup
      x-mcp-tool: register_group
      x-mcp-desc: >
        Register a new sub-group under a parent folder. Use when a
        conversation needs its own isolated context, routing, and
        grants. Not for renaming (use update_group) or for routing an
        existing chat (use add_route).
      x-mcp-scope: groups:write:own_group
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                parent: { type: string, x-mcp-arg: { hidden: true } }
                name: { type: string }
                model: { type: string, x-mcp-arg: { default: sonnet } }
      responses: { '201': { ... } }
```

Derived MCP tool: `register_group(name, model?)` — `parent` is hidden
(gateway fills it from the caller's folder), `model` defaults to
`sonnet`, the description is agent-facing. One operation, one tool, no
hand-authored MCP wrapper.

## THE FEASIBILITY GATE — the sharp cases

A naive 1:1 bridge fails the `mcp_tool_naming` rule. The vocabulary
must express the three cases below. **The 1→N split is the go/no-go:**
if it cannot render reply-vs-send cleanly (without param overloading),
the whole standard is not worth building — `5/5` keeps hand-authoring
the MCP tree.

### 1→N split — reply vs send (the canonical case)

arizuko's outbound message path is **one REST endpoint** (`POST
/v1/messages` writes a `messages` row) but **two MCP tools** with
sharply different descriptions and a different threading default. From
the live registrations (`ipc/ipc.go`):

- `reply` — THE DEFAULT response. Threads under the conversation being
  answered (quote/reply UI; Slack thread, not channel root). `reply_to`
  omitted → threads to the current turn automatically.
- `send` — a FRESH top-level message that is NOT a reply. Use only for
  a proactive notification or a message to a different chat.

A 1:1 bridge collapses these into one `createMessage(reply_to?, ...)`
tool and forces the model to reason about a `reply_to` param — exactly
the overloading the `mcp_tool_naming` rule forbids (`relocate(kind=...)`
shape). The split annotation:

```yaml
paths:
  /v1/messages:
    post:
      operationId: createMessage
      x-mcp-scope: messages:send
      x-mcp-split:
        - tool: reply
          desc: >
            THE DEFAULT way to respond — use for virtually every answer
            to the user. Delivers your message threaded to the
            conversation you're answering (quote/reply UI; on Slack it
            lands in the thread, not the channel root). Omit reply_to to
            thread to the current conversation automatically. Only reach
            for `send` when you deliberately need a fresh top-level
            message that is NOT a reply.
          args:
            reply_to:
              {
                default: '$turn.last_message_id',
                desc: 'earlier message to target; omit to auto-thread',
              }
        - tool: send
          desc: >
            A fresh top-level message that is NOT a reply to the current
            conversation. Use ONLY for a proactive/unprompted
            notification or a message to a different chat. For
            responding to the user (the normal case) use `reply`, which
            threads.
          args:
            reply_to: { hidden: true } # always top-level; never set
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                jid: { type: string }
                content: { type: string }
                reply_to: { type: string }
      responses: { '201': { ... } }
```

`x-mcp-split` is a list of variants; each variant names one tool, its
own `desc`, and per-arg overrides layered on the operation's base
schema. The gateway emits **two distinct tools** from one operation:

- `reply(jid, content, reply_to?)` — `reply_to` defaults to the
  triggering message id (`$turn.last_message_id`, resolved by the
  gateway from per-call context, not model-set).
- `send(jid, content)` — `reply_to` hidden (always omitted → the
  backend produces a top-level message).

Both call the same `POST /v1/messages`; the backend behaves
identically — threading is decided by whether `reply_to` is present.
The split lives entirely in the annotation; the REST handler stays
single. **This is the case `5/5` needs, and the vocabulary expresses it
without a single overloaded param.** Verdict: the standard is worth
building.

(Resolution syntax for context-supplied defaults — `$turn.*`,
`$caller.folder` — is a small named set the gateway documents; it is
not a general template language. Strict: an unrecognized `$`-reference
is a doc error, not a literal.)

### Suppress — `x-mcp-hidden`

REST-only operations get no agent tool:

```yaml
delete:
  operationId: purgeMessages
  x-mcp-hidden: true # operator REST surface; agents never purge
```

Internal/hot-path/operator-only operations (the
[`5/5` anti-patterns](../5/5-uniform-mcp-rest.md) §"What should NOT go
via MCP" — inbound ingestion, cost-log writes, migrations) carry
`x-mcp-hidden: true`. The gateway never emits a tool; the REST face
stays.

### Param hiding / defaulting

The folder is the recurring case: the agent operates within its scoped
token's folder, so a `folder`/`parent` arg must come from context, not
from a model-chosen value. `x-mcp-arg: { hidden: true }` removes it from
the tool schema; the gateway fills it from the caller's identity (over
the scoped socket, below). This prevents an agent from naming a folder
outside its scope as a tool argument — the auth boundary is not
expressible as a model-set param.

## The gateway

`openapi-mcp` reads an annotated OpenAPI doc and serves an MCP server:

1. **Load + validate.** Parse the doc, walk operations, build the tool
   table: for each non-hidden operation (or each `x-mcp-split` variant)
   with `x-mcp-desc`, emit a tool `{name, description, inputSchema}`.
   Reject ops missing `x-mcp-desc` (strict). Build the reverse map
   `tool → (method, path, arg-locations, hidden-arg sources, defaults)`.
2. **`tools/list`.** Return the derived tool table. The model sees
   sharp names + agent-facing descriptions; it never sees the REST
   shape.
3. **`tools/call`.** Look up the tool, validate args against the
   derived schema, fill defaults + context-supplied hidden args,
   re-scatter into an HTTP request (path/query/body per the op),
   forward to the backend, map the HTTP response into MCP content
   (JSON body → text/structured content; non-2xx → MCP tool error
   carrying the status + body).
4. **Transport.** It bridges the same transports arizuko's `ipc/`
   already uses — a unix socket / stdio pair into the container (the
   socat bridge per [ipc/SECURITY.md](../../ipc/SECURITY.md)) and the
   streamable-HTTP MCP endpoint from [J-sse.md](../5/J-sse.md) for
   remote clients. The agent connects to the gateway socket; the
   gateway dials the REST backend over HTTP.
5. **Auth.** The gateway forwards the caller's identity/scope to the
   REST backend, which enforces. It does **not** decide authorization —
   `x-mcp-scope` is documentation of what the call needs, surfaced for
   the consumer's lens mapping; the backend's `auth.Authorize`
   ([4/9](../4/9-acl-unified.md)) is the gate.

### Deny / error behavior

- **Missing `x-mcp-desc` on a non-hidden op, or any doc error** (§
  "Derivation semantics") — `serve` refuses to start; `tools` exits
  non-zero listing the offending ops. Strict, no `summary` fallback, no
  partial surface.
- **Unknown tool on `tools/call`** — JSON-RPC method/params error; no
  HTTP request made.
- **Arg validation failure** — JSON-RPC error before any HTTP call.
- **Backend non-2xx** — relayed as an MCP tool error with the status
  code and response body; `401/403` pass through as denials (the
  backend gated the call, the gateway reports it).
- **Backend unreachable** — MCP tool error; the gateway never fabricates
  a success.

## How arizuko consumes it

arizuko is the primary consumer and the reason the annotation layer
exists:

1. **arizuko annotates `/openapi.json`.** Extend the `resreg` emission
   ([36-yaml-manifests.md](../5/36-yaml-manifests.md) §"OpenAPI
   emission") to carry `x-mcp-*` on operations: CRUD ops get tool
   name/desc/scope from the `resreg.Resource` (the engine already knows
   the resource name + action); action-verb resources declare their
   `x-mcp-desc` / `x-mcp-split` in the same registration that today
   writes `MCPTool` slices by hand. One renderer, one doc.
2. **openapi-mcp derives the agent's MCP surface from it.** The
   in-container agent's tool list is rendered from the annotated
   `/openapi.json`, not hand-rolled in `ipc/ipc.go`. This is `5/5`'s
   MCP face — derived, not maintained beside the REST face.
3. **The unix socket stays the security boundary.** The agent reaches
   openapi-mcp over the scoped `ipc/` socket; the gateway forwards to
   `/v1/*` carrying the agent's identity. **The container is not handed
   a raw JWT** — the scoped socket is the capability, matching today's
   model ([ipc/README.md](../../ipc/README.md) "Capability token";
   [`5/5`](../5/5-uniform-mcp-rest.md) keeps the agent's token at the
   socket/host, not in the container).
4. **`5/5` simplifies.** The hand-written verb tools in `ipc/ipc.go`
   (reply, send, escalate_group, …) are replaced by `x-mcp-*`
   annotations on the corresponding `/v1/*` operations. The
   "one handler, two faces" promise holds with one face **derived** —
   the MCP tree is no longer a second authored artifact that can drift
   from REST.

The split mirrors [A](A-orthogonal-components.md)'s
domain/mechanism table: arizuko owns _which operations exist, what they
mean, and which scope they need_ (it authors the annotations);
openapi-mcp owns _render an annotated doc as MCP tools and forward
tool calls to HTTP_. The gateway never learns what a folder or grant
is.

## Public surface

Three contracts, per
[A-orthogonal-components.md](A-orthogonal-components.md) §7.
Indicative shapes; exact names are the component's business.

**CLI** — primary surface, runnable with no arizuko process:

```
openapi-mcp serve --spec <openapi.json|url> [--listen <sock-or-addr>] [--backend <base-url>]
openapi-mcp tools --spec <openapi.json>      # dry-run: list derived tools + flag undescribed ops
```

`--listen` is a unix socket (stdio bridge) or HTTP address matching the
MCP client's transport; `--backend` overrides the doc's `servers[]`
base URL.

**HTTP / MCP endpoint** — what an MCP client drives:

- The MCP server itself over the chosen transport (`tools/list`,
  `tools/call`, `initialize`, `ping`).
- `GET /health`.

**Go imports** — `openapi-mcp/pkg/...`:

- `gateway.NewServer(Config) *Server` — what `serve` runs.
- `derive.Tools(doc []byte) ([]Tool, error)` — the pure derivation:
  annotated OpenAPI bytes → tool table. Exposed so a consumer can
  inspect/validate the surface without serving.
- `client.New(backendURL) *Client` — thin HTTP forwarder.

## Layout

The [A §_Layout pattern_](A-orthogonal-components.md) skeleton:

```
openapi-mcp/
  README.md            public surface: CLI, MCP endpoint, Go imports
  Makefile             build, test, lint, image
  Dockerfile           ships its own image
  CHANGELOG.md         its own version history
  cmd/openapi-mcp/main.go
  pkg/gateway/         MCP server + HTTP forward (public)
  pkg/derive/          pure annotated-OpenAPI → tool-table (public)
  pkg/client/          backend HTTP client (public)
  internal/transport/  unix-socket / stdio / HTTP bridging (private)
  testdata/            annotated OpenAPI docs + expected tool tables
```

## Orthogonality acceptance

Per [A §_Acceptance_](A-orthogonal-components.md):

- The mechanical grep returns empty:

  ```
  $ grep -rE 'github\.com/[^/]+/arizuko/(store|core|gateway|api|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|gated|auth|audit|resreg|obs)' openapi-mcp/
  <empty>
  ```

- `make -C openapi-mcp build && make -C openapi-mcp test` passes on a
  host with **no arizuko process and no arizuko data directory**. Tests
  drive `derive.Tools` against `testdata/` annotated docs and the
  gateway against a stub HTTP backend.
- Every signature takes the OpenAPI doc and plain strings (`tool
string`, `backend string`) — never `core.Folder`, `core.Scope`,
  `resreg.Resource`. The gateway knows nothing of folders or grants.
- The component reads its own env vars / flags and owns no persistent
  state beyond the loaded doc; it never opens `messages.db`.

## Out of scope

- Authoring the annotations — arizuko writes `x-mcp-*` into its own
  doc via `resreg`; the gateway only reads them.
- Deciding authorization — the backend's `auth.Authorize` is the gate;
  `x-mcp-scope` is documentation surfaced for the consumer's lens, not
  an enforcement point.
- Tool-call _filtering_ — allow/deny per call is
  [17-mcp-firewall.md](17-mcp-firewall.md)'s job, a different layer that
  sits between agent and MCP server. openapi-mcp _produces_ a tool
  surface; mcp-firewall _gates_ one. Compose them: agent → firewall →
  openapi-mcp → REST.
- Streaming / SSE — openapi-mcp maps request/response operations; SSE
  surfaces ([J-sse.md](../5/J-sse.md)) stay their own endpoints.
- A general template language for context-supplied defaults — only a
  small named `$turn.*` / `$caller.*` set, documented and strict.

## Acceptance

- `openapi-mcp serve --spec <doc>` where `<doc>` carries the reply/send
  `x-mcp-split`: `tools/list` returns **two** tools `reply` and `send`
  with distinct descriptions; calling `reply` forwards `POST
/v1/messages` with `reply_to` defaulted, calling `send` forwards the
  same path with `reply_to` omitted — against a stub backend, no
  arizuko process running.
- A doc operation without `x-mcp-desc` and without `x-mcp-hidden` makes
  `serve` refuse to start and `tools` exit non-zero (strict load, no
  partial surface, no `summary` fallback).
- Two operations (or two `x-mcp-split` variants) deriving the same tool
  name make `serve` refuse to start (name collision is a doc error).
- An `x-mcp-hidden: true` operation produces no tool but is reachable
  via its REST path on the backend.
- A hidden arg (`x-mcp-arg: { hidden: true }`) is absent from the tool
  schema and is filled by the gateway from call context.
- A backend `403` becomes an MCP tool error carrying the status; the
  gateway fabricates no success.
- `make -C openapi-mcp build && make -C openapi-mcp test` passes on a
  host with no arizuko data directory.
- The orthogonality grep above returns empty.
