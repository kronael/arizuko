---
name: info
description: Show instance info, workspace state, available skills and tools. Use when asked about status, info, or help.
---

# Info

Display information about the current arizuko instance.

## What to report

1. Instance name (from hostname or config path)
2. Available skills: `ls ~/.claude/skills/`
3. Uptime: `cat /proc/uptime | awk '{print $1}'`
4. Migration version: `cat ~/.claude/skills/self/MIGRATION_VERSION`
   Latest: **51** — if version < 51, warn "migrations pending — run /migrate"
