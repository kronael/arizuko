---
status: shipped
---

# mcpfw — transparent MCP tool-call filter

> A binary that sits in front of ONE MCP server, parses JSON-RPC, and
> allows / denies / logs each `tools/call` against a flat ruleset.
> It ships alone. arizuko is not a consumer, and this spec says why.

## What it is for

Anyone running an agent against an MCP server they do not control has the
same problem: the server advertises a catalogue, the agent may call all of
it, and nothing in the protocol expresses "this agent may call these tools
and not those". mcpfw is the generic answer on that wire — intercept every
`tools/call`, match the tool name against a flat allow/deny ruleset, pass
or refuse.

The mechanism is generic: parse JSON-RPC, match a tool name, forward or
refuse. The domain — WHICH caller may use which tool — belongs to whoever
runs it. The firewall takes opaque strings and never learns what a tenant,
a user or a folder is. That split is the component contract of
[16-daemon-standalone-matrix.md](16-daemon-standalone-matrix.md), and
mcpfw is one of the parts that passes its import-orthogonality test.

This is a different layer from
[../5/23-skill-guard.md](../5/23-skill-guard.md). Skill-guard is a
PreToolUse hook that scans the CONTENT of agent-written skill files before
they land on disk; mcpfw filters JSON-RPC tool calls on the wire.
Skill-guard answers "is this skill file dangerous to write", mcpfw answers
"may this caller call this tool". Keep them distinct.

## arizuko is not a consumer

**Measured 2026-09-06.** No arizuko package imports `mcpfw`, nothing pushes
it a ruleset, and no agent's MCP traffic passes through it. That is the
correct arrangement, not a gap: arizuko already gates the calls mcpfw would
gate, with the same decision semantics and one axis more.

**The agent has exactly one MCP server.** `injectMcpEnv`
(`ant/src/mcp-servers.ts`) returns a single entry — `arizuko`, socat to the
per-turn gated socket `/run/ipc/gated.sock` — and `ant/src/index.ts` passes
that map as the turn's whole tool config. Each harness holds the same line:
`claude` sets `strictMcpConfig: true` so a `.mcp.json` in the turn's cwd is
not read (`ant/src/claude.ts`), `codex` passes `--ignore-user-config` so an
agent-written `$HOME/.codex/config.toml` `[mcp_servers]` block is not read
(`ant/src/codex.ts`), and `pi` declares `tools: 'none'` (`ant/src/pi.ts`).
The agent never dials a third-party MCP server; routd does.

**A third-party server arrives as a connector, and its tools are local.**
`routd.LoadConnectors` reads `[[connector]]` blocks with `transport = "mcp"`
(`routd/connectors.go`) and hands the specs to ipc (`routd/mcp.go`). ipc
discovers each server's catalogue with THIS folder's credential and
registers every remote tool as a LOCAL tool named `<connector>_<tool>`
through `granted()` (`ipc/ipc.go`). routd dials the upstream itself, inside
the tool handler, in `ipc.CallConnectorTool` (`ipc/connector.go`) — after
the gate has run.

**The gate is `auth.Authorize` on the `acl` rows.** `granted()` wraps every
handler in `authorizeCall(name, nil)` → `db.Authorize(folder,
"mcp:"+name, nil)`, and routd injects the real evaluator per turn
(`Authorize: s.turnAuthorize(t)` in `routd/mcp.go` →
`db.AuthorizeTurn` in `routd/agent_gate.go` → `auth.Authorize` in
`routd/sibling_db.go`). `callerSub` is the turn principal and non-empty,
which is the condition that switches the row check on. A second check
filters the announcement, so a tool the turn does not hold is not
advertised either.

**Same semantics, one axis more.** `auth.Authorize` is deny-wins and a
no-match is a deny (`auth/authorize.go`) — the model this spec's ruleset
section specifies. It is default-deny for connector tools in practice too:
the seeded floor role carries twelve messaging verbs and nothing else
(`routd/migrations/0023-4r-role-member.sql`), so a connector tool needs an
explicit `mcp:<connector>_<tool>` or `mcp:*` grant. On top of that,
`AuthorizeTurn` carries the two axes of
[../5/33-paths-roles.md](../5/33-paths-roles.md): the turn's folder confers
in full and the attested sender adds only inside that folder's subtree.
A `Rule` here is `{tool, action}` over a name glob with no target at all,
so it cannot express containment.

