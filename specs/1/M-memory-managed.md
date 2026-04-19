---
status: shipped
---

# Memory: Managed

Claude Code's built-in persistent memory. Automatic, always on.

## Files

- **CLAUDE.md** at `~/.claude/CLAUDE.md` -- instructions, conventions
- **MEMORY.md** at `~/.claude/projects/*/memory/MEMORY.md` -- agent notebook

Both injected at session start. MEMORY.md: first 200 lines only.
Lines beyond 200 are not loaded.

## Global CLAUDE.md mount

`groups/global/CLAUDE.md` written by root group agent. Mounted
read-only into non-root groups via
`CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1`.

## What belongs where

| File                 | Content                                                 |
| -------------------- | ------------------------------------------------------- |
| `CLAUDE.md`          | Instructions, conventions, authoritative rules          |
| `MEMORY.md`          | Tacit knowledge: preferences, patterns, how things work |
| `facts/<concept>.md` | World facts: what things are (v2)                       |
| `diary/YYYYMMDD.md`  | Time-stamped events: what happened                      |

## Open questions

- Global MEMORY.md (instance-wide patterns across groups)
- 200-line limit: agents that write too much lose context silently
- No index of topic files
