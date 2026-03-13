# Architecture

## Overview

Arizuko is a multitenant Claude agent router. External channel
adapters register via HTTP, deliver inbound messages to the router
API. Router routes to containerized Claude agents via docker and
calls channels back to send replies.

Go, SQLite (modernc.org/sqlite), Docker.

## Package Dependency Graph

```
cmd/arizuko/main
  ├── core          (Config, types, Channel interface)
  ├── store         (SQLite persistence)
  ├── api           (HTTP API: channel registration, inbound messages)
  │   ├── chanreg   (channel registry, health checks)
  │   └── store
  ├── chanreg       (channel registry, HTTP channel proxy)
  ├── gateway       (main loop, message routing)
  │   ├── container (docker spawn, volume mounts, sidecars)
  │   │   ├── groupfolder
  │   │   ├── mountsec
  │   │   └── runtime
  │   ├── queue     (per-group concurrency, stdin piping)
  │   │   └── runtime
  │   ├── router    (message formatting, routing rules)
  │   ├── ipc       (MCP server on unix socket)
  │   ├── scheduler (cron/interval/once task runner)
  │   │   └── store
  │   ├── diary     (YAML frontmatter annotations)
  │   └── groupfolder
  ├── compose      (docker-compose generation)
  └── logger        (slog JSON init)

channels/telegram/main  (standalone adapter binary)
  └── calls router HTTP API + serves outbound endpoints
```

## Message Flow

```
Channel adapter → POST /v1/messages (api) → store.PutMessage
  → gateway.messageLoop (polls every 2s)
  → store.NewMessages (unprocessed since lastTimestamp)
  → checkTrigger (direct mode or @name regex)
  → handleCommand (/new, /ping, /chatid, /stop)
  → router.ResolveRoutingTarget (delegate to child group if matched)
  → queue.SendMessage (stdin pipe to running container) OR
  → queue.EnqueueMessageCheck → processGroupMessages
    → store.MessagesSince (per-chat agent cursor)
    → store.FlushSysMsgs (XML system events prepended)
    → router.FormatMessages (XML message batch)
    → container.Run (docker run)
    → stream output → router.FormatOutbound (strip <internal> tags)
    → HTTPChannel.Send → POST /send to channel adapter
```

## Channel Protocol

Channels are external processes that register with the router
via HTTP. Both sides are HTTP servers.

**Inbound**: Channel adapter receives platform event, POSTs to
router `POST /v1/messages`. Router stores in SQLite, routes
to group.

**Outbound**: Router calls channel's registered URL
`POST /send`. Synchronous — 200 means delivered to platform.

**Registration**: On startup, channel `POST /v1/channels/register`
with name, callback URL, JID prefixes, and capabilities. Router
returns a session token for subsequent calls.

**Health**: Router pings `GET /health` every 30s. Three
consecutive failures triggers auto-deregister. Outbound queues
in `HTTPChannel.outbox` until channel re-registers.

**Auth**: Shared secret (`CHANNEL_SECRET`) for registration.
Session token for channel-to-router calls. Shared secret for
router-to-channel calls.

**Packages**: `chanreg/` (registry, health loop, `HTTPChannel`
proxy), `api/` (HTTP handlers for the router-side endpoints).

**Standalone adapters**: `channels/telegram/` is the first
external adapter. Polls telegram API, forwards to router HTTP,
serves `/send`, `/send-file`, `/typing`, `/health` for outbound.

Full protocol: `specs/7/1-channel-protocol.md`.

## Key Types (core package)

| Type            | Purpose                                                      |
| --------------- | ------------------------------------------------------------ |
| `Config`        | All settings from `.env` + env vars                          |
| `Message`       | Incoming message (sender, content, reply context)            |
| `Group`         | Registered group (folder, trigger, config)                   |
| `GroupConfig`   | Per-group: mounts, timeout, sidecars                         |
| `Route`         | Flat routing table entry (type, match, target)               |
| `Task`          | Scheduled task (cron/interval/once, prompt, status)          |
| `Channel`       | Interface: Connect, Send, SendFile, Owns, Typing, Disconnect |
| `ChatInfo`      | Chat metadata with errored flag                              |
| `SessionRecord` | Session log entry                                            |

## SQLite Schema

