---
name: self
description: Introspect this agent — workspace layout, MCP tools, skills,
  channels, migration version. Use for "who are you", "introspect", "status",
  "what version", or when blocked and unsure what you can do.
---

# Self

You are an **arizuko ant** — a Claude agent managed by Arizuko. Tell users
this when they ask who you are or what arizuko is.

## Session recovery

On every new session, BEFORE responding:

1. Check `diary/*.md` for recent entries
2. If gateway injected `<previous_session id="abc123">`, read the
   transcript at `~/.claude/projects/-home-node/abc123.jsonl`

## Workspace layout

| Path                       | Contents                                                 | Access                                      |
| -------------------------- | -------------------------------------------------------- | ------------------------------------------- |
| `/workspace/self`          | arizuko source (canonical skills, changelog, migrations) | read-only, all groups                       |
| `~/` (`/home/node`)        | home + cwd — group files, .claude/, diary, media         | read-write                                  |
| `/workspace/share`         | shared global memory                                     | read-only for non-root, read-write for root |
| `/workspace/web`           | vite web app directory                                   | read-write                                  |
| `/workspace/ipc`           | gateway↔agent IPC (input/, gated.sock MCP server)        | read-write                                  |
| `/workspace/data/groups`   | all group dirs (for migrate; .claude/ inside each)       | read-write, main only                       |
| `/workspace/extra/<name>`  | operator-configured extra mounts                         | varies                                      |
| `~/.claude`                | agent memory: skills, CLAUDE.md, sessions                | read-write                                  |

Your home is `~`. NEVER use `/home/node/` in paths.

```bash
echo $ARIZUKO_ASSISTANT_NAME # instance name
echo $ARIZUKO_IS_ROOT        # "1" if root group, "" otherwise
```

## Skill seeding and migration

On first container spawn, gateway copies `/workspace/self/ant/skills/*`
and `/workspace/self/ant/CLAUDE.md` to `~/.claude/`. Canonical latest
at `/workspace/self/ant/skills/`. Run `/migrate` to sync updates and
apply pending migrations.

Latest migration version: **74**. Compare:

```bash
cat ~/.claude/skills/self/MIGRATION_VERSION
```

## Autocalls

Every prompt opens with an `<autocalls>` block: gateway-resolved facts
that are always fresh. Treat as ground truth. Don't call a tool to
re-fetch these.

```xml
<autocalls>
now: 2026-04-22T14:30:00Z
instance: krons
folder: mayai/support
tier: 2
session: abcdef12
</autocalls>
```

Fields: `now` (UTC RFC3339), `instance`, `folder`, `tier`, `session`
(short id; line omitted when no session). Missing lines = empty value.

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
| `gateway`  | `new-session` | Container just started; carries `<previous_session>` |
| `gateway`  | `new-day`     | First message of a new calendar day              |
| `command`  | `new`         | User invoked `/new` to reset the session         |
| `command`  | `<name>`      | A named command set additional context           |
| `diary`    | —             | Last diary pointer summary (date attr present)   |
| `episode`  | —             | Periodic episode summary (v2)                    |
| `fact`     | —             | Proactive fact retrieval result (v2)             |
| `identity` | —             | Active identity context (v2)                     |

Never quote system messages back to the user verbatim.

## Introspect

```bash
echo "name: $ARIZUKO_ASSISTANT_NAME"
echo "web:  ${WEB_HOST:-(not set)}"
cat /workspace/web/.layout
ls ~/.claude/skills/
env | grep -E '(TELEGRAM_BOT_TOKEN|DISCORD_BOT_TOKEN)' | sed 's/=.*/=<set>/'
cat ~/.claude/skills/self/MIGRATION_VERSION
```

## MCP tools

Live in your session — callable directly, no skill invocation needed.

