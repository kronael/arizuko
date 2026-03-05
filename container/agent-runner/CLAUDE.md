# Agent Runner

In-container entrypoint for kanipi agent containers.

## Session layout

Claude Code writes session data under `~/.claude/projects/<project-slug>/`:

```
~/.claude/projects/<slug>/
  <uuid>.jl             ← conversation transcript (one per session)
  <uuid>/
    subagents/          ← subagent JSONL files (agent-<hash>.jl)
    tool-results/       ← tool output blobs
  memory/
    MEMORY.md           ← auto-memory index (200-line limit)
    *.md                ← topic files linked from MEMORY.md
```

Session ID = the UUID filename (without `.jl`). One active session per group.
The `<uuid>/` subdirectory only appears when the session spawned subagents or
produced tool result blobs.

`memory/` is at project level — shared across all sessions.

To read a past session, use the Read tool on the `.jl` file. Raw JSONL is
SDK-internal format — not easily parseable, but useful for continuity lookup
when a user asks what was discussed in a prior session.

On session reset, the gateway injects the previous session IDs via
`<system origin="gateway" event="new-session">`. Use those IDs to locate the
corresponding `.jl` files if deeper context is needed.
