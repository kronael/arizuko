---
name: self
description: Introspect this agent — workspace layout, skills, channels,
  migration version. Use for "who are you", "introspect", "status", "what version".
---

# Self

## Workspace layout

| Path                       | Contents                                                | Access                                      |
| -------------------------- | ------------------------------------------------------- | ------------------------------------------- |
| `/workspace/self`          | kanipi source (canonical skills, changelog, migrations) | read-only, all groups                       |
| `/workspace/group`         | this group's working directory                          | read-write                                  |
| `/workspace/share`         | shared global memory                                    | read-only for non-root, read-write for root |
| `/workspace/web`           | vite web app directory                                  | read-write                                  |
| `/workspace/ipc`           | gateway↔agent IPC (messages/, tasks/, input/)           | read-write                                  |
| `/workspace/data/sessions` | all group session dirs (for migrate)                    | read-write, main only                       |
| `/workspace/extra/<name>`  | operator-configured extra mounts                        | varies                                      |
| `~/.claude`                | agent memory: skills, CLAUDE.md, sessions               | read-write                                  |

## Skill seeding

On first container spawn, gateway copies:

- `/workspace/self/container/skills/*` → `~/.claude/skills/` (one-time, agent can modify)
- `/workspace/self/container/CLAUDE.md` → `~/.claude/CLAUDE.md` (one-time)

Canonical latest skills always at `/workspace/self/container/skills/`.

## Sync / migrate

`/migrate` skill reads from `/workspace/self/container/skills/`, compares each
skill's SKILL.md to `~/.claude/skills/` across all group session dirs, copies
updates, and runs pending migrations.

## Root group detection

```bash
[ "$NANOCLAW_IS_ROOT" = "1" ] && echo root || echo non-root
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

Latest migration version: **11**. If version < 11: migrations pending.

## MCP tools

| Tool                | Description                                                 |
| ------------------- | ----------------------------------------------------------- |
| `send_message`      | Send intermediate status update to user while still running |
| `send_file`         | Send a file from workspace to user as document attachment   |
| `schedule_task`     | Schedule recurring or one-time agent task                   |
| `pause_task`        | Pause a scheduled task                                      |
| `resume_task`       | Resume a paused task                                        |
| `cancel_task`       | Cancel and delete a scheduled task                          |
| `register_group`    | Register new WhatsApp group (root only)                     |
| `refresh_groups`    | Sync group metadata from channel (root only)                |
| `delegate_group`    | Forward a message to a child group for processing           |
| `set_routing_rules` | Set automatic routing rules for child groups                |
| `reset_session`     | Clear this group's session and start fresh                  |

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

## Self-extension

You can extend your own capabilities across sessions:

| What         | How                                           | When active  |
| ------------ | --------------------------------------------- | ------------ |
| Skills       | Create `~/.claude/skills/<name>/SKILL.md`     | Next session |
| Instructions | Edit `~/.claude/CLAUDE.md`                    | Next session |
| Memory       | Write to `~/.claude/projects/*/memory/`       | Next session |
| MCP servers  | Add to `~/.claude/settings.json` `mcpServers` | Next session |

### Registering MCP servers

Write a server script to your workspace and register it in settings:

```bash
# write your MCP server to workspace
cat > /workspace/group/tools/myserver.js << 'EOF'
// ... your MCP server implementation ...
EOF

# register in settings (preserves existing entries)
node -e "
const f = '/home/node/.claude/settings.json';
const s = JSON.parse(require('fs').readFileSync(f, 'utf-8'));
s.mcpServers = s.mcpServers || {};
s.mcpServers.mytools = { command: 'node', args: ['/workspace/group/tools/myserver.js'] };
require('fs').writeFileSync(f, JSON.stringify(s, null, 2) + '\n');
"
```

On next session spawn, the new MCP tools will be available as
`mcp__mytools__*`. The built-in `nanoclaw` server cannot be overridden.

### Known limitation

SDK hooks (PreCompact, PreToolUse) cannot be added by the agent.
These are hardcoded in the agent-runner.

## Root group only

```bash
ls /workspace/self/
cat /workspace/self/CHANGELOG.md
git -C /workspace/self log --oneline -10
```
