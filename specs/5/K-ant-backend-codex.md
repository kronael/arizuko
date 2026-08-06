---
status: partial
---

# Ant backend abstraction — Codex as second harness

> **Status (2026-08-06).** Partial. `claude.ts` now has `claude.test.ts`
> (normalize covered row-by-row against the mapping table below) and
> `ARIZUKO_BACKEND` is documented in `ant/README.md` + `EXTENDING.md`. The
> graceful-degradation contract was **not** built: on inspection it describes a
> fallback from a live-steering path the runtime does not have, and the
> "degraded" behavior it names is what the runtime already does
> unconditionally — see § `Caps` below, rewritten to match the code. That
> leaves `Caps` and four `Session` methods with no consumer; whether to wire or
> delete them is a contract change awaiting sign-off (BUGS `F6`).

A `Backend` interface inside the in-container agent (`ant/src/backend/`)
lets the runtime drive different agentic harnesses underneath.
`backend/claude.ts` wraps the harness ant shipped first (Claude Code via
`@anthropic-ai/claude-agent-sdk`); `backend/codex.ts` drives OpenAI's
`codex app-server` (JSON-RPC 2.0 over stdio). The MCP surface above the
backend — `send`/`reply`/`inspect_*` and `submit_turn`, served on the
per-turn unix socket **routd hosts in-process** (`routd/mcp.go`
`ServeTurnMCP`; runed only mounts the ipc dir) — does not change.

Two seams make the agent layer replaceable:

- **Outer — the _ant protocol_** (the MCP tool surface + `submit_turn`).
  A wire protocol, self-documenting via `tools/list`; anything "antish"
  that speaks it replaces ant wholesale. Defined in `specs/12`.
- **Inner — the `Backend`.** This spec. An in-process TypeScript
  interface whose self-documentation is the type
  (`ant/src/backend/types.ts` — `Backend`/`Session`/`Event`/`Caps`/
  `SessionConfig`). It swaps the harness under a fixed ant. A type is
  only self-documenting while every member has a caller: `Caps` and four
  `Session` methods currently have none (§ "`Caps` has no consumer").

## Core principle: ant wraps harnesses, never is one

**ant does not implement agent loops.** Model calls, tool execution,
multi-step reasoning, retries, prompt caching, session state — the whole
agentic part — live in the external harness. ant spawns it, reads its
event stream, and reports one turn over `submit_turn`.

Consequences (each rules out a real temptation):

- **No "API-direct" backend.** Calling Anthropic/OpenAI and running our
  own loop is not a backend — it is becoming a harness.
- **No vendor-neutral unification layer.** LiteLLM is a different product.
- **A harness with no clean wire protocol** (TUI-only, one-shot only,
  SIGINT-interrupt) is not a candidate.
- **A harness missing what ant delegates** (skills, MCP client, file
  tools, permission modes) is out of scope.
- **No cross-harness protocol translation.** Each backend speaks one
  harness natively; a harness without MCP-client support would force a
  translation layer — i.e. the agent-loop trap above.
- **No forking a harness** to fix protocol gaps. Wait for upstream or
  pick another.

## Where the seam sits

The runtime (`ant/src/index.ts`) uses exactly four members of the seam:
`name()`, `spawn(cfg)`, `session.events()`, `session.close()`. It spawns
a session, consumes the event stream, reports the turn on the
terminating event, closes, and — if another batch arrived over IPC —
spawns again with `resume` + `resumeAt`. Everything else — IPC drain,
progress nudges, transcript archiving, secret sanitizing, MCP-server
assembly, system-prompt build — is harness-agnostic and stays above the
seam.

Mid-turn steering does NOT cross the seam. Each backend wires it
natively, inside itself: claude registers a `PostToolUse` hook that
drains `/run/ipc/input` into the live query as `<user-steering>`
(`claude.ts createIpcDrainHook`); codex maps it to `turn/steer`. That is
this spec's own "each backend speaks one harness natively" applied to
steering, and it is why the runtime needs no steering call of its own.

## `Caps` has no consumer, and cannot get a useful one

`Caps` declares eight fields. Nothing in `ant/src` reads any of them —
`capabilities()`'s only call sites are tests. This is not an unfinished
wiring job; the graceful-degradation contract that motivated `Caps`
cannot be built as stated:

- Four of the fields gate `Session` methods — `interrupt`,
  `sendUserMessage`, `setModel`, `setPermissionMode` — that the runtime
  **never calls**. There is no call site to degrade.
- The spec's own worked example, "no live interrupt → close+respawn", is
  the runtime's unconditional behavior: `runQuery` always ends in
  `session.close()`, and continuation is always a fresh `spawn()` with
  `resume`/`resumeAt`. The degraded path is the only path, so a
  capability check would gate nothing.
- Wiring the undegraded path — call `sendUserMessage` when
  `multiTurn` — would add a **third** way to steer a live session beside
  the native per-backend hook and the respawn loop. Root `CLAUDE.md`:
  never add a parallel second path; two paths drift.
