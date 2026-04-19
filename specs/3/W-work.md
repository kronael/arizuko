---
status: unshipped
---

# Work — current task state

Skill-managed working state file. Inspired by brainpro's WORKING.md,
implemented as an agent skill.

## Path

```
/workspace/group/work.md
```

Single file per group. Agent-written, agent-read.

## Purpose

Captures what the agent is doing. Ephemeral — active task, blockers,
next steps. Overwritten, not appended.

## Skill: `/work`

Overwrites `/workspace/group/work.md`:

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

## Gateway injection

On session start, if `groups/<folder>/work.md` exists, inject as system
message (full content, no truncation). Injected after diary, before
conversation history.

## Triggers

1. `/work` skill — agent-initiated anytime
2. Pre-session nudge — if work.md >24h old, gateway annotates:
   "work.md is stale — update or clear with /work"
3. Session end — no automatic write

## Layer comparison

| Layer     | Timeframe  | Content           |
| --------- | ---------- | ----------------- |
| work.md   | Right now  | Active task       |
| diary     | Today      | What happened     |
| episodes  | Week/month | Aggregated        |
| facts     | Permanent  | Concepts/entities |
| MEMORY.md | Persistent | Tacit knowledge   |
