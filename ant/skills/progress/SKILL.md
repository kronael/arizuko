---
name: progress
description: >
  Declare a task summary, then plan with TodoWrite for a live checklist.
  USE for multi-step work. NOT for one-shot replies.
---

# Live progress checklist

ALWAYS declare the overall task in your first `<status>` before `TodoWrite`.
Use one short line. On compatible channels, the harness posts an immutable
`🤖` summary opener. The live checklist and answer follow inside its thread.

ALWAYS plan real milestones with `TodoWrite`. The harness edits one `⏳`
checklist as tasks move `pending → in_progress → completed`:

```
☑ build
⏳ deploy
☐ verify
```

NEVER post or edit the checklist yourself; ALWAYS update `TodoWrite`.
NEVER force a task list onto a one-line answer; ALWAYS answer directly.

## Planless turns and unsupported platforms

- Later `<status>` blocks update progress for turns without a task list.
  ALWAYS close a reported status with `reply`.
- An existing thread keeps the summary, progress and answer in that thread.
  A channel without new-thread support keeps one editable progress message.
  A channel without `edit` receives no progress message, GitHub included.
  When the turn ends, a channel that can delete removes its progress
  message; the other channels keep it as `✓ <text>`.
- If the opener is pending or unreadable, delivery refuses an unplaced answer.
  ALWAYS retry after the reported failure clears; NEVER send around the thread.
