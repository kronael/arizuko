# arizuko master architecture — microservice design

## Design principles

1. **Contract over code**: SQLite schema + MCP protocol
   are the interfaces. Language per service is free.
2. **Each daemon is independently shippable**: test, deploy,
   replace one service without touching others.
3. **Agents can build agents**: each service is small enough
   (~200-500 LOC) that an AI agent can write one from scratch.
4. **MCP as universal IPC**: gateway exposes tools via MCP
   over unix sockets. Any MCP client can connect.

## Component map

```
┌──────────────────────────────────────────────────────────┐
│                     SQLite (WAL)                          │
│  messages │ outbox │ tasks │ groups │ sessions │ auth     │
└─────┬────────┬────────┬────────┬────────┬────────┬───────┘
      │        │        │        │        │        │
  ┌───┴──┐ ┌───┴──┐ ┌───┴──┐ ┌──┴───┐ ┌──┴──┐ ┌──┴──┐
  │ tg   │ │ dc   │ │ wa   │ │ gate │ │ sch │ │ web │
  │ chan  │ │ chan  │ │ chan │ │ way  │ │ uler│ │     │
  └──────┘ └──────┘ └──────┘ └──┬───┘ └─────┘ └─────┘
                                 │
                          MCP unix socket
                                 │
                          ┌──────┴──────┐
                          │ agent       │
                          │ container   │
                          │ (claude CLI)│
                          └─────────────┘
```

Seven sections below: 5 components (channel, gateway, web,
agent runner, MCP IPC), scheduler (open: separate or
gateway-internal), and store (shared schema contract).
Each can be discussed and shipped independently.

## Insight from the Go rewrite

The Go port forced clean layering. The key insight wasn't
about Go — it was that the layers (channel, gateway, agent,
store, web) are genuinely independent. They share only
SQLite and types. If they don't share memory, they don't
need to share a process.

## Postfix model

Postfix runs ~12 small daemons, each with one job.
`master(8)` spawns and supervises them. They communicate
through well-defined interfaces: unix sockets, files in
queue directories, and shared lookup tables.

arizuko has the same shape:

```
channels     →  write messages to SQLite
gateway      →  reads messages, decides what to run
agent runner →  spawns docker containers
scheduler    →  checks cron, enqueues tasks
web          →  serves HTTP, auth, slink
```

Today these are goroutines/callbacks in one process.
They could be separate processes communicating through
SQLite (the outbox pattern from the Go spec).

## Proposed process map

```
arizuko-master          supervisor (or systemd)
  ├── arizuko-telegram   channel adapter → writes messages to DB
  ├── arizuko-discord    channel adapter → writes messages to DB
  ├── arizuko-whatsapp   channel adapter → writes messages to DB
  ├── arizuko-email      channel adapter → writes messages to DB
  ├── arizuko-gateway    reads DB, routes, manages agent lifecycle
  ├── arizuko-scheduler  reads tasks table, enqueues due tasks
  └── arizuko-web        HTTP server (auth, slink, vite proxy)
```

### Communication: SQLite as message bus

```
channel → INSERT INTO messages → gateway polls/watches
gateway → INSERT INTO outbox   → channel polls/watches
scheduler → INSERT INTO messages (synthetic) → gateway picks up
web → INSERT INTO messages (slink) → gateway picks up
```

All processes open the same SQLite database in WAL mode.
WAL allows concurrent readers + one writer. Writes are
serialized by SQLite's internal locking.

### Process lifecycle

Each process is a small binary (or a mode of one binary):

```
arizuko channel telegram    # runs telegram adapter
arizuko channel discord     # runs discord adapter
arizuko gateway             # runs core loop
arizuko scheduler           # runs cron checker
arizuko web                 # runs HTTP
arizuko master              # supervises all of the above
```

Or: skip `arizuko master` entirely and let systemd manage
them as separate units with dependencies.

## Why separate processes

1. **Crash isolation**: telegram crashes, discord keeps
   running. Gateway crashes, channels keep buffering
   messages to DB.

2. **Independent restart**: deploy new channel code without
   restarting the gateway mid-conversation.

3. **Resource isolation**: cgroups per process. Web server
   gets rate-limited independently from agent runner.

4. **Language freedom**: channels could be any language that
   speaks SQLite. Gateway stays TS (or Go, or whatever).
   Each process is replaceable.

5. **Simpler code**: each process is ~200-500 LOC with one
   concern. No callback spaghetti, no shared mutable state.

6. **Testable**: test each process in isolation. Mock by
   writing rows to SQLite.

## Why NOT separate processes

1. **Operational complexity**: more things to monitor, more
   logs to correlate, more failure modes.

2. **SQLite contention**: multiple writers compete for the
   write lock. WAL helps but under load you get SQLITE_BUSY.

