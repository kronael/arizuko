---
status: shipped
---

# Work — current task state

Skill-managed working state file. Inspired by brainpro's WORKING.md,
implemented as an agent skill.

## Path

```
/home/node/work.md
```

Single file per group. Agent-written, agent-read.

## Purpose

Captures what the agent is doing. Ephemeral — active task, blockers,
next steps. Overwritten, not appended.

## Skill: `/work`

Overwrites `/home/node/work.md`:

```markdown
## Current task

Implementing IPC file sending — path translation bug.

## Blockers

- hostPath() uses APP_DIR, should use GATEWAY_ROOT

## Next

- Fix hostPath, rebuild, deploy to krons
- Test with manual IPC message
```

Plain markdown, no frontmatter, max ~20 lines.

## Injection

`container/runner.go` reads `groups/<folder>/work.md` on prompt assembly
and appends it as a `<knowledge layer="work">` annotation — full
content, no truncation, after episodes and diary. Empty or missing file
= no annotation.

## Triggers

1. `/work` skill — agent-initiated, any time.
2. `get_work` / `set_work` MCP tools (`ipc/ipc.go`) — read at turn
   start to recover what was in flight, overwrite at turn end.
   `set_work` replaces the file, so read first if merging.
3. No automatic write at session end.

The staleness nudge this spec proposed ("work.md is stale — update or
clear") was never built. The file is injected whenever it is non-empty
regardless of age, so a stale `work.md` reads as current — clearing it
is the agent's discipline, not the platform's.

## Layer comparison

| Layer     | Timeframe  | Content           |
| --------- | ---------- | ----------------- |
| work.md   | Right now  | Active task       |
| diary     | Today      | What happened     |
| episodes  | Week/month | Aggregated        |
| facts     | Permanent  | Concepts/entities |
| MEMORY.md | Persistent | Tacit knowledge   |
