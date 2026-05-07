---
name: diary
description: Record significant events, decisions, and milestones in today's diary entry.
when_to_use: Use when asked to "write to diary", "log this", or after completing substantial work.
user-invocable: true
arg: <entry text>
---

# Diary

Path: `~/diary/YYYYMMDD.md`. Append to today's entry; create if missing.

## Format

```markdown
---
summary: |
  Working on arizuko gateway. Main user: onvos.
  - twitter: cookies expired, needs refresh
  - discord: DISCORD_BOT_TOKEN missing in staging
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
- Write at end of significant work (commit, bug fix, key decision) — the Stop
  hook nudges; don't wait to be asked
