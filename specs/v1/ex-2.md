# Experiment 2 — Auto-compact session ID behavior

**Question:** When Claude Code auto-compacts (~95% context), does it create a
new `.jl` file with a new UUID, or continue writing to the same one?

## Setup

1. Use REDACTED or a test group with a very long session (or fill context with
   large messages artificially)
2. Watch `data/sessions/main/.claude/projects/<slug>/` for new `.jl` files
3. Compare `newSessionId` returned before and after compaction fires

## Instrumentation

- Log `newSessionId` on every container exit in `container-runner.ts`
- Watch `.claude/projects/` with `inotifywait` during a long session
- After compaction fires, check if `sessions-index.json` appears or updates

## Success criteria

Know whether `newSessionId` must always be stored (changes on compact) or
only on first run (stays the same).

## Records to update

`specs/v1/memory-session.md` open item 2.