3. **Latency**: poll interval between processes adds delay.
   Could use NOTIFY (inotify on DB file) but adds complexity.

4. **Deployment**: now you ship and version multiple binaries
   (or one binary with subcommands, which is easier).

5. **Debugging**: tracing a message across 3 processes is
   harder than grep in one log.

## systemd as supervisor

Instead of writing `arizuko-master`, use systemd directly.
See "Component: Master" section below for the researched
design with template units and targets.

## Alternative: one binary, internal process isolation

Keep one `arizuko` binary but run subsystems as goroutines
(Go) or worker threads (Node) with the discipline of
separate processes:

- No shared mutable state between subsystems
- Communication only through SQLite (or in-memory channels
  that mirror the SQLite interface)
- Each subsystem has its own error boundary (supervisor
  restarts crashed goroutine)
- Can graduate to separate processes later without code
  changes

This is the "process discipline without process overhead"
approach. Erlang/OTP does this — separate actors with no
shared state, supervised restart, message passing.

## Outbox pattern detail

```
┌──────────┐     messages      ┌──────────┐     outbox       ┌──────────┐
│ channel  │ ──── INSERT ────→ │  SQLite  │ ←── INSERT ───── │ gateway  │
│ adapter  │ ←── SELECT ────── │   (WAL)  │ ──── SELECT ──→  │          │
└──────────┘     outbox        └──────────┘     messages      └──────────┘
```

Tables:

- `messages` — inbound messages from channels (+ scheduled)
- `outbox` — outbound messages from gateway to channels

Agent↔gateway IPC uses MCP over unix socket (see Component
6), not SQLite tables.

## What changes in the TS codebase

If we go multi-process (or process-discipline):

1. **Extract channel adapters**: each channel becomes a
   standalone script that connects to the API, writes
   messages to DB, reads outbox, sends replies. No
   imports from gateway code.

2. **Outbox table**: new table for gateway→channel messages.
   Channels poll it (or watch with inotify on the DB file).

3. **IPC via SQLite**: replace file-based IPC with
   ipc_requests/ipc_responses tables. Agent writes request
   row, gateway picks it up, writes response row.

4. **Gateway simplification**: gateway no longer manages
   channel connections. It just reads messages, spawns
   agents, writes outbox entries.

5. **Scheduler extraction**: scheduler becomes a loop that
   reads tasks table, inserts synthetic messages when tasks
   are due. No gateway imports needed.

## Open questions

### Q1: One binary or many?

One binary with subcommands (`arizuko gateway`,
`arizuko channel telegram`) means single artifact to
deploy. Many binaries means you can version/update
channels independently. One binary seems right for now.

### Q2: systemd or custom supervisor?

systemd gives us everything for free but ties us to Linux
and multiplies units per instance (5-7 units × N instances).
A custom supervisor (like Postfix master) is more portable
but we'd be writing process management code.

Third option: one process, goroutine/async supervisor
internally. Simplest to operate, still gets the
architectural benefits if we enforce no-shared-state.

### Q3: SQLite as IPC — fast enough?

Current file IPC is ~1ms (write file + signal). SQLite
INSERT + poll would be ~5-10ms with a 10ms poll interval.
Acceptable? Or do we need inotify on the DB file to get
sub-millisecond?

Could also use a unix socket sidecar: processes connect
to a local socket for real-time notifications, fall back
to SQLite polling if socket is down.

### Q4: Outbox polling vs notification

Channels need to know when outbox has new rows. Options:

