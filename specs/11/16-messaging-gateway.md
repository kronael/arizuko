---
status: draft
---

# messaging-gateway — generic message router

> Channels in, routes matched, messages out. A standalone router
> over opaque string ids — no folders, no grants, no agents. arizuko's
> `routd` consumes it and supplies the domain.

## Status

Draft / future. Extraction starts when a second consumer appears or
when `gated/` (and its successor `routd`) outgrows its current
envelope — same trigger as
[A-orthogonal-components.md](A-orthogonal-components.md) §
_messaging-gateway_. Today the routing mechanism lives inside
`gated`, tangled with arizuko domain; this spec defines what comes out
when the cut is made.

## Problem

`gated`'s routing core is generic at the protocol level — register a
channel adapter, normalize inbound events, match against a route
table, dispatch outbound with retry. But the implementation is braided
through arizuko's domain: routes key on `folder`, resolution walks the
folder hierarchy, the dispatch path checks grants and decides whether
to spawn an agent, and the message types carry `chat_jid` / `sender` /
`tier`. The mechanism (route a normalized message from a source id to a
destination id, retry on failure) is reusable; the domain (what a
folder is, which grant applies, when to spawn) is not.

Per [U-genericization.md](../5/U-genericization.md), `gated` is being
split into per-daemon daemons — `routd` (routing), `runed` (agent
runner), `mcpd` (MCP host), `authd`. `routd` is the arizuko **daemon**
that owns the domain. messaging-gateway is the generic **component**
that `routd` consumes. They are distinct: `routd` maps arizuko folders
onto messaging-gateway's opaque route ids and applies grants before
handing over a flat route; messaging-gateway never learns a folder
existed.

## What it owns (mechanism)

- **Channel adapter registry.** A live map of `(adapter id → endpoint)`.
  Adapters register over the HTTP API; the gateway forwards outbound
  messages to a registered adapter's endpoint and accepts inbound
  events from it. The gateway knows the adapter by an opaque id, not
  by platform semantics.
- **Normalized message types.** A flat inbound shape (`source id`,
  `conversation id`, `text`, `attachments`, opaque `meta`) and a flat
  outbound shape (`destination id`, `text`, `attachments`). All ids are
  strings. No `Folder`, no `JID`, no `Tier`, no `Sender`.
- **Route table keyed by opaque string ids.** A flat list of
  `(match → destination)` rules over string ids. Match is a string
  pattern (exact or glob) against the inbound `conversation id` /
  `source id`; destination is an opaque target id the consumer assigns
  meaning to. No hierarchy walk, no priority ladder beyond
  last-match-or-first-match (the component picks one and states it; see
  § _Route model_). The consumer collapses its own priority logic
  (@mention > reply > sticky > default — arizuko's ladder lives in
  [Q-unified-routing.md](../5/Q-unified-routing.md)) into the flat rule
  it hands over.
- **Outbound dispatch with retry.** Given a normalized outbound message
  and a destination id, resolve the adapter, deliver, and retry on
  transient failure with bounded backoff. Persist in-flight state
  (pending / sent / failed) in the component's own store; a background
  loop re-attempts pending rows and gives up after a ceiling. This is
  the poll-based delivery shape that already exists in
  [Q-unified-routing.md](../5/Q-unified-routing.md), lifted free of
  `messages.db`.

## What it does NOT own (arizuko domain)

- **Folder hierarchy.** No tree, no ancestry walk. The component sees
  opaque destination ids; arizuko's folder paths are mapped to them by
  `routd` before a route is installed.
- **Grants / tiers / ACL.** The component delivers whatever it is told
  to deliver. Authorization is decided by `routd` (against
  [`specs/4/9-acl-unified.md`](../4/9-acl-unified.md)) _before_ the
  route exists.
- **Agent spawn decisions.** "This inbound message means spawn an agent
  and run a turn" is arizuko domain. messaging-gateway routes a message
  to a destination id; what the consumer does when that id resolves to
  an agent runner is the consumer's business (`routd` → `runed`).
- **Sessions / sticky state / reply-chains.** Continuation logic
  (sticky session, reply-chain follow) is domain. The consumer resolves
  it to a concrete destination and installs a flat route; the gateway
  has no memory of conversations beyond in-flight delivery state.

## Route model (decided)

Flat list of `{match: <glob>, dest: <id>}`. **First match wins** —
evaluated top to bottom, first rule whose `match` glob matches the
inbound `conversation id` (falling back to `source id`) selects the
destination; no match → the message is dropped and recorded as
undelivered (no implicit default destination — strict, per the
arizuko CLAUDE.md "no silent fallbacks" rule the consumer inherits).
First-match (not deny-wins) because routing is positive selection, not
a permission gate: the consumer orders specific rules above general
ones. The consumer owns rule ordering; the gateway owns evaluation.

## Public surface

Three contracts, per
[A-orthogonal-components.md](A-orthogonal-components.md) §7. Exact
subcommand and field names are the component's business; the shapes
below are indicative.

