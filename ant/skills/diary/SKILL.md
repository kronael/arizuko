---
name: diary
description: >
  Append decisions, bugs fixed, milestones to today's `~/diary/YYYYMMDD.md`.
  USE for "write to diary", "log this", end-of-significant-work, the
  PreCompact nudge. NOT for routine ops, NOT for stable user facts (use
  users) or researched knowledge (use find).
user-invocable: true
arg: <entry text>
---

# Diary

Path: `~/diary/YYYYMMDD.md`. Append to today's entry; create if missing.
`~/diary/YYYY-WNN.md` and `YYYY-MM.md` beside it are the week and month
levels; `/compact-memories diary week|month` writes them, never a hand.

## Format

```markdown
---
summary: |
  Working on arizuko gateway. Main user: onvos.
  - telegram: bot token rotated, env not updated
  - discord: DISCORD_TOKEN missing in staging
---

## 10:32

Fixed WhatsApp reconnect backoff — was always resetting to attempt=1.
503 errors now get 20s minimum delay.
```

`summary:` — project, who you work with, up to 5 critical open items.
Update on every diary write.

## Rules

- `## HH:MM` entries, 250 chars max per entry
- Only decisions, bugs, discoveries, open items. Skip routine ops.
- May compress earlier entries from the same day
- Preferences and recurring patterns → MEMORY.md, report verbatim to user
- Write at the end of significant work (commit, bug fix, key decision). The
  PreCompact hook nudges once, before a compaction; nothing else does
