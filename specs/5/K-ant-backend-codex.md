---
status: planned
---

# Ant backend abstraction — Codex as second harness

A `Backend` interface in ant lets the runtime drive different
agentic harnesses underneath, with the MCP surface above unchanged.
First implementation: the existing claude-CLI driver. Second
implementation: OpenAI's Codex via `codex app-server` (JSON-RPC 2.0
over stdio). Future candidates: `opencode.ai`, etc.

Sibling to [../10/c-ant-mcp-runtime.md](../10/c-ant-mcp-runtime.md)
(the claude-specific runtime), which becomes the first
implementation of this interface.

## Core principle: ant wraps harnesses, never is one

Stated up front because it scopes everything below: **ant does not
implement agent loops.** The agent loop — model calls, tool
execution, multi-step reasoning, retries, prompt caching, session
state, the whole "agentic" part — lives entirely in the external
harness. Ant spawns the harness, speaks its wire protocol, and
exposes the result over MCP.

Consequences:

- No "API-direct" backend. Calling Anthropic / OpenAI APIs and
  running our own loop is explicitly not a backend.
- No vendor-neutral abstraction layer pretending to unify
  disparate provider APIs. LiteLLM-shaped wrappers are a different
  product.
- A harness that doesn't have a clean wire protocol (TUI-only,
  one-shot only, signal-driven interrupt) is not a candidate.
- Anything that requires ant to grow features the harness already
  has (skills, MCP-client, permission system, file tools) is out
  of scope.

The value ant adds is **wrapping and improving** existing harnesses
into a coherent, MCP-fronted, folder-shaped, operator-managed
system. The harnesses do the model work.

## Why now (planned, not unshipped)

The claude driver works. Operationally there is no immediate
requirement for a second harness. This spec stays `planned` until
one of these triggers fires:

1. **Vendor lock concern**: an arizuko deployment context where
   OpenAI is required or preferred over Anthropic.
2. **Capability gap**: a real workload claude handles poorly that
   another harness handles well.
3. **Reliability**: claude rate-limited or outage; need a fallback
   per agent at the runtime level.

When a trigger fires, this spec flips to `unshipped` and the
implementation work begins. The pre-work — getting the `Backend`
interface right, picking Codex over Open Interpreter — is captured
here so it doesn't need to be redone.

## Harness picked: Codex (`codex app-server`)

Researched against Open Interpreter and `opencode.ai`. Summary
from the oracle survey:

| Axis               | Claude CLI (today) | Codex `app-server`                                                            | Open Interpreter                 |
| ------------------ | ------------------ | ----------------------------------------------------------------------------- | -------------------------------- |
| Wire shape         | NDJSON over stdio  | JSON-RPC 2.0 over stdio/unix/ws                                               | Python lib, optional FastAPI SSE |
| Streaming          | message deltas     | `item/agentMessage/delta` events                                              | SSE chunks                       |
| Multi-turn in proc | yes                | yes (`thread/start`, `turn/start`)                                            | yes in Python lib, no via CLI    |
| Interrupt          | protocol message   | `turn/interrupt` RPC + `turn/steer` mid-turn nudges                           | SIGINT only                      |
| Tool use           | structured         | structured `mcpToolCall`, `command/exec`, `fs/*`                              | code execution; loose            |
| MCP                | client + server    | client (config in `~/.codex/config.toml`) + server (`codex_mcp_interface.md`) | not really                       |
| Auth               | OAuth / API key    | ChatGPT OAuth or `OPENAI_API_KEY`                                             | LiteLLM-routed                   |
| License            | proprietary        | Apache-2.0                                                                    | AGPL-3.0                         |
| Last release       | active             | weekly through 2026                                                           | Oct 2024 (stalled)               |
| Sandboxing         | permission modes   | sandbox policies + permission profiles + approval flow                        | trust-by-default                 |

Codex wins on every operational axis: documented protocol, first-
class interrupt, structured tool-use, MCP integration, active
maintenance, permissive license, near-isomorphic loop shape.

OI eliminated: stalled (18 months since last release), AGPL
(licensing friction for distributed servers), no first-class CLI
protocol, no documented interrupt.

`opencode.ai` noted as a strong third candidate (MIT, claude-cli-
shaped, HTTP+SSE server). If a third backend lands later, this is
where to look.

## The `Backend` interface

Lives in `ant/pkg/backend/`. Both `internal/claude/` and a future
`internal/codex/` implement it.