| Table               | Key columns                                                                                            |
| ------------------- | ------------------------------------------------------------------------------------------------------ |
| `chats`             | jid (PK), name, channel, is_group, errored                                                             |
| `messages`          | id (PK), chat_jid, sender, content, timestamp                                                          |
| `registered_groups` | jid (PK), folder, trigger_word, requires_trigger, container_config (JSON), parent, slink_token         |
| `routes`            | id (auto), jid, seq, type, match, target                                                               |
| `sessions`          | group_folder (PK), session_id                                                                          |
| `session_log`       | id (auto), group_folder, session_id, started_at, ended_at, result, error                               |
| `system_messages`   | id (auto), group_id, origin, event, body                                                               |
| `scheduled_tasks`   | id (PK), group_folder, chat_jid, prompt, schedule_type, schedule_value, context_mode, next_run, status |
| `task_run_logs`     | id (auto), task_id, run_at, duration_ms, status, result                                                |
| `router_state`      | key (PK), value — persists lastTimestamp, lastAgentTimestamp                                           |
| `auth_users`        | sub (unique), username (unique), hash                                                                  |
| `auth_sessions`     | token_hash (PK), user_sub, expires_at                                                                  |
| `email_threads`     | thread_id (PK), chat_jid, subject                                                                      |

WAL mode, 5s busy timeout. Migration via `PRAGMA user_version`.

## Container Lifecycle

1. `runtime.EnsureRunning()` — verify docker is reachable
2. `runtime.CleanupOrphans()` — stop stale `arizuko-*` containers
3. `container.Run()`:
   - Resolve group path via `groupfolder.Resolver`
   - `BuildMounts()` — assemble volume mounts (group, media, self, share, session, ipc, web, extra)
   - `mountsec.ValidateAdditionalMounts()` — check against allowlist
   - `seedSettings()` — write `settings.json` to session `.claude/` dir (env vars, nanoclaw MCP via socat, sidecar MCP config)
   - `seedSkills()` — copy `container/skills/` to session on first run
   - `StartSidecars()` — launch MCP sidecar containers (if configured)
   - `docker run -i --rm` with volume mounts, write JSON to stdin, read stdout
   - Parse output between `---NANOCLAW_OUTPUT_START---` / `---NANOCLAW_OUTPUT_END---` markers
   - Output shape: `{ status, result, newSessionId, error }`
   - Timer-based timeout with graceful stop then kill
   - `StopSidecars()` — stop sidecar containers after agent exits
   - Write log to `groups/<folder>/logs/container-<timestamp>.log`

Session management: new session ID from container output updates
`store.sessions`. On error with no output, session is evicted
(cursor rolled back so messages retry). On error with output,
cursor advances (partial work preserved).

## IPC Mechanism (ipc package)

MCP server on unix socket (`mark3labs/mcp-go`). Gateway starts
one `ipc.Server` per group before container spawn, listening on
`data/ipc/<folder>/nanoclaw.sock`.

**Tools exposed**: `send_message`, `send_file`, `reset_session`,
`delegate_to_child`, `create_task`, `list_tasks`.

**Transport**: socat bridges the host unix socket into the container.
Agent-runner configures `nanoclaw` MCP server in `settings.json`
using `socat UNIX-CONNECT` to reach the socket.

**Lifecycle**: server starts before `docker run`, stops after
container exits. Socket file cleaned up on stop.

Authorization: non-root groups can only send to registered JIDs.

## Sidecar Management (container/sidecar.go)

Per-group MCP sidecars defined in `GroupConfig.Sidecars`:

1. `StartSidecars()` — `docker run -d` each sidecar with socket volume
   at `ipc/sidecars/<name>.sock`, resource limits (memory, CPUs), network mode
2. Socket path wired into agent `settings.json` as `mcpServers` entry
   using `socat UNIX-CONNECT` transport
3. `StopSidecars()` — `docker stop` then `docker rm -f` on exit

## Queue (queue package)

`GroupQueue` serializes agent invocations per group:

- `maxConcurrent` global limit (default 5)
- Per-group state: active flag, pending messages/tasks, container name
- Circuit breaker: 3 consecutive failures opens breaker (reset by new message)
- `EnqueueMessageCheck` → `runForGroup` → `processMessages` callback
- `EnqueueTask` → `runTask` → task function
- `drainGroupLocked` — after completion, run pending tasks then messages then waiting groups
- `SendMessage` — write to IPC input dir for live stdin piping

## Routing Rules (router package)

`ResolveRoutingTarget(msg, rules)` evaluates in tier order:

