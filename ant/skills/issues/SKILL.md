---
name: issues
description: Append unresolved bugs and complaints to `~/issues.md` for operator review.
when_to_use: >
  Use when a user reports a problem outside this turn's scope, complains about
  platform behavior, asks for a missing feature, surfaces a deployment flaw,
  or sends a file that didn't arrive (`[Document: …]` with no attachment path).
  Skip if you fix it this turn.
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
