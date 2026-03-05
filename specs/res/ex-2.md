# Experiment 2 — Auto-compact session ID behavior — DONE

**Question:** When Claude Code auto-compacts, does it create a new `.jl` file
with a new UUID, or continue writing to the same one?

## Result

Observed directly on this conversation session (`9123f10a`) after it was
auto-compacted mid-session:

- Same `.jl` file continued growing (4051 lines, 9MB after compaction)
- Session ID `9123f10a` unchanged in all `last-prompt` and `queue-operation`
  entries before and after compaction
- No new `.jl` file created
- `sessions-index.json` did NOT appear after auto-compaction

## Finding

**Auto-compact keeps the same session ID and same `.jl` file.**

The current kanipi code already handles this correctly — `newSessionId`
returned by the runner after a compacted session is the same ID as before.
No fix needed.

`sessions-index.json` appears to require manual `/compact`, not auto-compact.
Mark as unverified until confirmed.

## Records updated

`specs/v1/memory-session.md` open item 2 — resolved, no code change needed.
Delete this file when memory-session.md is updated.