1. **command** — exact prefix match (e.g. `/code`)
2. **pattern** — regex match on content (max 200 chars)
3. **keyword** — case-insensitive substring
4. **sender** — regex on sender name
5. **default** — always matches

`IsAuthorizedRoutingTarget` — target must be direct child of source
within same world (root segment). Max delegation depth: 3.

## Scheduler (scheduler package)

Polls `store.DueTasks()` every 60s. For each due task:

1. Verify task still active
2. Enqueue via `queue.EnqueueTask` (respects concurrency limits)
3. Run agent with task prompt
4. Log run in `task_run_logs`
5. Compute next run: cron (via robfig/cron parser), interval (ms), or mark once-tasks complete

## Diary System (diary package)

`diary.Read(groupDir, max)` reads recent `.md` files from `group/diary/`.
Extracts `summary:` from YAML frontmatter. Returns XML annotations
with age labels (today, yesterday, N days/weeks ago). Prepended to
agent prompt as `<knowledge layer="diary">`.

## Error Handling

- Per-chat `errored` flag in `chats` table
- On agent error with no output: cursor rolled back, flag set, retry on next message
- On agent error with output: cursor advances, partial output preserved
- Circuit breaker in queue: 3 failures → stop processing until new message arrives
- Container timeout: graceful `docker stop`, then `Process.Kill`

## Mount Security (mountsec package)

Additional mounts validated against `~/.config/arizuko/mount-allowlist.json`:

- Path must be absolute, resolve symlinks, exist on host
- Must be under an `AllowedRoot`
- Blocked patterns: `.ssh`, `.gnupg`, `.env`, credentials, private keys
- Non-root groups forced read-only when `NonMainReadOnly` is set
- Container path: `/workspace/extra/<name>`

## Docker-in-Docker Path Translation

`container.hp()` translates local paths to host paths when gateway runs in
docker. `Config.HostProjectRoot` (from `HOST_DATA_DIR` env) provides the
host-side base. All volume mount paths go through `hp()` before being
passed to `docker run`.

## Configuration

All config via `.env` in data dir or env vars (`core.LoadConfig`).
Key values: `ASSISTANT_NAME`, `CONTAINER_IMAGE`, `IDLE_TIMEOUT`,
`MAX_CONCURRENT_CONTAINERS`, `HOST_DATA_DIR`, `HOST_APP_DIR`,
`MEDIA_ENABLED`, `WHISPER_BASE_URL`, `API_PORT`, `CHANNEL_SECRET`.

API server always starts (default port 8080). Channel adapters
are external processes that register via `POST /v1/channels/register`.

## Gateway Commands

| Command      | Effect                                    |
| ------------ | ----------------------------------------- |
| `/new [msg]` | Clear session, optionally process message |
| `/ping`      | Status: group, session, active containers |
| `/chatid`    | Echo the chat JID                         |
| `/stop`      | Stop running container for this chat      |

## Repository Layout

```
cmd/arizuko/        CLI entrypoint (run, create, group, compose, status)
core/               Config, types, Channel interface
store/              SQLite persistence (messages, groups, sessions, tasks, auth)
api/                HTTP API server (channel protocol endpoints)
chanreg/            Channel registry, health checks, HTTP channel proxy
gateway/            Main loop, message routing, commands
container/          Docker spawn, volume mounts, sidecars, skills seeding
  agent-runner/     In-container agent entrypoint
  skills/           Agent-side skills
queue/              Per-group concurrency, stdin piping
router/             Message formatting, routing rules
compose/            Docker-compose generation from services/*.toml
ipc/                MCP server (unix socket per group)
scheduler/          Cron/interval/once task runner
diary/              YAML frontmatter diary annotations
groupfolder/        Group path resolution and validation
mountsec/           Mount allowlist validation
runtime/            Docker binary, orphan cleanup
logger/             slog JSON init
template/           Seed for new instances
sidecar/            MCP server binaries (whisper)
channels/telegram/  Standalone telegram adapter binary
```

## Data Directory

`/srv/data/arizuko_<name>/` per instance:

- `.env` — config
- `store/` — SQLite DB (`messages.db`)
- `groups/<folder>/` — group files, logs, diary, media
- `groups/<world>/share/` — cross-group shared state
- `data/ipc/<folder>/` — MCP unix sockets + sidecar sockets
- `data/sessions/<folder>/.claude/` — agent session (settings, skills, CLAUDE.md)
- `web/` — vite web app
