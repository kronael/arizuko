---
status: draft
---

## <!-- trimmed 2026-03-15: TS lifecycle removed, rich facts only -->

## status: shipped

# Memory: Session

SDK session continuity across container invocations.

## Model

Session = Claude Code SDK conversation (.jl transcript file).
Gateway passes `resume: sessionId` to continue. One active session
per group folder (sequential via group-queue).

## .claude/projects/ Structure

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

## Compaction

At ~95% context, SDK auto-compacts: generates summary, continues
same session ID. Walks `.jl` from end on resume, finds last
`system/compact_boundary`, reconstructs from `logicalParentUuid`.

## Session Switching

| Trigger        | Mechanism                  | Result      |
| -------------- | -------------------------- | ----------- |
| Idle timeout   | Gateway discards stored ID | New session |
| Stale/rejected | SDK resume fails, fallback | New session |
| Agent request  | IPC `reset_session`        | New session |
| User `/new`    | Gateway detects            | New session |

## Context Injection on Reset

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

## `/new` Routing

`/new [message]` clears session for the group the router resolves.
Optional message becomes first prompt. Commands must not bypass routing.
