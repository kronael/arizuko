# 006 — session history access

Documented that the agent can read its own session JSONL files from
`~/.claude/projects/` using Read/Glob tools.

## What changed

- `container/skills/self/SKILL.md` has a new "Session history" section
  explaining where session files live and when the gateway injects the
  previous session ID on reset.

## Agent action required

Updated SKILL.md will be seeded on next `/migrate` run.