**So wiring it would be the forbidden second path.** Putting mcpfw on the
connector call would run two gates on one `tools/call` — root `CLAUDE.md`
bans that, and two gates drift. Making mcpfw the single gate would trade a
containment-carrying evaluator for one that matches names only. Neither is
an improvement, so the component ships alone: `6/16`'s own category, "none"
in its gate column.

**What a consumer would have to build.** A host that wanted mcpfw in front
of an MCP server would derive the flat allowed-tool list for one caller,
write it as the ruleset JSON, run `mcpfw serve` against the upstream, and
point the caller's MCP client at the firewall's address instead of the
upstream's. Nothing in the shipped binary reads a ruleset from anywhere but
that file at startup, so a host that needs per-caller rules runs one
process per ruleset. An in-process host skips the binary and calls
`firewall.Firewall.Handle` directly.

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
This is the opposite choice from `routd`'s first-match route table, and
deliberately so — routing is positive selection, gating is negative
authority.

`*` spans any run of characters including separators, so `read_*` covers
`read_file`. A ruleset whose action is not one of the three, or whose tool
pattern is empty, fails to load rather than being skipped: a typo'd rule
that parsed as "no rule" would vanish, and under default-deny a vanished
`deny` is the dangerous direction.

## Public surface

**CLI** — `serve` is the only verb, and the whole configuration is flags:

```
mcpfw serve --upstream <url> --rules <file> [--listen :7070] [--filter-list]
```

`--upstream` is one MCP server URL over HTTP; `--rules` is the ruleset JSON,
read once at startup. `--filter-list` removes denied tools from a
`tools/list` reply and is off by default: hiding a tool changes what the
model believes exists, which is a choice to make rather than a side effect
of gating. A refused call, and any call a `log` rule matches, is written to
the process log.

**HTTP** — one route, the proxy itself: `POST /` takes a single JSON-RPC
frame as the body and returns the gated reply. There is no admin API and no
runtime ruleset push; the ruleset is the file the process started with.

**Go imports** — `mcpfw/pkg/...`:

- `rule.Load(io.Reader) (Set, error)` — parse and validate a ruleset.
- `rule.Set.Decide(tool string) Decision` — the pure decision, `Allowed`
  plus `Logged`. `rule.Match(pattern, tool string) bool` is the glob.
- `firewall.Firewall` — `Rules`, `Upstream`, `Log`, `FilterList` as fields,
  gating one message per `Handle(json.RawMessage)`.
- `firewall.Upstream` — one `Call` method, so a stub in a test is three
  lines and the gate runs with no real server.

`internal/transport` carries JSON-RPC over HTTP and is private.

## Layout + orthogonality

Standard component skeleton and mechanical grep test per `6/16` — `mcpfw/`
is a sibling dir with its own `README`/`Makefile`/`Dockerfile`, `cmd/`,
public `pkg/{firewall,rule}`, private `internal/transport/`, and a
`testdata/` ruleset fixture. Acceptance adds two component-specific checks on top
of `6/16`'s: every signature takes strings (`tool`, `upstream`) and the flat
`Rule` shape — never `core.Folder`, `core.Grant`, or anything grant-shaped
— and `make -C mcpfw test` runs on a host with no arizuko data directory.

## Acceptance

Held by `mcpfw/pkg/firewall` and `mcpfw/pkg/rule` tests, all against a stub
upstream with no arizuko process:

- With a ruleset allowing `read_*` and denying `send_*`, a `tools/call` for
  `read_file` reaches the upstream and returns its reply; a call for
  `send_message` returns a JSON-RPC error and the upstream is never reached.
- A `tools/call` for a tool matched by no rule is denied (default-deny), and
  a `deny` beats an `allow` that also matches (deny-wins).
- A `log` rule marks the call logged and permits nothing on its own, and the
  logger sees a refused call with its decision.
- With `FilterList`, a denied tool is absent from the `tools/list` reply and an
  allowed one survives; without it, the denied tool is still advertised.
- A ruleset with an unknown action or an empty tool pattern fails to load.

The claim this spec makes about arizuko is held separately, by
`ipc/connector_gate_test.go`: a connector tool the turn does not hold is
neither announced nor dialled, a tool it holds reaches the upstream, and a
tool revoked after registration is refused at call time with the upstream
untouched.
