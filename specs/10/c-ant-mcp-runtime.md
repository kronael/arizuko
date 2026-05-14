---
status: unshipped
---

# Ant runtime — MCP front, NDJSON claude driver

The Go binary that replaces `ant/src/index.ts`. One process, two
contracts: an **MCP server** facing callers, and the local **`claude`
CLI** driven over its bidirectional NDJSON protocol. No `-p`. No TUI.
No SDK dependency.

Sibling to [b-ant-standalone.md](b-ant-standalone.md) — that spec
defined the folder layout and CLI shape; this spec defines the
runtime that sits behind every IO mode.

## Why this exists

Three reasons the current TS runtime gets retired:

1. **The TS runtime is a thin SDK shim.** `ant/src/index.ts` calls
   `@anthropic-ai/claude-agent-sdk`'s `query()`. The SDK itself just
   spawns `claude` with `--output-format stream-json --verbose
--input-format stream-json` and pipes NDJSON. No part of the SDK
   is a value-add we can't replicate in ~500 lines of Go.

2. **MCP is the right interface for "talk to an agent".** Every
   consumer arizuko currently wires (gateway, scheduler, chat CLI,
   future webd plugins) wants the same three operations — send a
   user message, receive a streaming reply, interrupt mid-stream.
   MCP defines exactly that contract and gives us free transport
   pluggability (stdio / unix socket / SSE / TCP) and ecosystem
   tooling (clients in every language, Inspector, evals).

3. **One unified model interface across arizuko.** Today: `ant/`
   speaks SDK, `container.Runner` speaks JSON-on-stdin, gateway
   speaks something else again. After this: every component that
   wants a model speaks MCP to one ant socket. Replacing the
   provider means replacing one server, not N call sites.

## The two layers

```
                  MCP transport (stdio | unix | tcp | sse)
                                 │
                  ┌──────────────▼──────────────┐
                  │   MCP server (tools, prog. notifs) │
                  └──────────────┬──────────────┘
                                 │ in-process
                  ┌──────────────▼──────────────┐
                  │  Session manager (multi-turn, resume)  │
                  └──────────────┬──────────────┘
                                 │ pipes (stdin/stdout NDJSON)
                  ┌──────────────▼──────────────┐
                  │  claude subprocess (per session)       │
                  │  --input-format stream-json            │
                  │  --output-format stream-json --verbose │
                  └─────────────────────────────┘
```

One claude subprocess per session. Sessions are addressable by ID;
multiple may run concurrently in one ant process. The MCP server is
a fan-in/fan-out router; nothing in the agent loop lives there.

## The claude driver — wire facts

Pinned: `claude` CLI ≥ 2.1.119. Schema is undocumented; pin the
version in `ant/go.mod` build tags or `Dockerfile.ant`.

**Spawn argv**:

```
claude --input-format stream-json
       --output-format stream-json
       --verbose
       --model <sonnet|opus|haiku>
       [--session-id <uuid>]
       [--resume <session-id>]
       [--mcp-config <path>]
       [--permission-mode bypassPermissions]
       [--add-dir <path>]...
```

Env: `CLAUDE_CODE_ENTRYPOINT=ant`.

**Inputs (stdin, one JSON per line)** — observed shapes:

User message:

```json
{
  "type": "user",
  "message": { "role": "user", "content": [{ "type": "text", "text": "..." }] },
  "parent_tool_use_id": null,
  "session_id": ""
}
```

Control request (interrupt is one of several subtypes):

```json
{
  "request_id": "<uuid>",
  "type": "control_request",
  "request": { "subtype": "interrupt" }
}
```

Other observed `subtype`s in the SDK source: `set_permission_mode`,
`set_model`, `set_max_thinking_tokens`, `mcp_reconnect`, `mcp_toggle`,
`can_use_tool`, `message_rated`, `remote_control`.

**Outputs (stdout, one JSON per line)** — observed types:

