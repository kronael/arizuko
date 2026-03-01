---
name: info
description: Show instance info, workspace state, available skills and tools. Use when asked about status, info, or help.
---

# Info

Display information about the current kanipi instance.

## What to report

1. Instance name (from hostname or config path)
2. Gateway status: `curl -s http://localhost:18789/health`
3. Available skills: `ls ~/.claude/skills/`
4. Uptime: `cat /proc/uptime | awk '{print $1}'`
