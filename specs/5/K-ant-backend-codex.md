---
status: draft
---

# Ant backend abstraction — Codex as second harness

A `Backend` interface inside the in-container agent (`ant/src/`) lets
the runtime drive different agentic harnesses underneath. First
implementation wraps the harness ant ships today (Claude Code via
`@anthropic-ai/claude-agent-sdk`). Second implementation: OpenAI's
Codex via `codex app-server` (JSON-RPC 2.0 over stdio). The MCP
surface above the backend — the agent's `send`/`reply`/`inspect_*`
tools and `submit_turn`, served on runed's per-tenant socket — does
not change.

## Core principle: ant wraps harnesses, never is one

**ant does not implement agent loops.** Model calls, tool execution,
multi-step reasoning, retries, prompt caching, session state — the
whole agentic part — live in the external harness. ant spawns the
harness, reads its event stream, and reports one turn result over
`submit_turn`. The `Backend` is the seam between "ant's per-turn
contract" and "one harness's wire protocol."

Consequences:

- No "API-direct" backend. Calling Anthropic/OpenAI APIs and running
  our own loop is not a backend.
- No vendor-neutral layer unifying disparate provider APIs. LiteLLM
  is a different product.
- A harness with no clean wire protocol (TUI-only, one-shot only,
  SIGINT-interrupt) is not a candidate.
- A harness that lacks features ant relies on the harness for (skills,
  MCP client, file tools, permission modes) is out of scope.

## Where the seam sits

The shipping runtime (`ant/src/index.ts`) does exactly four things the
`Backend` abstracts:

1. **Spawn a session** — today `query({ prompt: stream, options })`
   from the SDK, which spawns `claude --output-format stream-json
--input-format stream-json`.
2. **Feed user turns** — push text onto the `MessageStream`
   async-iterable (initial prompt + IPC-steered mid-turn messages).
3. **Consume the harness event stream** — the `for await` loop over
   SDK messages: `system/init` (capture `session_id`), `assistant`
   (track `uuid` for resume), `result` (the turn-terminating event
   carrying text + `modelUsage`).
4. **Report the turn** — on the `result` event, call `submitTurn(...)`
   with `{turn_id, session_id, status, result, error, models}`
   (`ant/src/mcp.ts`).

Everything else in `index.ts` — IPC drain, progress nudges, transcript
archiving, secret sanitizing, MCP-server assembly, system-prompt build
— is harness-agnostic and stays in the runtime, above the `Backend`.

## The `Backend` interface

Lives in `ant/src/backend/`. `backend/claude.ts` wraps the current SDK
path; `backend/codex.ts` drives `codex app-server`. The runtime
(`index.ts`) selects one and consumes its event iterator.

```ts
// A Backend spawns one harness session and yields normalized events.
export interface Backend {
  name(): 'claude' | 'codex';
  caps(): Caps;
  // Spawn the harness for a session. The returned Session is live until
  // close(). Throws if the harness binary is missing or fails to start.
  spawn(cfg: SessionConfig): Promise<Session>;
}

// A live harness session. The runtime drives one turn at a time:
// send() one user message, drain events() until an event with
// final:true, repeat for steered follow-ups, then close().
export interface Session {
  events(): AsyncIterable<Event>; // normalized harness output
  send(text: string): void; // push a user turn (initial + steering)
  interrupt(): Promise<void>; // mid-turn stop (close sentinel today)
  close(): void; // end session, kill subprocess
  sessionId(): string | undefined; // harness session id, set after init
}

// Normalized event. type drives runtime behavior; raw is the
// harness-native payload, preserved verbatim for logging.
export interface Event {
  type: 'init' | 'assistant' | 'tool' | 'result';
  raw: unknown;
  sessionId?: string; // set on 'init'
  text?: string; // assistant/result text, best-effort
  final: boolean; // true on the turn-terminating 'result'
  status?: 'success' | 'error'; // set on 'result'
  error?: string; // set on a failed 'result'
  models?: Record<string, ModelUsage>; // set on 'result' (ant/src/mcp.ts)
}

// What the runtime passes in. Mirrors the fields index.ts already
// threads into query(). A backend ignores fields it can't honor;
// caps() declares which.
export interface SessionConfig {
  cwd: string; // HOME = /home/node
  model?: string; // ARIZUKO_MODEL
  resume?: string; // prior harness session id
  resumeAt?: string; // resume anchor (claude: assistant uuid)
  systemPrompt?: string; // buildSystemPrompt() output
  mcpServers: Record<string, McpServerConfig>; // injectMcpEnv() output
  additionalDirs?: string[]; // extraDirs
  env: Record<string, string | undefined>; // sdkEnv (secrets folded in)
}

// What a backend reports up front. The runtime degrades gracefully on
// false (e.g. no live interrupt → close+respawn).
export interface Caps {
  interrupt: boolean;
  multiTurn: boolean; // steer mid-session without respawn
  sessionResume: boolean;
  mcpClient: boolean; // consumes mcpServers natively
  modelUsage: boolean; // reports per-model token/cost on result
}
```

