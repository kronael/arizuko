---
status: shipped
---

# Memory: Session

SDK session continuity across container invocations.

## Terminology

- **SDK session**: The .jl transcript file (Claude Code native)
- **Group session**: Per-group persistence, one active per group
- **Container run**: Single invocation of agent container
- **Session record**: DB row logging a completed run

## What it is

A session = Claude Code SDK conversation identified by ID.
SDK stores full transcript as `.jl` file. Gateway passes
`resume: sessionId` to continue where agent left off.

Session ID per-group-folder. One active session per group
(sequential, `group-queue.ts`).

## Lifecycle

```
container start
  -> gateway passes sessionId via stdin
  -> SDK resumes transcript
  -> agent runner returns newSessionId
  -> gateway stores it
  -> next spawn receives it
```

## .claude/projects/ structure

```
~/.claude/projects/<project-slug>/
  <uuid>.jl              -- conversation transcript
  <uuid>/
    subagents/            -- subagent JSONL files
    tool-results/         -- tool output blobs
  sessions-index.json     -- after compaction
  memory/
    MEMORY.md             -- auto-memory (200-line limit)
    *.md                  -- topic files
```

`memory/` is project-level, shared across sessions.

### JSONL entry types

`progress`, `assistant`, `user`, `system`,
`file-history-snapshot`, `queue-operation`, `last-prompt`.
SDK-internal format, not useful to agent directly.

## Compaction

At ~95% context, SDK auto-compacts: generates summary,
continues session. Same session ID and `.jl` file.
Compaction recorded as `system/compact_boundary` entry.
On resume, SDK walks `.jl` from end, finds last boundary,
reconstructs context from `logicalParentUuid` forward.

## Session reset

Idle timeout (`IDLE_TIMEOUT`, default 30min) kills
container. Next message starts new session. CLAUDE.md
and MEMORY.md persist.

## Context injection on reset

Gateway enqueues system messages (see `system-messages.md`):

```xml
<system origin="gateway" event="new-session">
  <previous_session id="9123f10a"/>
  <previous_session id="fa649547"/>
  <previous_session id="3c8a12bb"/>
</system>
<system origin="diary" date="2026-03-04">
  discussed API design
</system>
```

- Last 2 session IDs for continuity tracing
- Last diary entry summary (if exists)
- MEMORY.md loaded automatically by SDK

## Session switching

| Trigger        | Mechanism                  | Result      |
| -------------- | -------------------------- | ----------- |
| Idle timeout   | Gateway discards stored ID | New session |
| Stale/rejected | SDK resume fails, fallback | New session |
| Agent request  | IPC `reset_session`        | New session |
| User `/new`    | Gateway detects            | New session |

## `/new` routing

`/new [message]` — clears the session for whatever group the router resolves
for the incoming message. Optional message becomes the first prompt in the new
session. Commands must not bypass routing.

## Deferred

- Sessions table collapse into `registered_groups.session_id` (cleanup only)
- Agent SKILL.md session layout docs (cosmetic)
