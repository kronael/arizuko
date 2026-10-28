---
name: info
description: >
  Quick status: instance name, available skills and uptime. USE for "/info", "status", short "what's available". NOT
  for deep identity/tools introspection (use self) or web hostname
  setup (use infra).
user-invocable: true
---

# Info

Report:

1. Instance name from the prompt's `<autocalls>` block
2. Available skills: `ls ~/.claude/skills/`
3. Uptime: `awk '{print $1}' /proc/uptime`
