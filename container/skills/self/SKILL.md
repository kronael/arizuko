---
name: self
description: Introspect this agent — workspace layout, skills, channels,
  migration version. Use for "who are you", "introspect", "status", "what version".
---

# Self

## Workspace layout

| Path                       | Contents                                                | Access                   |
| -------------------------- | ------------------------------------------------------- | ------------------------ |
| `/workspace/self`          | kanipi source (canonical skills, changelog, migrations) | read-only, all groups    |
| `/workspace/group`         | this group's working directory                          | read-write               |
| `/workspace/global`        | shared global memory                                    | read-only, non-main only |
| `/workspace/web`           | vite web app directory                                  | read-write               |
| `/workspace/ipc`           | gateway↔agent IPC (messages/, tasks/, input/)           | read-write               |
| `/workspace/data/sessions` | all group session dirs (for migrate)                    | read-write, main only    |
| `/workspace/extra/<name>`  | operator-configured extra mounts                        | varies                   |
| `~/.claude`                | agent memory: skills, CLAUDE.md, sessions               | read-write               |

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
echo "name: $NANOCLAW_ASSISTANT_NAME"
echo "web:  ${WEB_HOST:-(not set)}"
cat /workspace/web/.layout 2>/dev/null || echo legacy
ls ~/.claude/skills/
env | grep -E '(TELEGRAM_BOT_TOKEN|DISCORD_BOT_TOKEN)' | sed 's/=.*/=<set>/'
ls /workspace/web/
cat ~/.claude/skills/self/MIGRATION_VERSION 2>/dev/null || echo 0
```

Latest migration version: **4**. If version < 4: migrations pending.

## MCP tools

| Tool             | Description                                                 |
| ---------------- | ----------------------------------------------------------- |
| `send_message`   | Send intermediate status update to user while still running |
| `send_file`      | Send a file from workspace to user as document attachment   |
| `schedule_task`  | Schedule recurring or one-time agent task                   |
| `list_tasks`     | List scheduled tasks                                        |
| `pause_task`     | Pause a scheduled task                                      |
| `resume_task`    | Resume a paused task                                        |
| `cancel_task`    | Cancel a scheduled task                                     |
| `register_group` | Register new WhatsApp group (main only)                     |

### send_file usage

Call `send_file` with the absolute path of any file under `/workspace`.
The gateway resolves the container path to the host and delivers it as a
document attachment.

## Group configuration files

Files you can create/edit in `/workspace/group/` to configure gateway behaviour:

| File                | Effect                                                        |
| ------------------- | ------------------------------------------------------------- |
| `.whisper-language` | One ISO-639-1 code per line (e.g. `cs`, `ru`). Gateway runs   |
|                     | one forced transcription pass per language in addition to the |
|                     | auto-detect pass. Output labelled `[voice/cs: ...]` etc.      |
|                     | Leave absent or empty for auto-detect only.                   |

Example — transcribe in Czech and Russian as well as auto-detect:

```bash
printf 'cs\nru\n' > /workspace/group/.whisper-language
```

## Main group only

```bash
ls /workspace/self/
cat /workspace/self/CHANGELOG.md
git -C /workspace/self log --oneline -10
```
