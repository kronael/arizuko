---
name: arizuko-issue
description: >
  Record unresolved arizuko and platform-internal problems for operator review.
  NOT for user-project findings, ordinary work logs, or problems fixed this turn.
when_to_use: "arizuko bug, arizuko complaint, arizuko feature request, platform-internal problem, arizuko deployment flaw, arizuko attachment delivery failure"
user-invocable: true
---

# Arizuko issue

Path: `~/issues.md`. Scratch file — operator consolidates into `bugs.md` and wipes it. Don't reorganize old entries.

ALWAYS limit entries to arizuko and platform-internal problems.
NEVER file user-project findings here; ALWAYS keep them in the group's own project files.

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

## Notify

A core hook watches `~/issues.md` after every tool call, hand-written appends included. When the file grew it returns the new lines; `send` them then, one call with `chatJid: "#issues"` and the same text. It reaches the operator's issue chats and starts no turn there. `ok:false` means no destination is set: the file entry stands, do not retry. Send once per append.