- `system` (subtype `init`) — once on startup; carries `session_id`,
  model, tools, mcp_servers, slash_commands, permission mode
- `rate_limit_event` — informational
- `assistant` — `{message:{content:[{type:"thinking"|"text",...}], usage, ...}}`
  emitted per content block as it completes; the same `message.id`
  is reused, content grows monotonically
- `tool_use`, `tool_result` (observed when tools fire — TODO: capture
  exact shapes against a tool-using prompt)
- `result` (subtype `success` | `error`) — turn complete; carries
  final `result` string, `duration_ms`, `usage`, `total_cost_usd`,
  `stop_reason`, `terminal_reason`, `session_id`
- `control_response` — matches `control_request` by `request_id`
- `keep_alive` — heartbeat, ignore

**End-of-turn = `result` event.** Not a timing heuristic. Atomic.

**Interrupt protocol**: write `control_request{subtype:"interrupt"}`,
wait for matching `control_response{subtype:"success"|"error"}` on
stdout, then continue draining until the corresponding `result`
event arrives (the in-flight turn ends, possibly with
`terminal_reason:"interrupted"` — verify on first integration).

**Process lifecycle**: close stdin → claude flushes and exits. SIGTERM
fallback with 5s grace, then SIGKILL. Stderr is captured into a ring
buffer and attached to error responses.

## The MCP front — surface

Transports (initial set):

