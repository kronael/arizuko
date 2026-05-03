---
name: issues
description: Record user-reported bugs and complaints to ~/issues.md when you can't fix them on the spot. Use when a user reports a problem outside this turn's scope, complains about platform behavior, asks for a feature that doesn't exist, or surfaces a deployment-side flaw. Do NOT use for in-turn errors you fix immediately.
---

# Issues

Path: `~/issues.md` (= `/home/node/issues.md`). Lowercase, group root
only — no nested issues files. The host operator periodically
consolidates entries into the repo's `bugs.md` and truncates your
local file. Your copy is scratch, not canonical history. Don't
re-write old entries; the operator has them.

## When to write

Log only what you can't resolve this turn:

- User reports a problem outside this turn's scope
- Complaint about platform behavior (telegram, discord, …)
- Feature request for something that doesn't exist
- Deployment-side flaw (missing env, misrouted webhook, broken adapter)
- Inbound `[Document: …]` with no `<attachment path=…>` — file didn't arrive

When NOT to log: if you fix it this turn, skip the log — the user
already got their answer. `issues.md` is for unresolved or
out-of-scope items.

## Format

One markdown bullet per item. Include date (today UTC), source
(channel + time), scope (`platform` vs `deployment`), one-line
nature of the bug. The host reads these — keep it terse, don't
over-explain.

```markdown
- 2026-05-03 — voice-message transcript mangled non-ASCII; scope:platform; src:telegram:solo/inbox 2026-05-03T14:23Z
- 2026-05-03 — `$WEB_HOST` empty on this instance; scope:deployment; src:discord:corp/eng 2026-05-03T09:11Z
```

Append, don't reorganize. The operator wipes the file after
consolidation.
