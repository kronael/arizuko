---
status: shipped
---

# User Context

Per-user memory files. Agent-controlled like facts.

## File

```
~/users/<channel>-<id>.md
```

Examples: `tg-123456.md`, `dc-789.md`.

```markdown
---
name: Alice
first_seen: 2026-03-06
---

Backend developer. Works on validator-bonds. Prefers concise answers.

## Recent

- 2026-03-10: asked about antenna calibration
- 2026-03-08: debugging validator issue
```

- Profile (<20 lines): role, expertise, preferences.
- Recent (~50 lines max): high-level interactions, diary-like scope.
- Auto-compact Recent when >50 lines (drop oldest).

## Gateway signal

Inject user identity, not content:

```xml
<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />
```

- `id`: channel-native sender ID
- `name`: from frontmatter (omitted if absent)
- `memory`: path if file exists

Agent decides when to read the full file.

## Agent behaviour

`/users` skill: read when context helps, update profile on durable
learning, log meaningful interactions (not small talk).

## Scope

Per-group. `users/alice` in group A ≠ group B. Cross-channel identity
is out of scope (see `specs/5/9-identities.md`).

## Files

- `router/` — inject `<user>` tag
- `ant/skills/users/SKILL.md`
- `container/CLAUDE.md` — document users/ pattern
