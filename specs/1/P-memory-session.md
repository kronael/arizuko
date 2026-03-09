# Memory: Session -- open

SDK session continuity across container invocations.

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
  <previous_session id="9123f10a" started="..."
    ended="..." msgs="42" result="ok"/>
</system>
<system origin="diary" date="2026-03-04">
  discussed API design
</system>
```

- Last session ID for continuity tracing
- Last diary entry summary (if exists)
- MEMORY.md loaded automatically by SDK

## Session switching

| Trigger        | Mechanism                  | Result      |
| -------------- | -------------------------- | ----------- |
| Idle timeout   | Gateway discards stored ID | New session |
| Stale/rejected | SDK resume fails, fallback | New session |
| Agent request  | IPC `reset_session`        | New session |
| User `/new`    | Gateway detects            | New session |

## Episode notes (REDACTED, Mar 2026)

Observed on live 4-day session (REDACTED, session 58f49dbe):

- Single session ran 4+ days without reset (IPC kept
  container alive between user turns)
- Full message replay on every restart (no checkpoint)
- No fallback on crash/timeout — all context lost
- SDK resume failure handling is urgent

## Open

1. Gateway error handling on `status: error` — clear
   stored session ID, notify user to retry
2. Session ID history injection — store last 3 in DB
3. Agent skills — document session layout in SKILL.md
4. `sessions` table collapse into
   `registered_groups.session_id`
5. `reset_session` IPC — specced in `commands.md`
6. `/new` detection — specced in `commands.md`
