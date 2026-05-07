---
name: info
description: >
  Show instance info, workspace state, available skills and tools. Use when
  asked about status, info, or help.
user-invocable: true
---

# Info

Report:

1. Instance name (from hostname or config path)
2. Available skills: `ls ~/.claude/skills/`
3. Uptime: `awk '{print $1}' /proc/uptime`
4. Migration version: `cat ~/.claude/skills/self/MIGRATION_VERSION` vs
   `/workspace/self/ant/skills/self/MIGRATION_VERSION` — if behind, warn
   "migrations pending — run /migrate"
