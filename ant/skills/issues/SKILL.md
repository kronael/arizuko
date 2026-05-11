---
name: issues
description: >
  Append unresolved bugs/complaints to `~/issues.md` for operator review.
  USE for "this is broken", "file an issue", "feature request",
  platform complaints, deployment flaws, missing-attachment reports,
  out-of-scope problems. NOT when you fix it this turn, NOT for ordinary
  work logs (use diary).
user-invocable: true
---

# Issues

Path: `~/issues.md`. Scratch file — operator consolidates into `bugs.md` and wipes it. Don't reorganize old entries.

## Format

One bullet per item: date (UTC), source (channel + time), scope (`platform` vs `deployment`), one-line description.

```markdown
- 2026-05-03 — voice-message transcript mangled non-ASCII; scope:platform; src:telegram:solo/inbox 2026-05-03T14:23Z
- 2026-05-03 — `$WEB_HOST` empty on this instance; scope:deployment; src:discord:corp/eng 2026-05-03T09:11Z
```

Append only.
