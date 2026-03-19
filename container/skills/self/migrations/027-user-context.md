# 027 — User context

## Goal

Enable per-user memory. The gateway now injects `<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />` before messages when a user file exists.

## What changed

**Gateway**: extracts `name` from YAML frontmatter of `~/users/<id>.md` and injects:

- `id` — always present (platform + sender ID)
- `name` — from file frontmatter (if file exists and has `name:` field)
- `memory` — path to user file (if file exists)

**CLAUDE.md** section:

```
# User Context

When a message arrives, the gateway injects `<user id="tg-123456" name="Alice" memory="~/users/tg-123456.md" />`.
If `memory` is present, you have a context file for this user. Read it when context would help.

Update user files via `/users`:
- Profile section: role, expertise, preferences (stable knowledge)
- Recent section: meaningful interactions (~50 lines, auto-compact)
```

**Skill**: `/users` skill at `~/.claude/skills/users/SKILL.md` for reading/writing user files.

## Check

```bash
grep -q "User Context" ~/.claude/CLAUDE.md && echo "done" || echo "run"
```

## Steps

No action needed. Documentation is already in `CLAUDE.md`. The feature is injected by the gateway.

## After

```bash
echo 27 > ~/.claude/skills/self/MIGRATION_VERSION
```