- **stdio** — default; for `ant <folder> --mcp` in a process pipe
- **unix socket** — `--socket=/path/to/.sock`; for arizuko's gated
  → ant call path (replaces today's `ipc.ServeMCP` socket)
- **tcp** — `--tcp=127.0.0.1:PORT`; for local dev / IDE-style clients

(SSE/HTTP transport: deferred; add when a non-local consumer needs it.)

### Tools

| Tool                                                | Purpose                                                | Streaming |
| --------------------------------------------------- | ------------------------------------------------------ | --------- |
| `session.create`                                    | spawn a new claude subprocess; returns `session_id`    | no        |
| `session.close`                                     | terminate one session                                  | no        |
| `session.list`                                      | list active sessions in this ant process               | no        |
| `session.send`                                      | send a user message to a session; stream the reply     | **yes**   |
| `session.interrupt`                                 | issue a `control_request{interrupt}` against a session | no        |
| `session.set_model` / `session.set_permission_mode` | proxy to control_request                               | no        |

### Streaming model

`session.send` is the only streaming call and uses MCP's standard
`progressToken` pattern. The client passes
`_meta.progressToken` in the `tools/call` params; the server emits
`notifications/progress` per assistant event, then returns the final
`tools/call` result when the `result` event arrives.

**Per-token streaming**: pass-through of the raw NDJSON event types
in each progress notification's `data` field. Clients that want plain
text use the helper resource `extract_text` (see below); clients that
want full fidelity (thinking blocks, tool uses, usage stats) get
everything.

```jsonc
// tools/call request
{"name":"session.send",
 "arguments":{"session_id":"abc","text":"hello"},
 "_meta":{"progressToken":"send-1"}}

// notifications/progress (one per assistant/tool_use/system event)
{"method":"notifications/progress",
 "params":{"progressToken":"send-1",
           "data":{"type":"assistant","message":{...}}}}

// tools/call response (when result event arrives)
{"content":[{"type":"text","text":"<final text>"}],
 "structuredContent":{"result":"<...>", "usage":{...},
                      "cost_usd":..., "stop_reason":"end_turn"}}
```

**Interrupt while streaming**: caller invokes `session.interrupt`
on the same session. The in-flight `session.send` will receive a
final `result` event with `terminal_reason:"interrupted"` and the
streaming call completes normally (no error). Cancellation by the
MCP client's standard `notifications/cancelled` is also honored —
it triggers the same interrupt path.

### Resources

- `session://<id>/transcript` — append-only JSONL log of all NDJSON
  events for the session (for replay, eval, debugging)
- `session://<id>/info` — last `system/init` payload + current state

### Prompts

None initially. Adding canned prompts is a folder concern, not a
runtime concern.

## Session model

- One `claude` subprocess per session. Multi-turn naturally — write
  another user message after the prior `result`, same process.
- `session.create` accepts `{model, cwd, system_prompt?,
permission_mode?, mcp_config?, session_id?, resume?}`. Default cwd
  is the ant-folder's `workspace/`. `session_id` is a stable handle
  the caller can supply to enable resume across ant restarts (passed
  to claude as `--session-id` or `--resume`).
- Each session has its own stderr ring buffer (8KB) and transcript
  log (rotated at 10MB).
- Concurrency cap: `--max-sessions` flag (default 32).
- Idle timeout: `--session-idle-timeout` (default 30m); idle session
  is closed and its `session_id` becomes resumable.

## Folder integration

When ant runs `<folder> --mcp`, the folder's contents shape every
session created on the socket:

- `SOUL.md` + `CLAUDE.md` are injected as the system prompt
- `skills/` is mounted at `<cwd>/.claude/skills/`
- `diary/` is mounted at `<cwd>/.claude/diary/`
- `secrets/` is read at startup; entries become env vars on the
  `claude` subprocess
- `MCP.json` becomes `--mcp-config` (after secret expansion)
- `workspace/` is the cwd

The MCP server is per-folder. One ant process = one folder = one
socket. Multi-folder hosting is out of scope; run multiple ant
processes.

## Where this code lives — initial → final

**Initial** (this pass): `/home/onvos/app/claude-serve/` becomes the
incubator for the Go runtime. The Python work there (`ClaudeStream`,
the smoke tests) is removed. The Go module is laid out as:

```
claude-serve/
  go.mod
  cmd/ant/main.go              — CLI entrypoint (mirrors ant/cmd/ant)
  internal/claude/             — NDJSON driver (subprocess, events,
                                 control_request, transcript)
  internal/session/            — session manager, lifecycle, resume
  internal/mcp/                — MCP server (stdio/unix/tcp), tools,
                                 progress notifications
  internal/folder/             — ant-folder loader (SOUL, skills, etc.)
  testdata/                    — captured NDJSON fixtures from real
                                 claude runs (recorded, not faked)
  test/smoke/                  — real-`claude` integration tests
```

Why incubate outside arizuko: faster iteration, no arizuko build
deps, sharper repo boundary. The package paths use a transient
module name (`github.com/onvos/claude-serve`).

**Final**: when stable, the packages move under `arizuko/ant/`
matching the layout in `b-ant-standalone.md`:

- `internal/claude/` → `ant/pkg/runtime/`
- `internal/session/` → `ant/pkg/runtime/session/`
- `internal/mcp/` → `ant/pkg/mcp/`
- `internal/folder/` → `ant/pkg/agent/` (merges with existing loader)
- `cmd/ant/` → `ant/cmd/ant/` (replaces the stub)

Repo move = `git mv` + import path rewrite + go.mod merge. No public
API change. The `claude-serve` repo is then archived.

## Replacement plan for `container.Runner`

Today: `container/runner.go:DockerRunner.Run(...)` spawns
`docker run … ant:latest`, pipes a `ContainerInput` JSON in, reads
`ContainerOutput` JSON out, monitors stderr for `[ant]` markers.

After: the `ant:latest` image's ENTRYPOINT becomes the new Go binary.
`DockerRunner.Run` is **unchanged** — same stdin shape, same exit
codes, same stderr marker contract. The runner is talking to a Go
binary instead of a TS one; that's the only difference. The MCP
socket is hosted _inside_ the container at `/workspace/ipc/ant.sock`
and arizuko's gated bridges into it (the current
`ipc.ServeMCP(/workspace/ipc/<group>.sock)` socket is on the _other_
side — that's gated's MCP serving its tools _to_ the agent, and is
unchanged).