Both claude and codex satisfy every `caps()` field today. The
interface is sized for them; weaker harnesses are allowed but report
`false` and degrade — never silently faked.

`spawn` of an unknown or unconfigured backend is an error, not a
fallback to claude (see § Backend selection).

## Event normalization — the load-bearing part

Each backend maps its native stream onto `Event`. The runtime is
identical for both: it loops `for await (e of session.events())`,
tracks `e.sessionId`, and on `e.final` calls `submitTurn(...)` built
from the event's `status`/`text`/`error`/`models`. There is **no
per-message streaming to the MCP layer** — `submit_turn` is the single
end-of-turn report (`ant/src/index.ts` `deliverTurn`).

claude maps trivially (it already produces these via the SDK):

| `Event`        | claude SDK message                                  | codex `app-server` JSON-RPC                            |
| -------------- | --------------------------------------------------- | ------------------------------------------------------ |
| `init`         | `system` subtype `init` → `session_id`              | `thread/started` → `threadId`                          |
| `assistant`    | `assistant` (track `uuid`)                          | `item/agentMessage/delta` + `item/agentMessage/done`   |
| `tool`         | `assistant` w/ `tool_use` / `user` w/ `tool_result` | `item/mcpToolCall`, `command/exec`, `item/toolResult`  |
| `result` ok    | `result` subtype `success` → `result`, `modelUsage` | `turn/finished` → assembled text + `usage`             |
| `result` error | `result` subtype `error_during_execution`           | `turn/failed` / JSON-RPC error on `turn/start`         |
| `result` retry | `result` subtype `error_max_turns`                  | `turn/finished` w/ truncation reason (runtime retries) |

`raw` preserves the native payload (claude content blocks, codex
items) so stderr logging keeps full fidelity. The runtime's existing
`error_max_turns` retry and `error_during_execution`
session-reset paths key off `status`/`raw`, unchanged.

## Driving `codex app-server`

`backend/codex.ts` spawns `codex app-server` and speaks JSON-RPC 2.0
over its stdio. Lifecycle per session:

1. `spawn`: launch `codex app-server` (subprocess, like the SDK spawns
   `claude`). Send the initialize/setup request carrying `cwd`,
   `model`, `systemPrompt`, and the MCP server list. Start a thread
   (`thread/start`); on `resume`, attach the prior `threadId`.
2. `send(text)`: `turn/start` with the user text. Steered messages
   mid-turn use `turn/steer`.
3. `events()`: read JSON-RPC notifications off stdout, map each to an
   `Event` per the table, emit `final:true` on `turn/finished`.
4. `interrupt()`: `turn/interrupt` RPC.
5. `close()`: end the thread, kill the subprocess.

MCP wiring: codex consumes MCP servers from `~/.codex/config.toml` or
the setup payload. `backend/codex.ts` renders `cfg.mcpServers` into
that format on `spawn`. Because both harnesses are MCP **clients**, the
agent's tool calls reach runed's socket identically — no schema
translation in ant. A harness without MCP-client support would force a
translation layer, i.e. the "ant becomes an agent loop" trap ruled out
above; such a harness is not a candidate.

Auth: claude reads `~/.claude` credentials; codex reads
`~/.codex/auth.json` or `OPENAI_API_KEY`. The backend maps the
ant-level secret (already in `cfg.env`) into the harness's expected
location on `spawn`.

## Backend selection

Infra toggle, env var (per CLAUDE.md: instance-wide infra in env, not
DB). The runtime reads `ARIZUKO_BACKEND` (default `claude`) from the
container env runed sets at spawn. Unknown value → fatal error at
startup, never a silent fallback.

Per-folder backend choice (a folder declaring `backend:` in its
manifest) is deferred — see open questions. Mixed backends across
folders fall out for free once selection is per-spawn: runed sets
`ARIZUKO_BACKEND` per container.

## Folder shape compatibility

The agent folder (PERSONA.md, CLAUDE.md, skills/, MCP.json, secrets/,
workspace/) is claude-shaped: skills are markdown that Claude Code
auto-loads from `.claude/skills/`. Codex does not auto-load
arizuko-shaped skills. v1: the codex backend concatenates relevant
`SKILL.md` bodies into `cfg.systemPrompt` (lossy — no on-demand
activation — but mechanically simple). Reframing skills as MCP tools
both harnesses consume is the higher-fidelity follow-up, taken only if
the codex backend sees real use. The folder shape itself does not
change.

## v1 scope

Ships:

