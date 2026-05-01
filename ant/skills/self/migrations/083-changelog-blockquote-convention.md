# 083 — CHANGELOG entries open with a `>` blockquote summary

Each release entry in `CHANGELOG.md` now opens with a 1-3 line
plain-language summary in a `>` blockquote, BEFORE the developer
detail. Example:

```markdown
## [v0.32.2] — 2026-04-30

> Cleaner URLs for the new HTTP API your agent now speaks. Plus a
> behind-the-scenes fix so docker rebuilds no longer briefly break
> agent spawning.

URL polish on the slink round-handle protocol shipped in v0.32.0.
The `/turn/` infix and `?steer=` query parameter both disappear...
```

`/migrate` step (e) extracts ONLY the version header + the blockquote
when broadcasting to user chats. The dev sections never reach end
users.

When you write/maintain CHANGELOG entries:

- ALWAYS add a blockquote at the top of each new release entry, 1-3
  lines, plain language, no jargon.
- Lead with what the user notices, not what changed in the code.
- Skip implementation details, file paths, function names — that's
  what the dev section is for.
- The blockquote is what reaches Telegram / WhatsApp / Discord chats.

Backfilled v0.32.0, v0.32.1, v0.32.2 with this convention.
