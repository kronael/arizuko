---
name: progress
description: >
  Plan with TodoWrite for a live checklist.
  USE for multi-step work. NOT for one-shot replies.
---

# Live progress checklist

ALWAYS plan real milestones with `TodoWrite`. The harness edits one `⏳`
checklist as tasks move `pending → in_progress → completed`, and deletes it
when the turn ends:

```
☑ build
⏳ deploy
☐ verify
```

NEVER post or edit the checklist yourself; ALWAYS update `TodoWrite`.
NEVER force a task list onto a one-line answer; ALWAYS answer directly.

## Planless turns and unsupported platforms

- `<status>` blocks update progress for turns without a task list. Say what the
  user gets next, never which tool, model or file. ALWAYS close a reported
  status with `reply`.
- A channel that cannot both edit and delete shows no checklist, GitHub
  included.
