---
name: progress
description: >
  Live progress for a multi-step turn. The harness does this automatically:
  plan with a task list (TodoWrite) and it renders ONE ⏳ checklist that edits
  in place as tasks tick over. USE only when a turn genuinely has several steps
  or will take a while. NOT for one-shot replies.
---

# Live progress checklist

For a genuinely multi-step turn, **plan with a task list (the `TodoWrite`
tool)**. The harness renders your list into ONE `⏳` checklist and edits
that same message as each task moves `pending → in_progress → completed`
(spec 5/24) — nothing to post or `edit` by hand:

```
☑ build
⏳ deploy
☐ verify
```

You don't call `send`/`edit` for this — writing the task list IS the
update. Keep the list to real milestones (three beats ten micro-steps);
never force a task list onto a one-line answer.

## Planless turns and unsupported platforms

- A quick turn with no task list still reports via `<status>…</status>`
  blocks (ant/CLAUDE.md § Status updates) — the floor.
- On platforms without message editing (email, WhatsApp, Reddit) the
  harness falls back to separate `⏳` notices automatically. Nothing to do.