```go
// Backend spawns and owns one harness subprocess per session.
type Backend interface {
    Spawn(ctx context.Context, cfg SessionConfig) (Driver, error)
    Name() string         // "claude" | "codex" | …
    Capabilities() Caps   // declares what's supported
}

// Driver drives one live session over the harness's wire protocol.
type Driver interface {
    Events() <-chan Event                       // streaming output
    SendUserMessage(text string) error          // user turn
    Interrupt(ctx context.Context) (Ack, error) // mid-turn stop
    SetModel(ctx context.Context, model string) (Ack, error)
    SetPermissionMode(ctx context.Context, mode string) (Ack, error)
    Close() error
}

// Event is a normalized event from any harness.
type Event struct {
    Type    EventType // EvSystemInit, EvAssistant, EvToolUse, EvToolResult,
                     // EvResult, EvRateLimit, EvKeepAlive
    Raw     map[string]any // harness-native payload (preserved verbatim)
    Text    string         // best-effort extracted text (helper)
    Final   bool           // true on the turn-terminating event
}

// SessionConfig is the union of what any backend might accept.
// Backends ignore fields they don't support; Capabilities() declares which.
type SessionConfig struct {
    Model          string
    Cwd            string
    SessionID      string
    Resume         string
    SystemPrompt   string
    MCPConfig      string
    PermissionMode string
    AddDirs        []string
    Env            []string
    ExtraArgs      []string
}

// Capabilities is what a backend reports up front; the MCP layer
// surfaces this to callers so they don't ask for things the backend
// can't do.
type Caps struct {
    Streaming        bool
    Interrupt        bool
    MultiTurn        bool
    SetModelLive     bool
    PermissionPrompt bool
    ToolUse          bool
    SessionResume    bool
    MCPClient        bool
}
```

Both claude and codex check every box in `Caps` today. The
interface is sized for them; backends with fewer capabilities are
allowed but explicitly degrade.

## Event normalization

Each backend produces a normalized stream of `Event` values. The
`Raw` field preserves the harness-native payload so callers that
care about full fidelity (thinking blocks, tool-use shapes, usage
stats) get everything. The `Type` field is the union of categories
both backends share.

Mapping table:

| Normalized     | claude NDJSON                                    | codex JSON-RPC                                      |
| -------------- | ------------------------------------------------ | --------------------------------------------------- | --------------- |
| `EvSystemInit` | `{type:"system", subtype:"init", …}`             | `thread/started` + initial caps                     |
| `EvAssistant`  | `{type:"assistant", message:{content:[…]}}`      | `item/agentMessage/delta`, `item/agentMessage/done` |
| `EvToolUse`    | `{type:"assistant"}` w/ `tool_use` content block | `item/mcpToolCall` / `command/exec`                 |
| `EvToolResult` | `{type:"user"}` w/ `tool_result` content block   | `item/toolResult`                                   |
| `EvResult`     | `{type:"result", subtype:"success"               | "error", …}`                                        | `turn/finished` |
| `EvRateLimit`  | `{type:"rate_limit_event", …}`                   | analog if present                                   |
| `EvKeepAlive`  | `{type:"keep_alive"}`                            | analog if present                                   |

Backends emit normalized events into the same channel the MCP layer
already reads (`Driver.Events()`). The MCP server's `session.send`
tool emits each event in a `notifications/progress` payload — same
pattern as today, just the `Raw` field changes shape per backend.

## What the MCP layer doesn't change

The MCP front (`internal/mcp/`) and its tool surface (`session.*`)
do not change. From above, the caller can't tell which harness is
running underneath — same `tools/call`, same progress
notifications, same `Ack` shapes on `session.interrupt` /
`session.set_model`.

Per-session backend choice is configured at `session.create` time:

```jsonc
{"name":"session.create",
 "arguments":{
   "backend": "codex",            // new arg; default "claude"
   "model":   "gpt-5",
   "cwd":     "/workspace",
   …
 }}
```

If `backend` is omitted, the server's default is used — set per ant
process via `--default-backend=claude|codex` on the CLI. The mix
within one ant process is allowed: groupA's session on claude,
groupB's session on codex. The MCP socket serves them both.

## Tool-use bridging — the load-bearing part

Both claude and codex support MCP. The claude driver already passes
`--mcp-config` to point claude at MCP servers it should consume.
Codex has the same — configured in `~/.codex/config.toml` or via
the app-server's setup payload. The backend's `Spawn` receives
the same `MCPConfig` path and is responsible for installing it
into the harness's native format.

Consequence: when an agent uses tools, the tool calls flow through
the harness's MCP-client to the MCP servers arizuko exposes (the
`gated.sock` socket). The backend doesn't need to translate tool
schemas — both harnesses speak MCP natively.

This is the load-bearing reason a swap is feasible at all: both
harnesses' MCP-client behavior makes them interoperable on the
tool side. A harness without MCP-client support would need a
schema translation layer in ant — and that translation layer is
exactly the "ant becomes an agent loop" tar pit we ruled out.

## Folder shape compatibility

The ant-folder (SOUL.md, CLAUDE.md, skills/, MCP.json, secrets/,
workspace/) is claude-shaped today: skills are markdown with
frontmatter and `claude` auto-loads them from `.claude/skills/`.

