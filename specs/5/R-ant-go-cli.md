---
status: draft
---

# R — ant as a Go wrapper around the Claude Code CLI

Replace the TypeScript `ant/` container runner with a thin Go binary that
shells out to the `claude` CLI. The container contract (stdin JSON →
stdout delimited output) stays identical; only the harness language and
the Claude integration layer change.

## Motivation

- Repo alignment: everything else in arizuko is Go. `ant/` is the lone TS
  island with `node_modules`, a separate Dockerfile, and a separate build
  toolchain (bunx tsc). Porting eliminates the duplication.
- Single-SDK footprint: the operator box already has Go. No reason to ship
  Node runtime + Agent SDK just to drive a subprocess agent.
- Thinner dependency graph: currently `@anthropic-ai/claude-agent-sdk` is
  a hard dep. The SDK is feature-rich but mostly wraps the same CLI we
  can invoke directly. Dropping the SDK removes one versioned tie to
  Anthropic's TS release cadence.
- Closer to the "just another binary" design in the MEMORY note — the
  agent container becomes a ~200-line Go program that could, in principle,
  be swapped for any CLI agent that speaks `--output-format stream-json`.
- **Not goal**: multi-provider support. That is spec G. This spec is a
  language/runtime swap for the Claude case only.

## Current behavior (baseline)

`ant/src/index.ts` (~640 lines):

1. Reads `ContainerInput` JSON from stdin.
2. Loads group files (soul, system.md, sessions index).
3. Constructs a `query({ … })` call against `@anthropic-ai/claude-agent-sdk`:
   - `allowedTools`: full bash/fs/mcp allowlist
   - `permissionMode: 'bypassPermissions'`
   - `mcpServers`: unix-socket config pointing at `/workspace/ipc/gated.sock`
     via `socat`
   - `hooks.PreCompact`: archives transcript at compaction
   - `hooks.PreToolUse.Bash`: strips secret env vars from bash tool
     invocations
4. Wraps prompt + follow-up messages in a `MessageStream` (AsyncIterable)
   so `isSingleUserTurn = false` — keeps the SDK from auto-terminating
   after the first prompt. `stream.push(text)` queues follow-up IPC
   messages as the next user turn (not true mid-turn — see open
   question 1).
5. Reads subsequent IPC follow-up messages from a fifo/pipe. A
   `PostToolUse` hook (`createIpcDrainHook`) is the exclusive
   message-drain path — it drains the IPC input dir between tool calls
   and returns queued messages as
   `hookSpecificOutput.additionalContext` for mid-loop injection inside
   the current turn. `pollIpcDuringQuery` only checks the `_close`
   sentinel. For text-only responses (no tool calls),
   `checkIpcMessage()` picks up steered messages at the next query
   start.
6. Streams SDK events, forwards text to stdout between
   `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---` markers,
   records the new session id, writes a `ContainerOutput` footer.

## Target behavior

Replace step 3-6 with a Go process that spawns `claude` CLI with
`--output-format stream-json` and `--input-format stream-json` so we
can still push follow-up messages as subsequent user turns, plus a
`PostToolUse` hook script for true mid-loop injection between tool
calls (see open question 1). Everything else (container contract,
group file loading, output delimiters, ContainerOutput) is unchanged.

```
stdin (ContainerInput JSON)
  → ant (Go)
    → reads group files, constructs system prompt
    → spawns: claude -p --output-format stream-json --input-format stream-json
              --permission-mode bypassPermissions
              --mcp-config /tmp/mcp.json
              --allowed-tools "..."
              [--resume <sessionId>]
    → writes first user turn as stream-json to claude's stdin
    → ranges over claude's stdout stream-json events
      → on assistant text delta: write to our stdout between ARIZUKO markers
      → on system init: capture session_id
      → on tool_use / tool_result: optional log
    → concurrently: ranges over IPC follow-up pipe
      → on each message: writes another user turn to claude stdin
    → on claude exit: emits ContainerOutput footer
```

## Open design questions

