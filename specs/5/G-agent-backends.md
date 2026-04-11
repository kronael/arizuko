---
status: draft
---

# G — Alternative Agent Backends

Support pluggable agent backends in `ant` so operators can run OpenAI
Codex CLI or pi-coding-agent instead of Claude Code. The container
protocol (stdin JSON → stdout delimited output) stays fixed; the backend
is a runtime choice.

## Background

`ant/src/index.ts` currently embeds `@anthropic-ai/claude-agent-sdk`
directly. The SDK's `query()` async iterator drives the conversation.
IPC follow-up messages are injected through two complementary paths:
a `PostToolUse` hook (`createIpcDrainHook`) drains the IPC input dir
between tool calls and returns queued messages as
`hookSpecificOutput.additionalContext`, appended to the tool result
Claude is about to read — achieving true mid-loop injection inside the
same turn. For text-only turns (no tool calls fire the hook),
`pollIpcDuringQuery` falls back to `stream.push(text)` on the
`MessageStream`, which queues the message as the next user turn rather
than interrupting the active agentic loop. A `draining` boolean mutex
shared between the hook and the poll timer prevents double-draining the
same files. Two additional SDK hooks handle transcript archiving
(PreCompact) and bash secret scrubbing (PreToolUse/Bash).

Alternative backends were evaluated for multi-provider support and reduced
Anthropic lock-in. Two candidates reached research depth:

## Candidate A — OpenAI Codex CLI

**Invocation:**

```bash
CODEX_API_KEY=<key> codex exec --json --ephemeral --full-auto \
  --sandbox danger-full-access \
  -c model_instructions_file=/tmp/system.md \
  - < /dev/stdin
```

**Output format:** NDJSON on stdout.

```json
{ "type": "item.completed", "item": { "type": "agent_message", "text": "..." } }
```

**MCP:** native. Config via `~/.codex/config.toml` or `-c` inline:

```toml
[mcp_servers.arizuko]
command = "socat"
args = ["STDIO", "UNIX-CONNECT:/workspace/ipc/gated.sock"]
```

The socat bridge pattern is identical to the current SDK setup — no
adapter code needed for MCP.

**Session resume:** `codex exec resume --last "follow-up"` spawns a new
process continuing the most recent session file.

**Fit analysis:**

| Feature                | Status                                                          |
| ---------------------- | --------------------------------------------------------------- |
| Headless one-shot      | ✓ `codex exec --json -`                                         |
| Mid-turn IPC injection | ✗ one-shot per invocation; each IPC message needs a new process |
| MCP (unix socket)      | ✓ native, socat bridge                                          |
| Session resume         | △ file-path based, not UUID                                     |
| System prompt          | △ must write temp file; `-c model_instructions_file=`           |
| Bash secret scrubbing  | △ shell wrapper: `unset ANTHROPIC_API_KEY; exec bash "$@"`      |
| PreCompact hook        | ✗ not available; post-run parsing workaround only               |
| Models                 | OpenAI GPT family only                                          |
| Maturity               | Production, OpenAI-backed                                       |

**Blocker:** mid-turn IPC injection is a core arizuko feature (follow-up
messages drained by a PostToolUse hook get appended to the next tool
result inside the active agentic loop). Codex exec is one-shot — each
follow-up requires respawning, with no hook surface for mid-loop
injection. This breaks session continuity and adds latency. Workable but
inferior to the current model.

## Candidate B — pi-coding-agent RPC mode

**npm:** `@mariozechner/pi-coding-agent`

**Invocation:**

```bash
pi --mode rpc --no-session
```

Communicates over stdin/stdout using strict NDJSON (LF delimiter).

**Commands sent to stdin:**

```json
{"type": "prompt", "message": "task prompt"}
{"type": "steer", "message": "follow-up mid-turn"}
{"type": "set_model", "provider": "anthropic", "modelId": "claude-sonnet-4-20250514"}
{"type": "abort"}
```

**Events received from stdout:**

```
agent_start / agent_end
message_update  (stream delta — concatenate for full text)
tool_execution_start / tool_execution_end
```

**MCP:** not built-in. Options:

1. Write a pi extension (~80 lines TypeScript) using `pi.registerTool()`
   to proxy calls to the arizuko socat bridge.
2. `pi-mcp-adapter` (nicobailon) — lazy-loading proxy extension for
   stdio/HTTP transports; needs `socat` wrapper for unix socket.