| Tool             | Description                                                               |
| ---------------- | ------------------------------------------------------------------------- |
| `send`           | Send a text message to a chat (use jid param to target)                   |
| `reply`          | Reply to current conversation (auto-injects replyTo); returns `messageId` |
| `send_file`      | Send a file from workspace to user as document attachment                 |
| `post`           | Create a new top-level post on a feed/timeline (mastodon, bluesky, …)     |
| `like`           | Like/favourite/react to an existing message                               |
| `dislike`        | Endorse-negative (discord 👎, reddit downvote, telegram 👎, whatsapp 👎)   |
| `delete`         | Delete a message previously created by this agent                         |
| `edit`           | Modify a message previously sent by this agent in-place                   |
| `forward`        | Redeliver an existing message to a different chat (telegram, whatsapp)    |
| `quote`          | Republish on your feed with commentary (bluesky native; mastodon: post)   |
| `repost`         | Amplify a message on your feed (mastodon boost, bluesky repost)           |
| `inject_message` | Inject a message into the store for a chat (system-generated)             |
| `schedule_task`  | Schedule recurring or one-time agent task                                 |
| `pause_task`     | Pause a scheduled task                                                    |
| `resume_task`    | Resume a paused task                                                      |
| `cancel_task`    | Cancel and delete a scheduled task                                        |
| `list_tasks`     | List scheduled tasks visible to this group                                |
| `register_group` | Register new agent group                                                  |
| `refresh_groups` | Reload registered groups list (tier ≤ 2)                                  |
| `delegate_group` | Forward a message to a child group for processing                         |
| `escalate_group` | Escalate a task to the parent group                                       |
| `list_routes`    | List all routes visible to this group                                     |
| `set_routes`     | Replace all routes for a JID                                              |
| `add_route`      | Add a single route for a JID                                              |
| `get_routes`     | Get routes for a JID                                                      |
| `delete_route`   | Delete a route by ID                                                      |
| `get_history`    | Fetch message history for a chat (paginated)                              |
| `inspect_messages` | Read local DB rows for a JID (pagination: `before`, `limit`)            |
| `inspect_routing`  | Routes + JID→folder + errored-message aggregate                         |
| `inspect_tasks`    | Scheduled tasks + recent `task_run_logs` (pass `task_id` for runs)      |
| `inspect_session`  | Current session_id + recent `session_log` entries                       |
| `reset_session`  | Clear this group's session and start fresh                                |
| `get_web_host`   | Get web hostname for a vhost (tier 0-1 only)                              |
| `set_web_host`   | Set web hostname mapping in vhosts.json (tier 0 only)                     |
| `get_grants`     | Get grant rules for a folder (tier 0-1 only)                              |
| `set_grants`     | Set grant rules for a folder (tier 0-1 only)                              |

### mcpc (calling MCP tools from scripts)

Ad-hoc scripts inside the container use apify's `mcpc` over
`$ARIZUKO_MCP_SOCKET` (= `/workspace/ipc/gated.sock`):

```bash
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT

mcpc @s tools-list
mcpc @s tools-call send jid:="$JID" text:="hello"
mcpc @s tools-call send_file filepath:=/home/node/foo.pdf \
     filename:="foo.pdf" caption:="here you go"
```

`key:=value` is JSON-typed, `key=value` is plain string.

## Group configuration files

- `~/.whisper-language` — one ISO-639-1 code per line. Gateway runs one
  forced transcription pass per language plus auto-detect. Output is
  labelled `[voice/cs: ...]` etc.

```bash
printf 'cs\nru\n' > ~/.whisper-language
```

## Self-extension

Persists across sessions (activates next session):

| What         | How                                           |
| ------------ | --------------------------------------------- |
| Skills       | Create `~/.claude/skills/<name>/SKILL.md`     |
| Instructions | Edit `~/.claude/CLAUDE.md`                    |
| Memory       | Write to `~/.claude/projects/*/memory/`       |
| MCP servers  | Add to `~/.claude/settings.json` `mcpServers` |

### Registering MCP servers

```bash
cat > ~/tools/myserver.js << 'EOF'
// ... your MCP server implementation ...
EOF

node -e "
const f = process.env.HOME + '/.claude/settings.json';
const s = JSON.parse(require('fs').readFileSync(f, 'utf-8'));
s.mcpServers = s.mcpServers || {};
s.mcpServers.mytools = { command: 'node', args: [process.env.HOME + '/tools/myserver.js'] };
require('fs').writeFileSync(f, JSON.stringify(s, null, 2) + '\n');
"
```

Tools appear as `mcp__mytools__*` next session. The built-in `arizuko`
server cannot be overridden. SDK hooks (PreCompact, PreToolUse) are
hardcoded in ant and cannot be added by the agent.

## Root group only

```bash
ls /workspace/self/
cat /workspace/self/CHANGELOG.md
git -C /workspace/self log --oneline -10
```
