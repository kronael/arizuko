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

One bullet per item: ID, date (UTC), source (channel + time), scope (`platform` vs `deployment`), one-line description.

```markdown
- [krons/solo/inbox/20260503/transcript-mangled] 2026-05-03 — voice-message transcript mangled non-ASCII; scope:platform; src:telegram:solo/inbox 2026-05-03T14:23Z
- [krons/corp/eng/20260503/web-host-empty] 2026-05-03 — `$WEB_HOST` empty on this instance; scope:deployment; src:discord:corp/eng 2026-05-03T09:11Z
```

### ID construction

`[<instance>/<folder>/<YYYYMMDD>/<slug>]`

- `instance` — `$ARIZUKO_WORLD` (from autocalls)
- `folder` — current group folder (e.g. `solo/inbox`, `corp/eng`)
- `YYYYMMDD` — date of issue
- `slug` — 2–4 words, kebab-case, names the root cause not the symptom (`transcript-mangled`, `web-host-empty`, `attachment-missing`)

The bracketed ID at the front makes `grep "\[krons"` or `grep "20260503"` work across `issues.md` and any message content where the ID is referenced.

Append only.