1. **Mid-turn injection semantics.** _Answered._ There is no native
   mid-turn injection primitive in the Claude Agent SDK (or CLI):
   `stream.push` on a `MessageStream` queues as the next user turn, it
   does not steer a turn already in flight. Upstream has an open feature
   request for native token-level real-time steering
   (anthropics/claude-code#30492). Our workaround, implemented in
   `ant/src/index.ts` as `createIpcDrainHook`, registers a `PostToolUse`
   hook that drains the IPC input directory and returns the queued
   messages as `hookSpecificOutput.additionalContext`. The SDK appends
   that context to the tool result Claude is about to read, giving us
   mid-loop injection _inside the same turn_ — between tool calls.
   This is the primary and exclusive message-drain path.
   `pollIpcDuringQuery` only checks the `_close` sentinel now — it no
   longer drains user messages. For text-only responses (no tool calls),
   steered messages are picked up at the next query start via
   `checkIpcMessage()`, which runs between queries in the main loop.
   The ant-go port must preserve both paths — wire a `PostToolUse` hook
   script that drains IPC and prints
   `hookSpecificOutput.additionalContext`, and fall back to checking for
   IPC messages between queries when no tool call fired during the turn.
2. **Hooks: PreCompact and PreToolUse/Bash.** The CLI supports hooks via
   `~/.claude/settings.json` (`hooks` key). Port the two current hooks to
   standalone scripts:
   - `PreCompact` → Go binary or shell script that copies the transcript
     file to `${GROUP}/sessions/<sid>.jsonl`. Wire via
     `hooks.PreCompact[].command`.
   - `PreToolUse.Bash` → shell wrapper that reads the tool input JSON from
     stdin, strips `ANTHROPIC_API_KEY`/other secrets, emits the modified
     JSON. Wire via `hooks.PreToolUse[].matcher=Bash`.
3. **MCP config format.** CLI takes `--mcp-config <file>` pointing at a
   JSON file with `{ mcpServers: { … } }`. Same shape as the SDK option;
   just serialize it and point at `/workspace/ipc/gated.sock` via socat.
4. **Session resume.** CLI supports `--resume <sessionId>` for continuing
   a prior session. Need to confirm the CLI-persisted session store
   (`~/.claude/projects/<slug>/<sid>.jsonl`) survives container restarts
   with the current mount layout. If not, ant-go needs to materialize
   session transcripts into the expected path before spawn.
5. **Stream-json event schema.** The CLI's `stream-json` output is not
   versioned. Pin to a CLI version in the Dockerfile and add a smoke test
   that decodes the key event types: `system` (init), `assistant`
   (content deltas), `tool_use`, `tool_result`, `result` (final). If the
   schema drifts, the smoke test catches it at build time.
6. **Error surface.** TS SDK raises structured errors; CLI errors come as
   exit code + stderr + possibly a `result` event with error status. Need
   a clean mapping to the current `ContainerOutput.error` string.

## Scope

- New: `ant-go/main.go`, `ant-go/Dockerfile` (multi-stage, final image
  installs `claude` CLI + socat + ca-certs + the Go binary).
- New: `ant-go/hooks/precompact.sh` (or `.go`), `ant-go/hooks/bash-clean.sh`.
- New: `ant-go/main_test.go` — stream-json decode smoke test against a
  golden event log.
- Modified: `container/runner.go` — switch image name from `arizuko-ant`
  to `arizuko-ant-go` (or flag-gate during rollout).
- Modified: `ant/Makefile` + root `Makefile` — add `ant-go` image target.
- Unchanged: container contract, group file layout, ContainerInput/Output
  JSON shapes, MCP socket path, IPC follow-up pipe, skill/session file
  locations. Existing TS `ant/` stays in-tree until ant-go ships green for
  a full rollout.

## Cutover plan

1. Build ant-go alongside existing ant.
2. Add `AGENT_IMAGE` env var (default `arizuko-ant:latest`, override to
   `arizuko-ant-go:latest`).
3. Deploy ant-go on a single spawn group first (e.g. REDACTED's root).
4. Soak for N days comparing session_log results, token usage, hook
   behavior (transcript archives, bash secret stripping).
5. Promote to REDACTED + REDACTED.
6. Delete `ant/` once no instances reference it.

## Non-goals

- Alternative backends (Codex, Pi, Gemini) — handled by spec G.
- Using the Anthropic Go SDK directly — would require reimplementing the
  entire agent loop (tool execution, hooks, MCP clients, subagents,
  compaction). Explicitly out of scope; the point of this spec is to
  stay on the CLI.
- Behavior changes. The CLI output should be byte-compatible with the
  current TS harness output modulo session id handling.

## Risks

- CLI schema drift: stream-json is undocumented-stable. Pin the version.
- Hook semantics: PreCompact + PreToolUse on the CLI have slightly
  different invocation conventions than the SDK. Validate in spike.
- Mid-turn injection: if CLI queues rather than interrupts, responsiveness
  on long agent turns degrades. Measure.
- Cold-start cost: spawning `claude` CLI per run vs keeping a Node process
  resident. The current ant already spawns per run (container lifecycle
  is one-shot), so no regression.

## References

- Current ant: `ant/src/index.ts`
- Related spec: [G — Alternative Agent Backends](G-agent-backends.md)
- MEMORY note: "agent container should be 'just another binary' — thin
  wrapper around Claude Code CLI, not tightly coupled to the TS SDK"
- Claude Code CLI docs: `claude --help` (stream-json modes, hooks,
  mcp-config, resume)