- Poll every 100ms (simple, 100ms latency)
- inotify on SQLite WAL file (instant, but brittle)
- Unix socket notify (gateway sends "new outbox" ping)
- LISTEN/NOTIFY (PostgreSQL has this; SQLite doesn't)
- Shared memory semaphore (fast but complex)

Poll at 100ms is probably fine. Chat messages aren't
latency-sensitive at the millisecond level.

### Q5: Does this work in docker-compose?

If each process is a subcommand of one binary, yes:

```yaml
services:
  gateway:
    image: arizuko
    command: ['gateway']
    volumes: [./data:/srv/data]
  telegram:
    image: arizuko
    command: ['channel', 'telegram']
    volumes: [./data:/srv/data]
  web:
    image: arizuko
    command: ['web']
    volumes: [./data:/srv/data]
    ports: ['8080:8080']
```

But they'd need to share the SQLite file via a volume.
SQLite over network filesystems (NFS, Docker volumes
between containers) is unreliable. So either:

- All processes in one container (defeats the purpose)
- All processes on same host, shared bind mount
- Switch to PostgreSQL for multi-container (big change)

### Q6: TS or Go for the multi-process version?

The multi-process design is language-agnostic. Each
process is small enough that language choice per process
is fine. But practically:

- **TS everywhere**: share types, one build system, agent
  container and gateway speak the same language. Channel
  libs are best in TS.
- **Go for gateway+scheduler, TS for channels+agent**:
  gateway benefits from goroutines, channels benefit from
  npm ecosystem. But two languages.
- **TS now, replace hot paths later**: start TS, profile,
  replace bottlenecks with Go if needed (never needed for
  a chat gateway).

### Q7: Is multi-process premature?

The current monolith works. Breaking it apart adds
operational complexity. The architectural benefits
(crash isolation, independent restart, simpler code)
might not be worth it at current scale.

Counter: the process-discipline approach (one binary,
enforced separation, SQLite as interface) gives 80% of
the benefits with 20% of the operational cost. That
might be the sweet spot.

### Q8: What about the agent container?

Agent containers are already separate processes (docker).
The IPC-via-SQLite pattern would replace file-based IPC
for agent↔gateway communication. Agent writes to
ipc_requests table (mounted SQLite), gateway reads it.

But: agent containers currently don't have SQLite access.
They communicate via JSON files in a mounted directory.
Giving them SQLite access means mounting the DB file
(or a separate IPC-only DB) into the container.

Alternative: keep file IPC for agent↔gateway (it works),
only use SQLite outbox for channel↔gateway.

### Q9: What's the minimum viable change?

If we just want better architecture without going full
multi-process:

1. Add outbox table
2. Make channels read/write only through SQLite
   (no direct function calls to gateway)
3. Keep everything in one process
4. Enforce no-cross-imports between channel/gateway/web

This gives the clean separation without operational
complexity. Graduate to multi-process later if needed.

## Component 1: Channel Adapter

One process per platform. Independently replaceable.
Language follows best library for that platform.

**Contract**:

- IN: platform events (WebSocket, webhook, IMAP IDLE)
- OUT: `INSERT INTO messages (id, chat_jid, sender, ...)`
- IN: `SELECT FROM outbox WHERE channel=? AND sent_at IS NULL`
- OUT: send via platform API, `UPDATE outbox SET sent_at=now`

**Interface**: SQLite tables only. No imports from gateway.

**Implementations**:

- Telegram: TS (grammy) or Python (python-telegram-bot)
- Discord: TS (discord.js)
- WhatsApp: TS (baileys) — no rival in other languages
- Email: Go (go-imap) or Python (aioimaplib)

**Size**: ~200-400 LOC each. An agent can write one in
10 minutes given the schema contract.

## Component 2: Gateway

The brain. Reads messages, routes, spawns agents, writes
outbox. The only service that manages containers.

**Contract**:

- IN: `SELECT FROM messages WHERE timestamp > cursor`
- OUT: spawn agent (docker exec / claude CLI)
- IN: agent output (stdout JSONL)
- OUT: `INSERT INTO outbox (chat_jid, content, ...)`
- OUT: MCP server on unix socket (per-group)

**Interface**: SQLite + MCP unix socket + docker.

**Subcomponents** (within gateway):

- Message loop: polls messages table
- Group queue: per-group worker, semaphore for max containers
- Router: JID → channel resolution, message formatting
- MCP server: exposes tools to agent containers

**Size**: ~500-800 LOC. The most complex component.

## Component 3: Scheduler

**Reconsidered**: scheduled tasks ARE messages. A scheduler
is just another message producer — same as a channel adapter.
It writes to `messages` with its own origin/sender ID. The
gateway doesn't care who wrote the row.

This means: multiple schedulers work naturally (each with
its own ID on the bus). No coordination needed. A scheduler
could be a separate process, a cron job, or folded into the
gateway — the architecture doesn't constrain this.

**Contract**:

- IN: `SELECT FROM tasks WHERE next_run <= now`
- OUT: `INSERT INTO messages (origin='scheduler', ...)`
- OUT: `UPDATE tasks SET next_run = <next>`

**Open**: separate process or gateway-internal? Both work.
Keeping it open for now.

### Scheduling = messages with delivery time

No separate table. A scheduled message is just a message
with a future `deliver_at`. The gateway ignores messages
where `deliver_at` hasn't passed yet.

```sql
-- Added columns on messages table
deliver_at INTEGER,   -- NULL = immediate delivery
recurrence TEXT,      -- cron expr | interval ms | NULL = once
origin TEXT           -- channel name | 'scheduler' | 'slink' | 'agent'
```

Gateway poll query:

```sql
SELECT * FROM messages
WHERE timestamp > ?
  AND (deliver_at IS NULL OR deliver_at <= unixepoch())
ORDER BY timestamp
```

Recurring: when a recurring message fires, scheduler
inserts the next occurrence as a new row. Only logic
the scheduler needs.

Kinds (distinguished by content/origin, not a type field):

- Timed message: text with `deliver_at` in future
- Agent task: prompt with `origin='scheduler'`
- Event reaction: `deliver_at` set when event matches
  (`deliver_at = event_time + delay`)

### Commands

```
/remind 2h check the deploy       → message, deliver_at=now+2h
/remind 3pm standup                → message, deliver_at=3pm
/every day 9am standup prompt      → message, recurrence=cron
/after join 5m welcome             → event trigger (open design)
/cancel <id>                       → delete from messages
/scheduled                         → list where deliver_at > now
```

Created by: users (commands), agents (MCP tool), web UI,
other schedulers. All just INSERT into messages.

**Open**:

- Event triggers (react to join, keyword, agent done):
  how expressive? Separate `event_triggers` table or
  inline in messages?
- Recurring message ownership: who cleans up completed
  recurrences?
- Template support for delayed messages? ("Hi {name}")

## Extensions / packaging (early, unclear)

Extensions are github repos with a known structure so they
can be installed and run alongside the system. Examples:
channel adapters, MCP servers, schedulers, web plugins.

Rough idea:

- Repo follows a convention (entrypoint, config, schema)
- `arizuko install <repo>` clones and wires it in
- Extension declares what it is (channel, mcp-server,
  scheduler, web-handler) and what tables/sockets it needs
- Runs as a separate process, supervised same as builtins

**Lots unclear**: discovery, versioning, config injection,
security (extensions get DB access), upgrade path,
dependency between extensions. Not designed yet.

## Component: Master (process supervisor)

Keeps everything running. Starts services, restarts on
crash, manages lifecycle. Three options:

**A: systemd** — already using it. One unit per service
per instance. Gets watchdog, journald, cgroups for free.
Downside: N services × M instances = many units.

**B: Custom master** — like Postfix `master(8)`. One
process reads a config file listing what to run:

```toml
# /srv/data/arizuko_andy/services.toml
[gateway]
cmd = "arizuko gateway"

[telegram]
cmd = "arizuko channel telegram"
restart = "always"

[web]
cmd = "arizuko web"
restart = "on-failure"
```

Single systemd unit runs `arizuko master`, which
supervises children. Simpler operations (one unit per
instance), but we write supervision code.

**C: Docker compose** — each service in a container.
Problem: SQLite over shared volumes is fragile.

**D: Hybrid** — master runs in systemd, optionally wraps
services in docker containers for isolation. Like podman
quadlet or systemd-nspawn.

**Decision**: pure systemd with template units + targets.

### How it works

Template units use `@` — instance name substituted via `%i`:

```ini
# /etc/systemd/system/arizuko-gateway@.service
[Unit]
Description=arizuko gateway (%i)
PartOf=arizuko@%i.target
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/arizuko gateway
WorkingDirectory=/srv/data/arizuko_%i
EnvironmentFile=/srv/data/arizuko_%i/.env
Restart=on-failure
RestartSec=5
SyslogIdentifier=arizuko-gw-%i

[Install]
WantedBy=arizuko@%i.target
```

```ini
# /etc/systemd/system/arizuko-telegram@.service
[Unit]
Description=arizuko telegram (%i)
PartOf=arizuko@%i.target
After=arizuko-gateway@%i.service

[Service]
Type=simple
ExecStart=/usr/local/bin/arizuko channel telegram
WorkingDirectory=/srv/data/arizuko_%i
EnvironmentFile=/srv/data/arizuko_%i/.env
Restart=on-failure
RestartSec=2
SyslogIdentifier=arizuko-tg-%i

[Install]
WantedBy=arizuko@%i.target
```

Target groups everything per instance:

```ini
# /etc/systemd/system/arizuko@.target
[Unit]
Description=arizuko instance %i

[Install]
WantedBy=multi-user.target
```

### Operations

```bash
# Start/stop entire instance
systemctl start arizuko@andy.target
systemctl stop arizuko@andy.target

# Restart just telegram
systemctl restart arizuko-telegram@andy

# Enable on boot
systemctl enable arizuko@andy.target

# Status overview
systemctl status 'arizuko-*@andy*'

# Logs for one instance
journalctl -u 'arizuko-*@andy*' --since '5 min ago'

# Logs for just gateway
journalctl -u arizuko-gateway@andy
```

### CLI generates units

`arizuko create <name>` reads `.env`, generates and enables
the right units:

```bash
arizuko create andy
# → Creates /srv/data/arizuko_andy/.env
# → Generates arizuko-gateway@andy, arizuko-web@andy
# → Detects TELEGRAM_BOT_TOKEN → generates arizuko-telegram@andy
# → Enables arizuko@andy.target
# → systemctl start arizuko@andy.target
```

`arizuko enable andy telegram` / `arizuko disable andy telegram`
adds/removes channel units.

### Dependencies

- `PartOf=arizuko@%i.target` on all services — stop/restart
  target stops/restarts everything
- `After=arizuko-gateway@%i.service` on channels — gateway
  starts first
- Channels crash independently — gateway stays up, messages
  buffer in SQLite. PartOf (not BindsTo) gives this.

### Health

Start simple: `Restart=on-failure`. Graduate to
`Type=notify` + `WatchdogSec=30s` when stuck-process
detection matters (~5 LOC with sd_notify).

### Socket activation for MCP sidecars

systemd holds the unix socket, starts sidecar on first
connection. Agent container connects → systemd starts
whisper sidecar → passes socket fd:

```ini
# arizuko-mcp-whisper@.socket
[Socket]
ListenStream=/run/arizuko/%i/mcp-whisper.sock

# arizuko-mcp-whisper@.service
[Service]
ExecStart=/usr/local/bin/arizuko-mcp-whisper
```

Practical for sidecars (on-demand). Not for gateway
(needs to be always-running to poll).

### Updating after config changes

`arizuko provision <instance>` — reads `.env`, diffs against
existing units, adds/removes/restarts as needed:

```bash
vim /srv/data/arizuko_andy/.env   # add TELEGRAM_BOT_TOKEN
arizuko provision andy
# → Detected TELEGRAM_BOT_TOKEN → enable arizuko-telegram@andy
# → systemctl daemon-reload && start arizuko-telegram@andy
```

```bash
# Remove discord token from .env
arizuko provision andy
# → DISCORD_BOT_TOKEN missing → stop + disable arizuko-discord@andy
```

```bash
# After any config change, restart everything
arizuko restart andy
# → systemctl restart arizuko@andy.target
```

No magic. Edit `.env`, run `provision`. Predictable.

### Podman quadlet (future option)

Quadlet `.container` files let systemd manage containers
directly (no docker daemon). Template `%i` works in
quadlets too. Good for channel adapters that need
isolation. Not for ephemeral agent containers (quadlets
are for long-lived services).

## Pipeline language: langaxe (orthogonal component)

Agent-level pipelines (chaining tools, prompts, agents)
are handled by langaxe (`/home/onvos/app/langaxe`), a
separate project. Not part of arizuko — orthogonal.

langaxe provides:

- YAML definitions for tools (shell), prompts (LLM),
  agents (prompts with tools)
- Steps pipelines: ordered tool chains with stdin flow
- Agentic loop with budget tracking
- Sub-agents: prompts are tools, calling a prompt-tool
  spawns a sub-agent
- Symlink dispatch: `ln -s langaxe do` → `do "task"`
- Full tracing (tokens, cost, latency)
- Multi-provider (Anthropic, OpenAI, Ollama, etc.)

Example pipeline definition:

```yaml
researcher:
  system: Research topics thoroughly
  tools: [search, cat_file]
  token_budget: 100000

reviewer:
  system: Review research output
  tools: [researcher] # sub-agent!
```

For arizuko: agent containers could use langaxe as the
pipeline orchestrator instead of (or alongside) Claude
Code. A scheduled task could invoke a langaxe pipeline.
The gateway doesn't care — it just runs a command.

This is deferred. See `specs/v2/topicrouting.md` for
the routing/pipeline discussion.

## Not components: memory, skills, character

These shape agent behavior, not system architecture.
They live inside the container, not in the gateway:

- **Skills**: SKILL.md files mounted into container.
  Agent reads them, gains capabilities. Self-extensible
  (agent can write new skills).
- **Memory**: files in group folder (conversations/,
  diary/, CLAUDE.md). Persist across sessions. Agent
  reads/writes them.
- **Character**: character.json merged into system prompt.
  Persona, style, topics.

These are configuration of the agent, not building blocks
of the system. The architecture doesn't need to know about
them — it just mounts the right directories.

## Component 4: Web Server

HTTP server for auth, share links, vite proxy.

**Contract**:

- IN: HTTP requests
- OUT: static files, vite proxy, auth endpoints
- OUT: `INSERT INTO messages` (for slink inbound)
- IN/OUT: `auth_users`, `auth_sessions` tables

**Interface**: SQLite + HTTP.

**Size**: ~300-500 LOC. Any language.

## Component 5: Agent Runner

Thin wrapper that invokes Claude Code CLI inside a
container. Reads JSON from stdin, outputs JSONL on stdout.
Connects to gateway's MCP socket for actions.

**Contract**:

- IN: JSON on stdin `{prompt, sessionId, secrets, ...}`
- OUT: JSONL on stdout (results, progress, session IDs)
- OUT: MCP client → gateway socket for send_message, etc.

**Interface**: stdin/stdout JSON + MCP unix socket.

**Size**: ~50-100 LOC. Any language. Current implementation
is 700 LOC TS because it uses the SDK directly instead of
wrapping the CLI.

## Component 6: MCP IPC (replaces file-based IPC)

This is the biggest architectural change. The gateway
becomes an MCP server. Agent containers connect as MCP
clients over unix sockets.

### How it works

```
Gateway                          Agent Container
────────                         ──────────────
MCP server listening on          MCP client connects to
/data/ipc/<group>.sock           /workspace/ipc/gateway.sock
                                 (bind-mounted from host)
     ←── initialize ──────────
     ──── capabilities ───────→
     ←── tools/list ──────────
     ──── [send_message, ─────→
          schedule_task,
          register_group, ...]
     ←── tools/call ──────────
         {send_message,
          text: "hello"}
     ──── result: {ok: true} ─→
     ──── notification: ──────→   (bidirectional!)
         {new_message: "..."}
```

### What it replaces

| Current (file IPC)            | New (MCP socket)              |
| ----------------------------- | ----------------------------- |
| ipc-mcp-stdio.ts (333 LOC)    | eliminated                    |
| JSON files in mounted dir     | eliminated                    |
| SIGUSR1 signal to wake agent  | MCP notification (push)       |
| 500ms polling for responses   | synchronous RPC response      |
| \_close sentinel file         | MCP session close / transport |
| Per-request tmp file + rename | single socket connection      |

### Gateway MCP tools

Direct mapping from existing action registry:

| Tool             | Input                        | What it does          |
| ---------------- | ---------------------------- | --------------------- |
| `send_message`   | `{text, sender?}`            | send to chat channel  |
| `send_file`      | `{filepath, filename?}`      | send file to chat     |
| `schedule_task`  | `{prompt, type, value, ...}` | create scheduled task |
| `list_tasks`     | `{}`                         | list group's tasks    |
| `delete_task`    | `{task_id}`                  | delete a task         |
| `register_group` | `{jid, name, folder, ...}`   | register new group    |
| `clear_session`  | `{}`                         | reset agent session   |
| `delegate`       | `{folder, prompt}`           | delegate to subgroup  |

### Go implementation

The official MCP Go SDK makes this trivial:

```go
// Gateway side: MCP server on unix socket
ln, _ := net.Listen("unix", socketPath)
server := mcp.NewServer(
    &mcp.Implementation{Name: "arizuko", Version: "3.0"},
    nil,
)
mcp.AddTool(server,
    &mcp.Tool{Name: "send_message"},
    handleSendMessage,
)
// Accept connections (one per agent container)
conn, _ := ln.Accept()
transport := &mcp.IOTransport{Reader: conn, Writer: conn}
server.Connect(ctx, transport, nil)
```

```go
// Agent side: MCP client over unix socket
conn, _ := net.Dial("unix", "/workspace/ipc/gateway.sock")
client := mcp.NewClient(
    &mcp.Implementation{Name: "agent", Version: "1.0"},
    nil,
)
session, _ := client.Connect(ctx,
    &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
// Call tools
session.CallTool(ctx, &mcp.CallToolParams{
    Name: "send_message",
    Arguments: map[string]any{"text": "hello"},
})
```

### Why MCP over unix socket

1. **Claude Code already speaks MCP**: it's not a new
   protocol to learn — it's the native tool protocol
2. **Language-agnostic**: any MCP client in any language
   connects to the same socket, gets the same tools
3. **Bidirectional**: gateway can push messages to agent
   via notifications (replaces SIGUSR1 + file polling)
4. **Typed**: tool schemas are JSON Schema, auto-generated
   from Go structs, self-documenting
5. **Testable**: connect any MCP client to the socket,
   call tools, verify results. No docker needed.

### Open: Could Claude Code connect directly?

If the gateway MCP server is mounted at a path Claude Code
recognizes (via settings.json or --mcp-config), Claude Code
could connect to it directly — eliminating the agent runner
wrapper entirely. The "nanoclaw" MCP server becomes the
gateway socket.

**Researched**: Claude Code supports stdio, sse, and http
transports only. No native unix socket transport. But it
uses unix sockets internally (Chrome bridge).

**Solution**: stdio-to-socket proxy. Claude Code spawns it
as a stdio MCP server. Proxy bridges stdin/stdout to the
gateway's unix socket. One line:

```sh
#!/bin/sh
socat - UNIX-CONNECT:/workspace/ipc/gateway.sock
```

Configure in agent's MCP settings:

```json
{
  "nanoclaw": {
    "command": "socat",
    "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/gateway.sock"]
  }
}
```

Claude Code thinks it's a stdio server. Gateway gets a
socket connection. Zero custom code.

## Component 7: Store (shared schema)

Not a service — a schema contract. All services open the
same SQLite database in WAL mode.

**Tables**:

```sql
messages          -- inbound from channels
outbox            -- outbound from gateway to channels
tasks             -- scheduled work
task_run_logs     -- execution history
registered_groups -- group config + routing rules
chats             -- discovered chat metadata
sessions          -- active session per group
router_state      -- KV for cursors etc.
system_messages   -- gateway→agent notifications
auth_users        -- web auth
auth_sessions     -- web sessions
session_log       -- agent session history
```

**Rules**:

- WAL mode (concurrent readers + one writer)
- busy_timeout=5000 (wait up to 5s for write lock)
- Each service only writes to its own tables
- Channel adapters: write `messages`, read+update `outbox`
- Gateway: read `messages`, write `outbox`, manage all other
  tables
- Scheduler: read `tasks`, write `messages`+`tasks`
- Web: read/write `auth_*`, write `messages`

---

## The agent container: what it really is

The agent container (`container/agent-runner/`) is a thin
wrapper (~700 LOC) around `@anthropic-ai/claude-agent-sdk`.

What it does:

1. Reads JSON from stdin (prompt, sessionId, secrets)
2. Calls `query()` — Claude Code's full agentic loop
3. Streams results back via stdout markers
4. Handles IPC (file polling for follow-up messages)
5. Configures MCP servers (nanoclaw for gateway IPC)
6. Injects character/persona into system prompt
7. Hooks: archive on compaction, sanitize bash env

What it does NOT do:

- Implement any tools (Bash, Read, Write, Edit, Grep...)
- Manage the LLM conversation loop
- Handle permissions or sandboxing

All of that is inside Claude Code itself. The agent runner
just configures and invokes it.

### Three ways to drive Claude Code from any language

**Option A: Claude Code CLI as subprocess**

```python
# Python example
import subprocess, json

result = subprocess.run(
    ["claude", "--print", "--output-format", "json",
     "--session", session_id,
     "--permission-mode", "bypassPermissions",
     "--mcp-config", mcp_config_path],
    input=prompt,
    capture_output=True, text=True,
    env={**os.environ, "ANTHROPIC_API_KEY": key}
)
output = json.loads(result.stdout)
```

Or streaming with `--output-format stream-json`:

```go
// Go example
cmd := exec.CommandContext(ctx, "claude",
    "--output-format", "stream-json",
    "--permission-mode", "bypassPermissions",
    "--mcp-config", mcpConfigPath)
cmd.Stdin = strings.NewReader(prompt)
stdout, _ := cmd.StdoutPipe()
// read JSONL stream line by line
```

This is what takopi does (reference system). It shells
out to the Claude Code CLI and parses JSONL events.

Pros:

- Any language works. Agent container = 50 LOC.
- Claude Code handles all tools, permissions, sessions
- MCP servers configured via JSON file, not SDK API
- Session resume via --resume flag

Cons:

- CLI output format may change between versions
- No hook API (PreCompact, PreToolUse) from CLI
- Subprocess management overhead

**Option B: Anthropic API + custom tool loop**

Call the Messages API directly. Implement your own tool
dispatch. You get full control but must reimplement every
tool Claude Code provides (Bash, Read, Write, Edit, Grep,
Glob, WebSearch, WebFetch, Task, Skill, ...).

This is what brainpro/muaddib does — custom agent daemon
with its own tool implementations.

Verdict: massive effort. Only if you need tools Claude
Code doesn't provide, or need to run without Claude Code.

**Option C: MCP-native architecture**

MCP (Model Context Protocol) is language-agnostic. An
agent container could be any MCP client that connects to
MCP servers for tools. The gateway itself could be an
MCP server exposing send_message, schedule_task, etc.

This inverts the current architecture but aligns with
where the ecosystem is heading.

### The microservices argument

> "These are now very easy to create using agents. That's
> the main argument for ecosystem fracturing — iterate
> fast and allow anyone to use what they want."

If each service is small (~200-500 LOC) with a clear
contract (SQLite tables + stdout/stdin protocol), then:

- An agent can write a new channel adapter in 10 minutes
- A channel adapter in Python is just as valid as one in Go
- The gateway doesn't care what language wrote the row
- Anyone can contribute a service without learning the
  whole codebase

The contract is the schema, not the code:

```sql
-- Any process that INSERTs here is a channel adapter
CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    chat_jid TEXT NOT NULL,
    sender TEXT,
    name TEXT,
    content TEXT,
    timestamp INTEGER,
    bot_msg INTEGER DEFAULT 0,
    channel TEXT  -- which adapter wrote this
);

-- Any process that SELECTs here and sends is a channel
CREATE TABLE outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_jid TEXT NOT NULL,
    content TEXT,
    file_path TEXT,
    created_at INTEGER,
    sent_at INTEGER  -- NULL until channel marks sent
);
```

Language doesn't matter. Contract matters.

### Agent container as microservice

The agent container is already a microservice — it runs
in docker, communicates via stdin/stdout + file IPC.

To make it truly language-agnostic:

1. **Standardize the protocol**: JSON on stdin, JSONL on
   stdout. Document the exact schema. Any binary that
   speaks this protocol is a valid agent runner.

2. **Move from SDK to CLI**: replace the 700 LOC SDK
   wrapper with a 50 LOC subprocess wrapper that invokes
   `claude` CLI. Now the agent container is just a shell
   script.

3. **Or keep the SDK wrapper**: it provides hooks and
   streaming that CLI doesn't. But document the protocol
   so alternative implementations are possible.

The container image carries Claude Code + the runner.
Whether the runner is TS, Python, Go, or bash doesn't
matter — Claude Code binary inside the container does
the real work.

### Q10: Should we standardize on CLI instead of SDK?

The SDK (`@anthropic-ai/claude-agent-sdk`) gives us:

- Streaming message iteration
- Hook API (PreCompact, PreToolUse)
- MCP server configuration via code
- Session management via API

The CLI (`claude`) gives us:

- Language-agnostic invocation
- Simpler container (no node_modules for runner)
- MCP config via JSON file
- Session management via flags

What we'd lose going CLI-only:

- PreCompact hook (conversation archiving)
- PreToolUse hook (bash env sanitization)
- Fine-grained streaming (individual message types)
- MessageStream (piping IPC into active query)

The hooks are the blocker. If Claude Code CLI exposes
hooks (via config file or environment), the SDK wrapper
becomes unnecessary.

### Q11: What's the ideal agent container?

Minimal container that:

1. Reads JSON config from stdin
2. Writes a claude config file (MCP servers, hooks, etc.)
3. Invokes `claude` CLI as subprocess
4. Proxies stdout back to gateway
5. Handles IPC (polls for follow-up messages, pipes to
   claude's stdin or starts new invocation)

This is ~100 LOC in any language. The container image
needs: claude CLI binary + this thin wrapper + any MCP
server binaries.

### Verdict: SDK wrapper is unnecessary

The agent runner is just: write JSON to stdin, read JSONL
from stdout, parse, react, forward. This is trivial in
any language. The SDK adds convenience but no capability
that can't be replicated with subprocess + JSON parsing.

The two remaining SDK-specific features:

- **MessageStream** (piping IPC into active query): with
  CLI, just start a new `claude --resume` invocation per
  follow-up message. Simpler and more robust.
- **Hooks** (PreCompact, PreToolUse): move to Claude Code
  settings.json or post-processing. Conversation archival
  can run after the query ends. Bash env sanitization can
  be a settings rule.

The agent container becomes ~50-100 LOC in any language:
spawn `claude` CLI, pipe stdin/stdout, handle IPC.

### SDK landscape (researched 2026-03-06)

| SDK               | Languages           | What it gives you           |
| ----------------- | ------------------- | --------------------------- |
| Anthropic API SDK | TS, Python, Go,     | Messages API + tool use.    |
|                   | Java, Ruby, C#, PHP | You build the agent loop.   |
| Claude Agent SDK  | TS, Python          | Full runtime (all tools,    |
|                   |                     | sessions, hooks, MCP).      |
| Claude Code CLI   | any (subprocess)    | Full runtime via stdio.     |
|                   |                     | --output-format stream-json |
| MCP SDK           | TS, Python, Go,     | Build MCP servers/clients   |
|                   | C#, Java, Rust, +4  | in any language.            |

Go has official Anthropic API SDK + official MCP SDK.
Only missing: official Agent SDK (community port exists).
But CLI subprocess gives the same runtime from any language.

### Q12: Language per service — what makes sense?

Given the microservice architecture where contract = SQLite
schema + stdin/stdout protocol:

| Service          | Best language | Why                        |
| ---------------- | ------------- | -------------------------- |
| telegram adapter | TS or Python  | best libs (grammy, ptb)    |
| discord adapter  | TS            | discord.js is dominant     |
| whatsapp adapter | TS            | baileys has no real rival  |
| email adapter    | Python or Go  | imaplib/go-imap both solid |
| gateway/router   | any           | just SQLite + docker exec  |
| scheduler        | any           | trivial cron loop          |
| web server       | any           | HTTP + SQLite              |
| agent runner     | any (or bash) | thin CLI wrapper           |

The insight: only channel adapters have a strong language
preference (due to library ecosystems). Everything else
is language-agnostic.