**CLI** — primary surface, runnable with no arizuko process:

```
messaging-gateway serve [--listen :8080] [--db <path>]
messaging-gateway route add <match-glob> <dest-id>
messaging-gateway route list
```

**HTTP API** — what `routd` (or any consumer) drives:

- `POST /v1/adapters {id, endpoint}` — register a channel adapter.
- `DELETE /v1/adapters/{id}` — unregister.
- `POST /v1/inbound {source, conversation, text, attachments, meta}` —
  an adapter posts a normalized inbound event; the gateway matches it
  against the route table and records the routed destination.
- `GET /v1/routes` / `PUT /v1/routes` — read / replace the flat route
  table.
- `POST /v1/outbound {dest, text, attachments}` — enqueue an outbound
  message; the gateway resolves the adapter, delivers, retries.
- `GET /v1/state` — in-flight delivery state (pending / sent / failed).
- `GET /health`.

**Go imports** — `messaging-gateway/pkg/...` for in-process embedding
(the consumer that wants the router without an extra hop):

- `gateway.NewServer(Config) *Server` — what `serve` runs.
- `route.Match(table []Rule, conversation, source string) (dest string, ok bool)`
  — the pure matcher, exposed so callers can share it.
- `client.New(baseURL) *Client` — thin HTTP wrapper over the API.

## Layout

The [A §_Layout pattern_](A-orthogonal-components.md) skeleton:

```
messaging-gateway/
  README.md            public surface: CLI, HTTP API, Go imports
  Makefile             build, test, lint, image
  Dockerfile           ships its own image
  CHANGELOG.md         its own version history
  cmd/messaging-gateway/main.go
  pkg/gateway/         server + dispatch (public)
  pkg/route/           pure matcher (public)
  pkg/client/          HTTP client (public)
  internal/store/      delivery-state persistence (private; own SQLite)
  testdata/            route + match fixtures
```

## Orthogonality acceptance

Per [A §_Acceptance_](A-orthogonal-components.md):

- The mechanical grep returns empty:

  ```
  $ grep -rE 'github\.com/[^/]+/arizuko/(store|core|gateway|api|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|gated|auth|audit|resreg|obs)' messaging-gateway/
  <empty>
  ```

- `make -C messaging-gateway build && make -C messaging-gateway test`
  passes on a host with **no arizuko process and no arizuko data
  directory**. Tests drive the HTTP API and the pure matcher against
  `testdata/` fixtures.
- Every signature takes strings (`id string`, `dest string`,
  `conversation string`) — never `core.Folder`, `core.JID`,
  `core.Tier`, `chanlib.InboundMsg`.
- The component reads its own env vars and CLI flags; it does not
  import `core.Config` or arizuko's `.env` loader. It owns its own
  SQLite file for delivery state; it never opens `messages.db`.

## How arizuko consumes it

`routd` (the arizuko routing daemon from the
[U-genericization.md](../5/U-genericization.md) gated split) is the
domain layer on top:

1. `routd` owns the folder hierarchy, the route rules joined with
   grants, and the priority ladder (@mention > reply > sticky >
   default — [Q-unified-routing.md](../5/Q-unified-routing.md)).
2. On an inbound event, `routd` resolves the arizuko **domain**
   question — which folder, is the sender granted, is this a
   continuation — and collapses the answer into a single opaque
   destination id.
3. `routd` installs / updates the flat route in messaging-gateway
   (`PUT /v1/routes` or `pkg/gateway` in-process) **after** the grant
   check passes. The gateway never sees the grant or the folder; it
   sees `(glob → dest-id)`.
4. messaging-gateway dispatches outbound to the adapter and handles
   retry. When a destination id resolves to an agent runner, `routd`
   hands off to `runed`; the gateway is uninvolved in the spawn.

The split mirrors A's domain/mechanism table: arizuko owns _what a
folder is and which grant applies_; messaging-gateway owns _match a
normalized message against a flat table and deliver it with retry_.

## Out of scope

- Folder/grant/session logic of any kind (stays in `routd`).
- Agent spawn / turn lifecycle (stays in `runed`).
- Adapter implementations — the gateway registers and forwards to them;
  the per-platform adapters (`teled`, `whapd`, …) stay arizuko-side
  edge daemons that POST normalized inbound and receive normalized
  outbound.
- Auth — the gateway trusts its caller; arizuko's `proxyd` + `authd`
  sit in front when deployed inside arizuko.

## Acceptance

- `messaging-gateway serve` plus `messaging-gateway route add 'tg:*' agentA`
  routes an inbound event with `conversation=tg:123` to destination
  `agentA` and delivers an outbound message back through a registered
  stub adapter — with no arizuko process running.
- An inbound event matching no route is recorded undelivered, never
  silently dropped to a default.
- `make -C messaging-gateway build && make -C messaging-gateway test`
  passes on a host with no arizuko data directory.
- The orthogonality grep above returns empty.
