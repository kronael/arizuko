---
status: reference
---

# Reference Systems Analysis

Findings from studying brainpro, takopi, and eliza-atlas source code.

Sources:

- `/home/onvos/app/refs/brainpro` — Rust CLI + gateway + agent daemon
- `/home/onvos/app/refs/takopi` — Python Telegram bridge
- `/home/onvos/app/eliza-atlas` — ElizaOS fork with YAML facts

---

## Adopted

**Circuit breaker** (from brainpro `circuit_breaker.rs`): Closed -> Open
(5 failures) -> HalfOpen (30s) -> Closed. Per-backend registry.
Adopted in `queue/queue.go`.

**Doom loop detection** (from brainpro `agent_impl.rs`): ring buffer of
last N tool-call hashes; 3 identical -> abort turn. Adopted in
container runner.

**Group routing** (from takopi ThreadScheduler + brainpro
ChannelSessionMap): per-thread FIFO with session lock. Adopted as
GroupQueue in `queue/`.

**Modular persona assembly** (from brainpro `config/persona/`):
per-group identity files assembled by mode flags. Adopted as
SOUL.md + SYSTEM.md per group.

**Resume token pattern** (from takopi): embed session ID in reply,
extract on next message, `--resume <id>` to agent CLI. Adopted as
session continuity in `store/`.

**XML context bundles** (from eliza-atlas): wrap memory/facts in XML
tags for system prompt. Adopted as the standard prompt format in
`router/`.

## Evaluated, not adopted

**Two-phase fact verification** (from eliza-atlas): Opus researches,
Sonnet refutes. Not adopted — requires second Claude invocation per
factset; cost model unresolved.

**YAML facts repo** (from eliza-atlas): per-slug `.md` files with
embeddings and cosine search. Not adopted — arizuko uses skills and
CLAUDE.md for long-term memory instead.

**Per-engine project routing** (from takopi): `/<engine-id>`,
`@branch` directives. Not adopted — arizuko routes by group folder,
not by engine/worktree.

## Rejected

**LangChain-style chaining** (from eliza-atlas service model):
topological dependency resolution for plugin lifecycle. Rejected —
too much framework for arizuko's unix-process model.

**Full brainpro permission system**: pattern-based allow/ask/deny with
tool globs. Rejected in favor of grants rule engine (`grants/`).

---

## Cross-cutting comparison

| Pattern        | brainpro               | takopi                | eliza-atlas           | arizuko              |
| -------------- | ---------------------- | --------------------- | --------------------- | -------------------- |
| Routing        | Session-per-target     | Thread-per-message    | Room-per-workspace    | Group-per-JID        |
| Resume         | Session UUID           | Resume token          | Session+message ID    | Session folder       |
| Memory         | BOOTSTRAP+MEMORY+daily | None (stateless)      | YAML facts+embeddings | Skills+CLAUDE.md     |
| Tool isolation | Subagent toml+perms    | CLI flags (delegated) | Restricted tool list  | Grants rule engine   |
| MCP            | config.toml per-server | N/A                   | N/A                   | ipc unix socket      |
| Resilience     | Circuit breaker+doom   | Thread-safe queuing   | Session lifecycle     | Circuit breaker+doom |

---

## Open

- Doom loop threshold: 3 identical calls (brainpro default). Adjust
  for agents that legitimately repeat reads.
- Circuit breaker scope: per-group or per-image? Per-group is safer.
- Modular persona: boundary between gateway-managed persona vs.
  agent-managed rules still fuzzy.
