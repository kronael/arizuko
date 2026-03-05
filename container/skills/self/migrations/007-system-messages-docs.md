# Migration 007 — System messages and session layout docs

## What changed

- `SKILL.md` gains a "System messages" section documenting all origins
  (`gateway/new-session`, `gateway/new-day`, `command/new`, `diary`,
  `episode`, `fact`, `identity`) and the rule to never quote system
  messages back to the user verbatim.
- `container/agent-runner/CLAUDE.md` created with session layout section:
  where `.jl` files live, what session IDs are, how to read past sessions.

## Migration steps

No filesystem changes required — documentation only. Run `/migrate` to
pull the updated SKILL.md into `~/.claude/skills/self/SKILL.md`.
