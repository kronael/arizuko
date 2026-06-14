---
status: draft
---

# mcp-firewall — transparent MCP tool-call filter

> Sits between an agent and its MCP servers, parses JSON-RPC, and
> allows / denies / logs each `tools/call` against a flat ruleset.
> No folders, no grants — arizuko derives the rule list and pushes it.

## Status

Draft / future. No consumer wired yet. Cataloged in
[A-orthogonal-components.md](A-orthogonal-components.md) §
_mcp-firewall_; this spec expands that stub. The gated split shipped
(`routd` + `runed` — see [`specs/5/E-routd.md`](../5/E-routd.md),
[`P-runed.md`](../5/P-runed.md)); this lands when per-folder tool gating
needs a home that isn't tangled with grants.

## Problem

arizuko needs per-folder tool gating for in-container agents: a folder
granted only `messages:read` must not be able to call a tool that
sends. The natural place to enforce this is between the agent's MCP
client and the MCP server(s) it talks to — intercept every
`tools/call`, check it, pass or refuse. But the _mechanism_ (parse
JSON-RPC over the existing transport, match a tool name against a rule
list, forward or refuse) is generic; the _domain_ (what a folder is,
which grant produces which tools) is arizuko's. Today, building this
inside arizuko would braid the MCP-proxying mechanism through the grant
layer.

Split it: mcp-firewall is the generic **component** that filters by a
flat ruleset. arizuko's grant layer derives the flat allowed-tool list
per (folder, agent) and pushes it. The firewall never learns what a
folder or grant is. arizuko's `mcpd` host (from the gated split) sits
_behind_ the firewall; the agent's MCP traffic flows
agent → firewall → `mcpd`.

This is a different layer from
[8-skill-guard.md](8-skill-guard.md): skill-guard is a PreToolUse hook
that scans the _content_ of agent-written skill files for threat
patterns before they land on disk. mcp-firewall filters _JSON-RPC
tool calls_ on the wire. Skill-guard answers "is this skill file
dangerous to write"; mcp-firewall answers "is this agent allowed to
call this tool right now". Both gate the agent, at different layers;
keep them distinct.

## What it owns (mechanism)

- **Transparent MCP proxy.** Bridges the same transport arizuko already
  uses for MCP — a unix socket / stdio pair into the container (the
  `ipc/` socat bridge per [ipc/SECURITY.md](../../ipc/SECURITY.md)),
  and the streamable-HTTP MCP endpoint from
  [J-sse.md](../5/J-sse.md) for remote clients. The agent connects to
  the firewall instead of directly to the upstream MCP server; the
  firewall dials one or more upstreams and splices the streams.
- **JSON-RPC parse + dispatch.** Reads each JSON-RPC frame. For
  `tools/call`, extract the tool name and check it against the ruleset.
  For `tools/list`, optionally filter the returned tool array to the
  allowed set (so a denied tool is invisible, not just un-callable).
  Everything else (`initialize`, `ping`, resource reads, prompts)
  passes through untouched.
- **Allow / deny / log per call.** On `tools/call`: allow → forward to
  upstream and relay the response; deny → return a JSON-RPC error to
  the client without touching upstream; log → record the call (and its
  decision) to the firewall's own log/store. The decision is a pure
  function of the tool name and the flat ruleset.
- **Runtime state.** The active ruleset (pushed by the consumer) and a
  call log. Its own store; never `messages.db`.

## What it does NOT own (arizuko domain)

- **Where the ruleset comes from.** arizuko's grant layer derives it;
  the firewall receives a flat list. The firewall has no concept of a
  grant, a tier, or a scope expression.
- **What a folder / agent / session is.** The firewall sees opaque
  tool-name strings and an opaque instance label. Mapping a folder's
  grants to a tool list is arizuko's job.
- **Tool semantics.** The firewall does not know what `send_message`
  does versus `read_file`. It matches names against globs; meaning is
  the consumer's.

## Ruleset model (decided)

Flat list of `{tool: <glob>, action: allow|deny|log}` over opaque
tool-name strings. **Deny-wins, default-deny:**

- Every rule is evaluated; if any matching rule has `action: deny`, the
  call is denied (deny dominates allow regardless of order).
- A `log` rule records the call and does not by itself permit or
  refuse — the allow/deny outcome is decided by the allow/deny rules.
- A `tools/call` matching **no** `allow` rule (and no `deny`) is
  **denied** — default-deny, no implicit pass-through for unlisted
  tools.

Deny-wins + default-deny (not first-match) because this _is_ a
permission gate: the safe failure is "refuse", and a later broad
`allow: *` must never silently re-open a tool an earlier rule denied.
This is the opposite choice from
[messaging-gateway](16-messaging-gateway.md)'s first-match route
table, and deliberately so — routing is positive selection, gating is
negative authority.

## Public surface

Three contracts, per
[A-orthogonal-components.md](A-orthogonal-components.md) §7.

**CLI** — primary surface, no arizuko process needed:

```
mcp-firewall serve --upstream <addr> --rules <file> [--listen <sock-or-addr>]
mcp-firewall check <tool-name>        # dry-run a name against the ruleset
```

