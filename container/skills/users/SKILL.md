---
name: users
description: Read or update user context files. Use when you need to
  remember something about a user or recall what you know about them.
user_invocable: true
arg: <user-id or action>
---

# User Context

`~/users/` stores per-user memory files. One file per sender.

## File format

```markdown
---
name: Alice
first_seen: 2026-03-06
summary: >
  Backend developer working on validator-bonds. Prefers concise
  answers with code refs.
---

Backend developer. Works on validator-bonds.
Prefers concise answers with code refs.

## Recent

- 2026-03-10: asked about antenna calibration
- 2026-03-08: debugging validator issue
```

- Frontmatter: identity + summary fields (name, first_seen, summary)
- Profile: stable knowledge — role, expertise, preferences (<20 lines)
- Recent: interaction log — meaningful interactions only (~50 lines max)

## Reading

When the gateway injects `<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />`,
that user has a context file. The `name` is extracted from frontmatter. Read the
full file if you need more context (role, preferences, history).

No `memory` attribute means no file exists yet.

## Writing

**`summary:` frontmatter** — 1-2 sentence description of the user for recall
indexing. Update when profile knowledge changes (role, expertise, preferences).

**Profile section** — update when you learn something durable:

- Name or role
- Expertise areas
- Communication preferences

**Recent section** — log meaningful interactions (diary-like scope):

- Questions about specific topics
- Completed tasks or deliverables
- Preferences expressed

NOT every message — only notable interactions worth remembering.

When Recent exceeds ~50 lines, drop oldest entries.

## File naming

Files are named by channel and platform ID:

- `tg-123456.md` — Telegram
- `wa-5551234.md` — WhatsApp
- `dc-789.md` — Discord
- `em-user@example.com.md` — Email

Use the `id` from `<user>` tag for the filename.

## Usage

```
/users tg-123456        # read user file
/users update tg-123456 # update user file with new knowledge
```

When invoked without args, list all user files.
