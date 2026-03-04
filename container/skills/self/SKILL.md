---
name: self
description: Introspect this agent — workspace layout, skills, channels,
  migration version. Use for "who are you", "introspect", "status", "what version".
---

# Self

## Workspace layout

| Path                      | Contents                                                | Access                   |
| ------------------------- | ------------------------------------------------------- | ------------------------ |
| `/workspace/self`         | kanipi source (canonical skills, changelog, migrations) | read-only, all groups    |
| `/workspace/group`        | this group's working directory                          | read-write               |
| `/workspace/global`       | shared global memory                                    | read-only, non-main only |
| `/workspace/web`          | vite web app directory                                  | read-write               |
| `/workspace/ipc`          | gateway↔agent IPC (messages/, tasks/, input/)           | read-write               |
| `/workspace/extra/<name>` | operator-configured extra mounts                        | varies                   |
| `~/.claude`               | agent memory: skills, CLAUDE.md, sessions               | read-write               |

## Skill seeding

On first container spawn, gateway copies:

- `/workspace/self/container/skills/*` → `~/.claude/skills/` (one-time, agent can modify)
- `/workspace/self/container/CLAUDE.md` → `~/.claude/CLAUDE.md` (one-time)

Canonical latest skills always at `/workspace/self/container/skills/`.

## Sync / migrate

`/migrate` skill reads from `/workspace/self/container/skills/`, compares each
skill's SKILL.md to `~/.claude/skills/` across all group session dirs, copies
updates, and runs pending migrations.

## Main group detection

```bash
[ "$NANOCLAW_IS_MAIN" = "1" ] && echo main || echo non-main
```

## Introspect (all groups)

```bash
cat /workspace/web/.layout 2>/dev/null || echo legacy
ls ~/.claude/skills/
env | grep -E '(TELEGRAM_BOT_TOKEN|DISCORD_BOT_TOKEN)' | sed 's/=.*/=<set>/'
ls /workspace/web/
cat ~/.claude/skills/self/MIGRATION_VERSION 2>/dev/null || echo 0
```

Latest migration version: **1**. If version < 1: migrations pending.

## Main group only

```bash
ls /workspace/self/
cat /workspace/self/CHANGELOG.md
git -C /workspace/self log --oneline -10
```
