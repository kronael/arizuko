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
  ├── authd         (identity, authorization, JWT, OAuth, middleware)
  ├── chanreg       (channel registry, HTTP channel proxy)
  ├── gateway       (main loop, message routing)
  │   ├── container (docker spawn, volume mounts, sidecars, runtime)
  │   │   ├── groupfolder
  │   │   └── mountsec
  │   ├── queue     (per-group concurrency, stdin piping)
  │   ├── router    (message formatting, routing rules)
  │   ├── icmcd     (MCP server on unix socket, runtime auth via authd)
  │   ├── diary     (YAML frontmatter annotations)
  │   └── groupfolder
  └── compose       (docker-compose generation)

gated/main           (gateway daemon entrypoint)
  └── wires core + store + gateway + api + chanreg + icmcd + authd

teled/main           (telegram adapter daemon)
  └── calls router HTTP API + serves outbound endpoints

discd/main           (discord adapter daemon)
  └── calls router HTTP API + serves outbound endpoints

whapd/               (whatsapp adapter daemon, TypeScript)
  └── calls router HTTP API + serves outbound endpoints

timed/main           (scheduler daemon)
  └── polls scheduled_tasks, inserts messages into shared DB
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

**Standalone adapters**: `teled/` (Telegram, Go), `discd/`
(Discord, Go), and `whapd/` (WhatsApp, TypeScript) are external
adapter daemons. Each polls its platform API, forwards to router
HTTP, and serves `/send`, `/send-file`, `/typing`, `/health` for
outbound.

Full protocol: `specs/7/1-channel-protocol.md`.

## Key Types (core package)

| Type            | Purpose                                                      |
| --------------- | ------------------------------------------------------------ |
| `Config`        | All settings from `.env` + env vars                          |
| `Message`       | Incoming message (sender, content, reply context)            |
| `Group`         | Registered group (folder, trigger, config)                   |
| `GroupConfig`   | Per-group: mounts, timeout, sidecars                         |
| `Route`         | Flat routing table entry (type, match, target)               |
| `Task`          | Scheduled task (cron, prompt, status)                        |
| `Channel`       | Interface: Connect, Send, SendFile, Owns, Typing, Disconnect |
| `SessionRecord` | Session log entry                                            |

## SQLite Schema

| Table               | Key columns                                                                                    |
| ------------------- | ---------------------------------------------------------------------------------------------- |
| `chats`             | jid (PK), name, channel, is_group, errored                                                     |
| `messages`          | id (PK), chat_jid, sender, content, timestamp                                                  |
| `registered_groups` | jid (PK), folder, trigger_word, requires_trigger, container_config (JSON), parent, slink_token |
| `routes`            | id (auto), jid, seq, type, match, target                                                       |
| `sessions`          | group_folder (PK), session_id                                                                  |
| `session_log`       | id (auto), group_folder, session_id, started_at, ended_at, result, error                       |
| `system_messages`   | id (auto), group_id, origin, event, body                                                       |
| `scheduled_tasks`   | id (PK), owner, chat_jid, prompt, cron, next_run, status, created_at                           |
| `router_state`      | key (PK), value — persists lastTimestamp, lastAgentTimestamp                                   |
| `auth_users`        | sub (unique), username (unique), hash                                                          |
| `auth_sessions`     | token_hash (PK), user_sub, expires_at                                                          |
| `email_threads`     | thread_id (PK), chat_jid, subject                                                              |

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

## IPC Mechanism (icmcd package)

MCP server on unix socket (`mark3labs/mcp-go`). Gateway starts
one `icmcd` server per group before container spawn, listening on
`data/ipc/<folder>/nanoclaw.sock`. All 16 tools always registered;
runtime auth via `authd.Authorize`.

**Transport**: socat bridges the host unix socket into the container.
Agent-runner configures `nanoclaw` MCP server in `settings.json`
using `socat UNIX-CONNECT` to reach the socket.

**Lifecycle**: server starts before `docker run`, stops after
container exits. Socket file cleaned up on stop.

**Identity and authorization**: `icmcd` resolves caller identity
from socket path (folder, tier). Authorization checks delegated
to `authd.Authorize` at runtime.

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
5. **trigger** — trigger word match (group activation)
6. **default** — always matches

`IsAuthorizedRoutingTarget` — target must be direct child of source
within same world (root segment). Max delegation depth: 3.

## Scheduler (timed/)

Standalone daemon. Single Go binary with its own `main()`.
Reads `DATABASE` env for SQLite path. Polls `scheduled_tasks` every 60s.
For each due task (status=active, next_run <= now):

1. Insert prompt as message into `messages` table (sender: `scheduler`)
2. Compute next run via robfig/cron parser, update `next_run`
3. Tasks without cron expression get `next_run` set to NULL (one-shot)

Gateway picks up scheduler-injected messages in its normal poll loop.

**DB sharing**: timed opens the same SQLite DB as gated (WAL mode).
Own migration runner using shared `migrations` table (keyed by service
name `"timed"`). Schema: `timed/migrations/0001-schema.sql`
creates `scheduled_tasks` if not present (idempotent with store's copy).

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

`container.hp()` translates local paths to host paths when gateway runs
in docker. `HOST_DATA_DIR` env provides the host-side base.

## Configuration

All config via `.env` in data dir or env vars (`core.LoadConfig`).
Key values: `ASSISTANT_NAME`, `CONTAINER_IMAGE`, `IDLE_TIMEOUT`,
`MAX_CONCURRENT_CONTAINERS`, `HOST_DATA_DIR`, `HOST_APP_DIR`,
`MEDIA_ENABLED`, `WHISPER_BASE_URL`, `API_PORT`, `CHANNEL_SECRET`.

API server always starts (default port 8080).

## Repository Layout

```
cmd/arizuko/        CLI entrypoint (create, group, compose, status)
core/               Config, types, Channel interface
store/              SQLite persistence (messages, groups, sessions, tasks, auth)
api/                HTTP API server (channel protocol endpoints)
authd/              Identity, authorization, JWT, OAuth, middleware
chanreg/            Channel registry, health checks, HTTP channel proxy
gateway/            Main loop, message routing, commands
container/          Docker spawn, volume mounts, sidecars, runtime, skills seeding
  agent-runner/     In-container agent entrypoint
  skills/           Agent-side skills
queue/              Per-group concurrency, stdin piping
router/             Message formatting, routing rules
compose/            Docker-compose generation from *.toml service configs
icmcd/              MCP server (unix socket per group, runtime auth via authd)
diary/              YAML frontmatter diary annotations
groupfolder/        Group path resolution and validation
mountsec/           Mount allowlist validation
template/           Seed for new instances
sidecar/            MCP server binaries (whisper)
gated/              Gateway daemon
timed/              Scheduler daemon (cron poll, messages)
teled/              Telegram adapter (Go)
discd/              Discord adapter (Go)
whapd/              WhatsApp adapter (TypeScript)
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
