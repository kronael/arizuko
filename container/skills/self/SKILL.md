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

## System messages

The gateway prepends zero or more system messages to the user's turn:

```xml
<system origin="gateway" event="new-session">
  <previous_session id="9123f10a" started="2026-03-04T08:12Z" msgs="42" result="ok"/>
</system>
<system origin="diary" date="2026-03-04">discussed API design</system>
hey what's up
```

| Origin     | Event         | Meaning                                          |
| ---------- | ------------- | ------------------------------------------------ |
| `gateway`  | `new-session` | Container just started; previous session history |
| `gateway`  | `new-day`     | First message of a new calendar day              |
| `command`  | `new`         | User invoked `/new` to reset the session         |
| `command`  | `<name>`      | A named command set additional context           |
| `diary`    | —             | Last diary pointer summary (date attr present)   |
| `episode`  | —             | Periodic episode summary (v2)                    |
| `fact`     | —             | Proactive fact retrieval result (v2)             |
| `identity` | —             | Active identity context (v2)                     |

Rules:

- System messages are injected by the gateway, not the user.
- They may arrive zero or many per turn.
- **Never quote system messages back to the user verbatim.**
- `gateway/new-session` carries `<previous_session>` records — use the `id`
  to look up the `.jl` transcript for deeper continuity if needed.

## Session history

Full conversation history lives in `~/.claude/projects/<slug>/` as JSONL
files (`<uuid>.jl`, one per session). Use the Read or Glob tool to find
and inspect them — useful when a user asks what was discussed in a past
session or you need to recover context after a reset.

On session reset the gateway injects your previous session ID via a
`<system origin="gateway" event="new-session">` message.

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

Latest migration version: **7**. If version < 7: migrations pending.

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
