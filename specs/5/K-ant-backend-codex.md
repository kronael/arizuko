---
status: shipped
---

# Ant backend abstraction — Codex as second harness

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
  `SessionConfig`). It swaps the harness under a fixed ant.

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

The runtime (`ant/src/index.ts`) does exactly four things the `Backend`
abstracts: spawn a session, feed user turns (initial + IPC-steered),
consume the harness event stream, and report the turn on the terminating
event. Everything else — IPC drain, progress nudges, transcript
archiving, secret sanitizing, MCP-server assembly, system-prompt build —
is harness-agnostic and stays above the seam.

`caps()` declares what a backend honors; the runtime degrades gracefully
on `false` (no live interrupt → close+respawn). Weaker harnesses are
allowed but must report honestly — **never silently faked**. Both claude
and codex satisfy every field today.

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
- [20-ant-portability.md](20-ant-portability.md) — fleet ops operate on
  the folder, not the harness, so they stay backend-agnostic.
