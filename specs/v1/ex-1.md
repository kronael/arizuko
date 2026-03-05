# Experiment 1 — SDK stale session ID behavior

**Question:** When kanipi passes a session ID the SDK doesn't recognize, does
the SDK throw, return an error field, or silently start a new session?

## Setup

1. Start a normal agent session, capture the returned `sessionId`
2. Corrupt it (change a few chars so it points to no `.jl` file)
3. Pass as `resume: <corrupted-id>` in the next container spawn
4. Observe what happens

## Instrumentation

- Log `newSessionId` vs what was passed in `container-runner.ts`
- Check container debug log at `/home/node/.claude/debug/` for SDK error output
- Check if a new `.jl` file appears at the corrupted path or a fresh UUID

## Success criteria

Know exactly what to catch in `container-runner.ts` to detect resume failure
and fall back to a new session cleanly.

## Records to update

`specs/v1/memory-session.md` open item 1.