- `streaming` and `toolUse` have no branch to gate: a backend that
  cannot stream cannot implement `events()` at all.
- The two branches the runtime genuinely makes — resume-id validation
  and MCP rendering — key on `backend.name()`, because they are about
  one harness's id format and config format, not about a capability.

`setModelLive` is the field that proves it: `claude.ts` reports `false`,
`codex.ts` reports `true`, and nothing has ever behaved differently,
because the model is fixed per spawn via `SessionConfig.model`. Both
values are pinned by tests so the divergence stays a recorded fact.

The open decision — delete `Caps` and the four unused `Session` methods,
or build the live-steering path they describe — is a contract change and
needs sign-off. Tracked in BUGS `F6`. Until then the honest reading is:
**the seam is `name` + `spawn` + `events` + `close`**, and a new backend
satisfies it by implementing those.

## Event normalization — the load-bearing part

The runtime loops `for await (e of session.events())` and on `e.final`
builds `submitTurn(...)` from `status`/`text`/`error`/`models`. There is
**no per-message streaming to the MCP layer** — `submit_turn` is the
single end-of-turn report (`ant/src/index.ts` `deliverTurn`). `raw`
preserves the native payload so stderr logging keeps full fidelity.

The mapping is the decision record:

| `Event`        | claude SDK message                                  | codex `app-server` JSON-RPC                            |
| -------------- | --------------------------------------------------- | ------------------------------------------------------ |
| `init`         | `system` subtype `init` → `session_id`              | `thread/started` → `threadId`                          |
| `assistant`    | `assistant` (track `uuid`)                          | `item/agentMessage/delta` + `item/agentMessage/done`   |
| `tool`         | `assistant` w/ `tool_use` / `user` w/ `tool_result` | `item/mcpToolCall`, `command/exec`, `item/toolResult`  |
| `result` ok    | `result` subtype `success` → `result`, `modelUsage` | `turn/finished` → assembled text + `usage`             |
| `result` error | `result` subtype `error_during_execution`           | `turn/failed` / JSON-RPC error on `turn/start`         |
| `result` retry | `result` subtype `error_max_turns`                  | `turn/finished` w/ truncation reason (runtime retries) |

The runtime's `error_max_turns` retry and `error_during_execution`
session-reset paths key off `status`/`raw`, unchanged by the seam.

## Backend selection

Infra toggle, env var (instance-wide infra in env, not DB): the runtime
reads `ARIZUKO_BACKEND` (default `claude`) from the container env runed
sets at spawn. **Unknown value → fatal at startup, never a silent
fallback to claude.** Mixed backends across folders fall out for free —
selection is per-spawn. A per-folder `backend:` manifest hint is deferred
until the folder layout gains the slot.

MCP wiring: codex consumes servers from `~/.codex/config.toml` or the
setup payload, so `backend/codex.ts` renders `cfg.mcpServers` into that
format on spawn. Because both harnesses are MCP **clients**, the agent's
tool calls reach the socket identically. Auth: claude reads `~/.claude`,
codex reads `~/.codex/auth.json` or `OPENAI_API_KEY`; the backend maps
the ant-level secret from `cfg.env` into the harness's expected location.

## Folder shape compatibility

The agent folder is claude-shaped: skills are markdown Claude Code
auto-loads from `.claude/skills/`. Codex does not. v1 concatenates
relevant `SKILL.md` bodies into `cfg.systemPrompt` — lossy (no on-demand
activation) but mechanically simple. Reframing skills as MCP tools both
harnesses consume is the higher-fidelity follow-up, taken only if the
codex backend sees real use. **The folder shape itself does not change.**

## Why Codex (decision record)

Codex is the second harness because its shape matches claude's: a
documented JSON-RPC protocol, first-class `turn/interrupt` +
`turn/steer`, structured tool-use, native MCP client, prompt caching, and
ChatGPT-plan auth. Caching and plan billing are properties of the
**harness**, not the integration — the `Backend` selects a harness that
has them, it never implements them. No third harness is planned
(`opencode.ai` is the obvious candidate if one is); the seam stays
abstract so one could be added.

**Risk carried:** `app-server` is younger than claude's stream-json, so
field-level breakage between releases is likely. Pin `CODEX_VERSION` in
the codex image variant when it exists.

## Relation to other specs

- [../6/1-adoption-interop.md](../6/1-adoption-interop.md) — the seam is
  language-agnostic; a Go harness would arrive through the scoped
  reimplement path there. The once-planned Go rewrite of ant itself was
  dropped and deleted.
- [P-runed.md](P-runed.md) — the execution envelope the backend runs
  inside; harness-agnostic by design.
- [8-yaml-manifests.md](8-yaml-manifests.md) (state transport) and
  [28-packages.md](28-packages.md) (package/product composition) — fleet
  ops operate on the folder, not the harness, so they stay
  backend-agnostic.
