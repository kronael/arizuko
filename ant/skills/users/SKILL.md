---
name: users
description: Read or update user context files in users/.
when_to_use: Use when you need to remember something about a user or recall what you know about them.
user-invocable: true
arg: <user-id or action>
---

# User Context

`~/users/` stores per-user memory files. One file per sender, named by
channel + platform ID: `tg-123456.md`, `wa-5551234.md`, `dc-789.md`,
`em-user@example.com.md`. Use the `id` from the gateway's `<user>` tag.

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

- Frontmatter: `name`, `first_seen`, `summary` (1-2 sentence digest)
- Profile body: stable knowledge (<20 lines)
- Recent: meaningful interactions only (~50 lines; drop oldest when over)

## Usage

The gateway injects `<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />`
when a context file exists. Read it for role, preferences, history.
No `memory` attribute → no file yet.

```
/users tg-123456        # read user file
/users update tg-123456 # update with new knowledge
```

No args → list all user files. Update when you learn durable facts
(role, expertise, preferences) or log a notable interaction —
not every message.