Codex does not natively auto-load arizuko-shaped skills. Two options:

1. **Materialize skills into Codex's system prompt** at session
   creation. The codex backend reads `skills/`, concatenates
   relevant SKILL.md bodies into the system prompt, and uses
   Codex's `SystemPrompt` field. Lossy (no per-tool injection,
   no on-demand activation) but mechanically simple.

2. **Skills as MCP tools**. Reframe each skill as an MCP server
   ant exposes via the same `gated.sock` mechanism, with tool
   names derived from skill frontmatter. Both claude and codex
   consume them uniformly. Higher fidelity but more plumbing.

v1 of this spec picks (1). (2) is a follow-up if the codex
backend sees real use and the system-prompt approach hits limits.

The folder shape itself doesn't change. The skills are still
claude-shaped markdown, edited the same way. The backend's job is
to feed them to whichever harness it drives.

## Open questions

1. **Codex version pin**. Same pinning model as claude — `ARG
CODEX_VERSION` in `ant/Dockerfile` (when the codex backend
   image variant exists). app-server protocol is younger than
   claude's NDJSON; field-level breakage between releases is more
   likely.
2. **`Caps` declarations for legacy harnesses**. If a third
   backend with weaker capabilities arrives (e.g. signal-only
   interrupt), how does the MCP layer represent the degraded
   surface to callers? A `Caps` field already exists; verify the
   spec's discovery story (`session.capabilities` MCP tool?
   include in `session.create` response?).
3. **`opencode.ai` as third backend** — capture as future-work
   note. It's MIT, HTTP+SSE server, claude-cli-shaped. Could land
   without disturbing the interface.
4. **Per-session backend choice vs per-folder default**. A folder
   could declare its preferred backend in a manifest field; the
   `session.create` default falls back to the folder hint, then
   to the process default. Specify when the folder layout (b-)
   gets a `backend:` slot.
5. **Cost / latency reporting**. Both harnesses report token usage
   in their final events; the `Event` normalization should expose
   this in a uniform field so MCP responses can include cost
   without backend-specific code in the MCP layer.
6. **Authentication injection**. claude reads from
   `~/.claude/credentials`; codex reads from `~/.codex/auth.json`
   or `OPENAI_API_KEY`. Backend's responsibility to map a
   single ant-level secret into the harness's expected location.

## Acceptance (when this spec flips to unshipped)

- `ant/pkg/backend/` defines `Backend`, `Driver`, `Event`,
  `Caps`, `SessionConfig`. Both `internal/claude/` and
  `internal/codex/` implement it.
- `ant --default-backend=codex /workspace --mcp` starts a session
  that runs against Codex; the MCP `session.send` tool streams
  progress; `session.interrupt` lands a `turn/interrupt` RPC.
- A real-`codex` smoke suite under `ant/test/smoke/codex/` mirrors
  the claude one: basic turn, streaming send, interrupt mid-stream,
  tool-use round-trip via an MCP server. Costs real tokens; opt-in.
- A mixed-backend session list — `session.create` once with
  claude, once with codex, both alive in one ant process — works
  end-to-end.
- `Caps()` for both backends is verified to match the actual
  capability surface (no false advertising).
- Zero changes to the MCP front (`internal/mcp/`) beyond accepting
  the new `backend` arg in `session.create`.

## Out of scope

- Implementing API-direct against any vendor. Ruled out by the
  core principle.
- LiteLLM-style multi-vendor wrappers as a backend. Different
  product; if it ever lands, lives next to ant, not inside.
- Harness translation layers (transforming claude's NDJSON shape
  into codex's protocol or vice versa). Each backend natively
  speaks one harness; no cross-translation.
- Forking a harness to fix protocol gaps. If a harness doesn't
  have what we need, we wait for upstream or pick a different
  harness. Not maintaining a fork.
- Backend-specific operator verbs in arizuko. The fleet ops in
  [../10/e-ant-portability.md](../10/e-ant-portability.md) work
  identically regardless of backend, because they operate on the
  folder, not the harness.

## Relation to other specs

- [../10/c-ant-mcp-runtime.md](../10/c-ant-mcp-runtime.md) — the
  claude-specific runtime that becomes the first `Backend`
  implementation.
- [../10/b-ant-standalone.md](../10/b-ant-standalone.md) — folder
  shape; this spec adds an optional `backend:` hint.
- [../10/d-ant-image-cutover.md](../10/d-ant-image-cutover.md) —
  image build; a Codex variant follows the same pattern (`ARG
CODEX_VERSION`, similar Dockerfile).
- [../10/e-ant-portability.md](../10/e-ant-portability.md) — fleet
  ops; lockfile and verbs are backend-agnostic.
