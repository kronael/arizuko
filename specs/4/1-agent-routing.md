---
status: planned
---

# Agent Routing & Specialized Workers — open (v2)

Pipeline-based message routing to specialized agents within a group.
Group-configurable from inside (agent or operator sets up routing rules).

## Problem

Today every message in a group goes to one agent with one fixed setup.
Some groups need multiple specialized agents — a coder, a researcher,
a scheduler — each with their own container image, CLAUDE.md, skills,
and session. The group operator (or the main agent itself) should be
able to configure which messages go where.

## Design

A group can register **worker agents** alongside the default agent.
Each worker has:

- A name / trigger pattern (keyword, command, or ML-routed)
- Its own container image (or inherits the group default)
- Its own session (separate session ID, separate JSONL transcript)
- Its own CLAUDE.md / skills (in `data/sessions/<folder>/<worker>/`)
- Its own IPC channel

Workers are still sequential per worker slot — one active container
per worker at a time. Multiple workers in a group can run in parallel
(subject to `MAX_CONCURRENT_CONTAINERS`).

## Routing

Three routing modes:

**1. Command-based** — `/code fix this`, `/research topic` routes to
the named worker. The command prefix is stripped before the worker
sees the message. Extends the v1 commands spec.

**2. Keyword/rule-based** — operator defines rules:
`{ pattern: /^@coder/, worker: 'coder' }`. Matched in gateway before
agent spawn.

**3. Agent-delegated** — default agent receives message, decides to
hand off via IPC `type:'delegate'` with `{ worker, prompt }`. Gateway
spawns the target worker with the delegated prompt. Response goes back
to the channel.

## Worker setup

Workers are configured per group. Configuration stored in
`registered_groups.workers` (JSON column or separate table).

A worker definition:

```json
{
  "name": "coder",
  "trigger": "/code",
  "image": "kanipi-agent-coder:latest",
  "claudeMd": "container/workers/coder/CLAUDE.md"
}
```

The main agent can manage worker config via IPC or file tools — the
operator doesn't need to touch the DB directly. This is the
"configurable from inside" requirement.

## Pipeline

Each worker slot is a mini-pipeline:

```
inbound message
  → routing rules (command / keyword / delegation)
  → worker selected
  → worker queue (same GroupQueue, different slot key)
  → runContainerCommand with worker config
  → response back to channel
```

The main agent always sees the full message stream. Workers see only
what is routed to them.

## Prior art

- **brainpro**: `ChannelSessionMap` maps channel target → session,
  one session per channel target. No worker concept.
- **muaddib**: one VM per arc (channel), no routing within a channel.
- **OpenClaw**: 24 hook types, binding-based routing, closest to what
  we want but at the channel level not the worker level.

## Open

- Worker config schema and DB storage
- IPC `type:'delegate'` message definition
- Whether workers share the group's conversation history or have
  their own isolated history
- Worker lifecycle: same idle timeout as main agent, or configurable?
- Security: worker images are operator-specified — same mount allowlist
  checks as `mount-security.ts` apply
- v1 bridge: agent-defined commands (from `specs/1/6-commands.md`) are
  a stepping stone — once command routing exists, worker routing is
  a natural extension
- ML-based routing (classify intent → worker) is speculative, keep open
