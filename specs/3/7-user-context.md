---
status: shipped
---

# User Context

Per-user memory files. Agent-controlled like facts.

## Design

One file per user, managed by the agent:

```
~/users/<channel>-<id>.md
```

Examples: `tg-123456.md`, `wa-5551234.md`, `dc-789.md`

### File format

```markdown
---
name: Alice
first_seen: 2026-03-06
---

Backend developer. Works on validator-bonds.
Prefers concise answers with code refs.

## Recent

- 2026-03-10: asked about antenna calibration
- 2026-03-08: debugging validator issue
```

Frontmatter: identity fields. Body: stable knowledge + interaction log.

- Profile section: role, expertise, preferences (<20 lines)
- Recent section: high-level interactions, diary-like scope (~50 lines max)
- Auto-compact Recent when >50 lines: drop oldest entries

### Gateway signal

Gateway injects user identity, not full content:

```xml
<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />
```

- `id`: channel-native sender ID
- `name`: from file frontmatter (omitted if no file or no name)
- `memory`: path if file exists, omitted if no file yet

Agent decides when to read the full file. Gateway extracts just the name from YAML frontmatter.

### Agent reads/writes

Agent uses `/users` skill to:

- Read user file when context would help
- Update profile when learning something durable (role, expertise, style)
- Log meaningful interactions in Recent section (not every message)

### What to log

Similar to diary scope — only notable interactions:

- Questions about specific topics
- Completed tasks or deliverables
- Preferences expressed
- Context that might be useful later

NOT routine greetings or small talk.

## Scope

Per-group. `users/alice` in group A ≠ `users/alice` in group B.

Cross-channel identity (same person on telegram + whatsapp) is out of scope — see 5/9-identities.

## Changes

```
src/router.ts
  - inject <user> tag with id, name, memory path

container/skills/users/SKILL.md
  - /users skill for read/write

container/CLAUDE.md
  - document users/ pattern
```

~5 lines gateway + skill.
