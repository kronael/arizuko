---
status: superseded
---

# Agent Teams — disabled

Claude Code's experimental Agent Teams (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`,
`TeamCreate`/`TeamDelete`/`SendMessage`) not used.

Why not:

- Parallelism already at gateway level (container per group).
- Team-member processes have no tracked lifecycle → orphan risk.
- Sibling stdouts go nowhere — gateway reads one stdio pair.
- `~/.claude/teams/` mount is per-group, wrong for cross-team state.
- Experimental; chat agent has no use for sustained multi-session fork.

Subagents via Agent/Task tool stay — they're the supported pattern.
