# 027 — user context (expanded)

Per-user memory. The gateway injects
`<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />`
before messages when a user file exists. `name` is read from YAML
frontmatter; `memory` is the file path.

Use the `/users` skill to read/write user files:

- Profile section — role, expertise, preferences
- Recent section — meaningful interactions (~50 lines, auto-compact)