**Fit analysis:**

| Feature                | Status                                                                                       |
| ---------------------- | -------------------------------------------------------------------------------------------- |
| Headless RPC           | ✓ `pi --mode rpc`                                                                            |
| Mid-turn IPC injection | ✓ `steer` command — true mid-turn, closer than `MessageStream.push()` which queues next-turn |
| MCP (unix socket)      | △ requires extension or pi-mcp-adapter + socat wrapper                                       |
| Session resume         | ✓ `switch_session` RPC command                                                               |
| System prompt          | △ must write `.pi/SYSTEM.md` before spawn                                                    |
| Bash secret scrubbing  | △ shell wrapper same as Codex                                                                |
| PreCompact hook        | ✗ not available                                                                              |
| Models                 | ✓ 15+ providers including Anthropic, OpenAI, Google                                          |
| Maturity               | Community, solo developer (`badlogic/pi-mono`)                                               |

**Advantage:** `steer` is a first-class mid-turn injection primitive —
arguably stronger than Claude SDK, where `MessageStream.push()` only
queues next-turn and we have to use a `PostToolUse` hook workaround for
true mid-loop delivery. One long-lived process per container run,
matching the current architecture. Multi-provider means a single ant
image can run Claude, GPT, or Gemini via config.

**Risk:** solo developer project; no Anthropic or OpenAI backing. API
surface may break across versions.

## Comparison

| Concern             | Claude SDK (current) | Codex CLI        | Pi RPC             |
| ------------------- | -------------------- | ---------------- | ------------------ |
| Mid-turn injection  | PostToolUse hook     | Requires respawn | `steer` command    |
| MCP (unix socket)   | native               | native           | extension required |
| Models              | Anthropic only       | OpenAI only      | 15+ providers      |
| PreCompact/hooks    | native               | shell workaround | shell workaround   |
| Secret isolation    | `env` SDK option     | shell wrapper    | shell wrapper      |
| Session persistence | UUID                 | file path        | `switch_session`   |
| Maturity            | high                 | high             | medium             |

## Implementation path

`ant/src/index.ts` hardcodes `query()` from the Anthropic SDK. Adding
backends requires extracting the backend into a driver interface:

```typescript
interface AgentDriver {
  run(prompt: string, opts: RunOptions): AsyncIterator<AgentEvent>;
  steer(text: string): void; // mid-turn injection
  abort(): void;
  sessionId(): string | undefined;
}
```

The `runQuery()` function loops over `AgentDriver.run()` events instead of
SDK messages. `MessageStream` is replaced by `steer()` calls on the driver.

Backend selection: `AGENT_BACKEND=claude|codex|pi` env var in container,
defaulting to `claude`.

Estimated work per backend:

- Claude: refactor current code into driver interface (~no net new code)
- Pi: spawn subprocess, read NDJSON events, write commands, write MCP
  extension (~200 lines TypeScript)
- Codex: spawn subprocess, read NDJSON events, write temp config files,
  handle per-IPC respawn (~150 lines TypeScript + regression on injection)

## Hooks workaround for non-Claude backends

PreCompact: run a post-session archiver that reads the backend's session
log (Codex writes `~/.codex/sessions/`; Pi writes to `--session` path).
PreToolUse/Bash: wrap bash with a shell script that unsets secret vars
before exec; inject via `AGENT_BASH_WRAPPER=/bin/bash-clean` env.

## Decision

Not shipping. Reasons:

1. Claude SDK is the best structural fit (native hooks, mid-turn injection,
   MCP).
2. Pi RPC is architecturally close but requires a custom MCP extension and
   carries maturity risk.
3. Codex is production-grade but the one-shot model degrades the IPC
   injection pattern — a core arizuko feature.
4. Multi-provider support is better served by Claude's own model switching
   than by changing the agent harness.

**Revisit if:** Anthropic SDK deprecated, operator request for GPT/Gemini
agent, or pi-coding-agent gains a stable MCP extension ecosystem.

## References

- [Codex CLI — Non-interactive mode](https://developers.openai.com/codex/noninteractive)
- [Codex CLI — MCP docs](https://developers.openai.com/codex/mcp)
- [pi-mono GitHub](https://github.com/badlogic/pi-mono)
- [pi RPC protocol](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/rpc.md)
- [pi-mcp-adapter](https://github.com/nicobailon/pi-mcp-adapter)