`--upstream` may repeat (multiple upstream MCP servers fan in). The
listen endpoint is a unix socket (stdio bridge) or an HTTP address,
matching the transport the client uses.

**HTTP / admin API** — to push a ruleset at runtime:

- `PUT /v1/rules` — replace the active ruleset (the consumer pushes the
  derived flat list here).
- `GET /v1/rules` — read the active ruleset.
- `GET /v1/log` — recent call decisions (for audit).
- `GET /health`.

**Go imports** — `mcp-firewall/pkg/...`:

- `firewall.NewProxy(Config) *Proxy` — what `serve` runs.
- `rule.Decide(rules []Rule, tool string) Action` — the pure decision
  function (`Allow` / `Deny`), exposed so callers can share it.
- `client.New(adminURL) *Client` — thin HTTP wrapper for pushing rules.

## Layout

The [A §_Layout pattern_](A-orthogonal-components.md) skeleton:

```
mcp-firewall/
  README.md            public surface: CLI, HTTP/admin API, Go imports
  Makefile             build, test, lint, image
  Dockerfile           ships its own image
  CHANGELOG.md         its own version history
  cmd/mcp-firewall/main.go
  pkg/firewall/        proxy + JSON-RPC splice (public)
  pkg/rule/            pure decide function (public)
  pkg/client/          admin HTTP client (public)
  internal/transport/  unix-socket / stdio / HTTP bridging (private)
  testdata/            JSON-RPC frames + ruleset fixtures
```

## Orthogonality acceptance

Per [A §_Acceptance_](A-orthogonal-components.md):

- The mechanical grep returns empty:

  ```
  $ grep -rE 'github\.com/[^/]+/arizuko/(store|core|gateway|api|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|gated|auth|audit|resreg|obs)' mcp-firewall/
  <empty>
  ```

- `make -C mcp-firewall build && make -C mcp-firewall test` passes on a
  host with **no arizuko process and no arizuko data directory**. Tests
  drive the proxy against a stub upstream MCP server and assert
  allow / deny / list-filter outcomes from `testdata/` fixtures.
- Every signature takes strings (`tool string`, `upstream string`) and
  the flat `Rule` shape — never `core.Folder`, `core.Grant`,
  `types.Scope`, or anything grant-shaped.
- The component reads its own env vars / flags and owns its own store;
  it never opens `messages.db` or imports `grants`/`auth`.

## How arizuko consumes it

arizuko owns the per-(folder, agent) tool policy; the firewall enforces
the derived list:

1. arizuko's grant layer (`auth/`, `grants/`, against
   [`specs/4/9-acl-unified.md`](../4/9-acl-unified.md)) resolves a
   folder's grants into the **flat allowed-tool list** for the agent
   about to run a turn — the domain decision.
2. arizuko pushes that flat list to the firewall (`PUT /v1/rules` or
   `pkg/firewall` in-process) as `{tool: <glob>, action: allow}` rules,
   one ruleset per agent/turn. Denials are implicit via default-deny;
   explicit `deny` rules express revocations on top of a broad allow.
3. `routd` hosts the per-turn MCP socket (see
   [`specs/5/E-routd.md`](../5/E-routd.md)); the firewall sits
   **between** the in-container agent and that socket. The agent's MCP
   client connects to the firewall socket — the same `ipc/` socat bridge
   it uses today ([ipc/SECURITY.md](../../ipc/SECURITY.md)); the firewall
   dials routd. The agent cannot reach the MCP socket except through the
   firewall.
4. The firewall allows / denies / logs each `tools/call`; arizuko reads
   the decision log via `GET /v1/log` for audit (arizuko's own
   `audit_log` remains the forensic source of truth — the firewall's
   log is observability, mirroring the OTLP-vs-audit split in
   CLAUDE.md).

The split mirrors A's domain/mechanism table: arizuko owns _which tools
this folder's grants permit_; mcp-firewall owns _match a tool name
against a flat rule list and forward or refuse the JSON-RPC call_.

## Out of scope

- Deriving rules from grants (stays in `auth/` / `grants/`).
- Folder / tier / scope concepts of any kind.
- Tool-argument inspection or payload scanning (a denied tool is denied
  by name; argument-level policy is a separate concern, not this gate).
- Skill-file content scanning — that is
  [8-skill-guard.md](8-skill-guard.md)'s PreToolUse hook, a different
  layer.
- Auth of the admin API — arizuko's `proxyd` + `authd` front it when
  deployed inside arizuko; standalone, the admin endpoint trusts its
  caller.

## Acceptance

- `mcp-firewall serve --upstream <stub> --rules <file>` with a ruleset
  allowing `read_*` and denying `send_*`: an agent `tools/call` for
  `read_file` reaches the stub upstream and returns its result; a call
  for `send_message` returns a JSON-RPC error without touching upstream
  — with no arizuko process running.
- A `tools/call` for a tool matched by no rule is denied (default-deny).
- With `tools/list` filtering on, the denied tool does not appear in
  the listed tools returned to the client.
- `make -C mcp-firewall build && make -C mcp-firewall test` passes on a
  host with no arizuko data directory.
- The orthogonality grep above returns empty.