- `ant/src/backend/` with `Backend`/`Session`/`Event`/`Caps`/
  `SessionConfig`. `backend/claude.ts` wraps the current `query()`
  path with zero behavior change; `index.ts` consumes it via the
  interface.
- `backend/codex.ts` driving `codex app-server`: spawn, single turn,
  streaming `events()`, `turn/interrupt`, MCP wiring, tool-use
  round-trip.
- `ARIZUKO_BACKEND` selection; unknown value is fatal.
- v1 skill handling for codex = system-prompt concatenation.

Deferred:

- Per-folder `backend:` manifest hint.
- Skills-as-MCP-tools for codex.
- A third backend (`opencode.ai` is the candidate: MIT, claude-cli-
  shaped, HTTP+SSE — lands without disturbing the interface).
- Codex image variant (separate `ARG CODEX_VERSION` Dockerfile).

## Acceptance

- `backend/claude.ts` passes the existing `src/` test suite with no
  runtime behavior change (the refactor is invisible above the seam).
- With `ARIZUKO_BACKEND=codex`, a turn runs against codex; the agent's
  `submit_turn` reports the same `{status, result, session_id, models}`
  shape; `interrupt()` lands a `turn/interrupt`.
- A real-codex smoke suite under `ant/test/smoke/codex/` mirrors the
  claude one: basic turn, mid-turn steer, interrupt, tool-use
  round-trip through an MCP server. Costs real tokens; opt-in.
- `caps()` for both backends matches the actual surface (no false
  advertising).
- Zero change to runed's MCP socket, the agent's tool surface, or the
  `submit_turn` payload shape.

## Out of scope

- API-direct against any vendor (ruled out by the core principle).
- LiteLLM-style multi-vendor wrappers as a backend.
- Cross-harness protocol translation (each backend speaks one
  harness natively).
- Forking a harness to fix protocol gaps — wait for upstream or pick
  another harness.
- Backend-specific operator verbs. Fleet ops
  ([../8/7-ant-portability.md](../8/7-ant-portability.md)) operate on
  the folder, not the harness, so they are backend-agnostic.

## Why Codex (decision record)

Surveyed against Open Interpreter and `opencode.ai`. Codex wins on
every operational axis: documented JSON-RPC protocol, first-class
`turn/interrupt` + `turn/steer`, structured tool-use, native
MCP-client, Apache-2.0, active maintenance, and a loop shape
near-isomorphic to claude's. Open Interpreter eliminated: stalled
(last release Oct 2024), AGPL-3.0, no documented CLI protocol, no
first-class interrupt. `opencode.ai` is the strong third candidate
held for later.

Two **economic** axes gate any harness past the operational bar — they
decide whether running it at arizuko's turn volume is affordable, not
whether it works:

1. **Prompt caching.** Caching is the harness's job (§ Core principle).
   A harness that re-sends the full context uncached every turn burns
   tokens linearly with conversation depth; one that caches well is the
   difference between viable and not. Codex caches natively; claude
   does via the SDK.
2. **Subscription-plan auth.** The harness must authenticate via a
   login/plan (ChatGPT plan for codex's `~/.codex/auth.json`; the
   Claude subscription for claude) — not only a per-token API key.
   Plan-based billing is flat; per-token API billing scales with every
   turn and is the expensive path at fleet volume.

`opencode.ai` and any later candidate are held to both. A harness that
clears the operational bar but caches poorly or is API-key-only is not
adopted until that changes.

## Open questions

1. **Per-spawn vs per-folder selection.** Env-per-spawn ships in v1.
   A folder-level `backend:` hint (resolved by runed, env still the
   transport) is specified when the folder layout
   ([../12/b-ant-standalone.md](../12/b-ant-standalone.md)) gains the
   slot.
2. **Codex protocol stability.** `app-server` is younger than claude's
   stream-json; field-level breakage between releases is likely. Pin
   `CODEX_VERSION` in the codex image variant when it exists.

## Relation to other specs

- [../12/c-ant-mcp-runtime.md](../12/c-ant-mcp-runtime.md) — the
  planned Go runtime rewrite. If it lands, the `Backend` seam ports
  to Go unchanged in shape (this spec defines the seam, not the
  language).
- [../12/b-ant-standalone.md](../12/b-ant-standalone.md) — folder
  shape; this spec adds an optional `backend:` hint (deferred).
- [../12/d-ant-image-cutover.md](../12/d-ant-image-cutover.md) — image
  build; a codex variant follows the same pattern.
- [P-runed.md](P-runed.md) — owns the per-tenant MCP socket and the
  `submit_turn` → `POST /v1/turns/{turn_id}/result` forward. The
  backend sits below all of it.
- [../8/7-ant-portability.md](../8/7-ant-portability.md) — fleet ops;
  backend-agnostic.