So this spec touches one binary swap. The runner interface, the
gated MCP socket, the gateway, and every consumer above stays.

## Acceptance

1. `make smoke` in claude-serve drives a real `claude` CLI through
   the Go driver, exercises send / streaming read / interrupt /
   multi-turn / resume, costs <$0.10 per full run.
2. `ant <folder> --mcp --socket=/tmp/ant.sock` exposes the documented
   tool set; MCP Inspector connects and can call `session.send`
   with progress streaming.
3. The TS runtime in `ant/src/` is removed and the `ant:latest`
   image ENTRYPOINT is the new Go binary.
4. `container.Runner` consumers (gateway, gated) work unchanged on
   one canary group folder for a full day.
5. `ant/`'s Go code has zero arizuko-internal imports (same
   orthogonality test as `crackbox/` and current `ant/`).

## Out of scope (this spec)

- Sandbox backends (`pkg/host`) — orthogonal; covered by b-ant-standalone.md
- Multi-folder hosting — one ant = one folder
- Provider abstraction over OpenAI / Gemini / Ollama — the goal is
  unified _interface_, not unified _backend_. Single backend: local
  `claude` CLI. A provider-switch can be added later by replacing
  the `internal/claude/` package while keeping the MCP surface.
- Authentication on non-stdio transports — unix socket is filesystem-
  permission-gated; tcp is dev-only and bound to 127.0.0.1. JWT-style
  auth deferred.
- SSE / HTTP transport — add when a remote consumer needs it.
- Web UI / Inspector skin — use the upstream MCP Inspector for now.

## Open questions

1. **Pin `claude` version where.** Build-tag in Go, `Dockerfile.ant`,
   or a runtime version check that refuses to spawn unknown versions?
   The NDJSON schema is undocumented and could shift on any minor
   release.
2. **Tool-use event shape** — observed `assistant` events well; have
   not yet captured a real run that uses `Bash`/`Read`/`Edit` to lock
   the `tool_use` and `tool_result` shapes. First implementation
   should record fixtures from a tool-using prompt.
3. **Resume semantics across ant restarts.** `--resume <sid>` works
   inside a running claude; if ant dies, does the next ant invocation
   with the same `session_id` get the same context? Verify before
   exposing `session.resume` in the MCP surface.
4. **Progress notification volume.** Per-event streaming can mean
   one notification per token under partial-message mode. Cap or
   coalesce? `--include-partial-messages` is opt-in; default off
   keeps it to one notification per content block.
5. **Permission prompts (`can_use_tool`)** — claude can send a
   `control_request{subtype:"can_use_tool"}` to ask permission for
   a tool call. The MCP server needs to surface this _back to the
   MCP caller_ as a separate notification or elevation prompt. First
   pass: hard-allow via `--permission-mode bypassPermissions` for
   in-container runs; defer interactive permission surface.
6. **Cost accounting** — `result` events carry `total_cost_usd`. Do
   we surface it per-call (in `structuredContent`), aggregate per
   session, or both? Likely both.
7. **stdin closure semantics** — does writing `null` or specific
   sentinel cleanly end a turn without ending the session? Today we
   just wait for the `result` event; investigate whether explicit
   end-of-turn is needed for some edge cases.

## Relation to other specs

- [b-ant-standalone.md](b-ant-standalone.md) — defines the
  folder/CLI shape this runtime sits behind. Open questions there
  (CLAUDE.md collision, binary naming, root Makefile wiring) carry
  over.
- [../9/](../9/) — `mcp-fw` / standalone hardening. The MCP server
  here is the same protocol; reuse any shared MCP client/server
  helpers if they land first.
- [../8/12-crackbox-sandboxing.md](../8/12-crackbox-sandboxing.md) —
  `--sandbox=crackbox` consumes this runtime's CLI surface; no
  protocol-level coupling.
