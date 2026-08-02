---
status: shipped
---

# Memory: Session

SDK session continuity across container invocations.

## Model

Session = Claude Code SDK conversation (`.jl` transcript). Gateway passes
`resume: sessionId` to continue. One active session per group folder
(sequential via group-queue).

## `.claude/projects/` layout

```
~/.claude/projects/<project-slug>/
  <uuid>.jl              conversation transcript
  <uuid>/
    subagents/           subagent JSONL
    tool-results/        tool output blobs
  sessions-index.json    post-compaction
  memory/
    MEMORY.md            auto-memory (200 lines)
    *.md                 topic files
```

`memory/` is project-level, shared across sessions.

## Compaction

At ~95% context, SDK auto-compacts, continues same session ID. Walks
`.jl` from end on resume, finds last `system/compact_boundary`,
reconstructs from `logicalParentUuid`.

## Session switching

| Trigger        | Mechanism                    | Result      |
| -------------- | ---------------------------- | ----------- |
| Idle > 2 days  | Spawn-site check (see below) | New session |
| Stale/rejected | SDK resume fails, fallback   | New session |
| Agent request  | IPC `reset_session`          | New session |
| User `/new`    | Gateway detects              | New session |

### 2-day idle expiry

At spawn, routd compares the per-chat agent cursor (timestamp of the
last processed non-bot message) against the idle threshold. If exceeded,
the stored session id for `(folder, topic)` is deleted before the
container starts — the next run is fresh.

The reason is hallucination, not resource cleanup: multi-day resumes are
the root cause of the failure where an agent blends historical state
into the current turn and answers confidently about a world that has
moved on. A stale session is worse than no session.

Implementation: `sessionIdleExpiry` (2 days) + `sessionIdleExpired` in
`routd/loop.go`, overridable via `SESSION_IDLE_EXPIRY`.

## Context injection on reset

```xml
<system origin="gateway" event="new-session">
  <previous_session id="9123f10a"/>
  <previous_session id="fa649547"/>
</system>
<system origin="diary" date="2026-03-04">
  discussed API design
</system>
```

Last 2 session IDs + last diary entry. MEMORY.md loaded by SDK.

## `/new`

Clears session for the group the router resolves. Optional message
becomes the first prompt. Commands do not bypass routing.
