# Architecture

## Package Dependency Graph

```
cmd/arizuko/main
  ├── core          (Config, types, Channel interface)
  ├── store         (SQLite persistence)
  ├── api           (HTTP API: channel registration, inbound messages)
  │   ├── chanreg   (channel registry, health checks)
  │   └── store
  ├── auth          (identity, authorization, JWT, OAuth, middleware)
  ├── chanreg       (channel registry, HTTP channel proxy)
  ├── gateway       (main loop, message routing)
  │   ├── container (docker spawn, volume mounts, sidecars, runtime)
  │   │   ├── groupfolder
  │   │   └── mountsec
  │   ├── queue     (per-group concurrency, stdin piping)
  │   ├── router    (message formatting, routing rules)
  │   ├── ipc       (MCP server on unix socket, runtime auth via auth)
  │   ├── diary     (YAML frontmatter annotations)
  │   └── groupfolder
  └── compose       (docker-compose generation)

gated/main           (gateway daemon entrypoint)
  └── wires core + store + gateway + api + chanreg + ipc + auth

teled/main           (telegram adapter daemon)
  └── calls router HTTP API + serves outbound endpoints

discd/main           (discord adapter daemon)
  └── calls router HTTP API + serves outbound endpoints

mastd/main           (mastodon adapter daemon)
  └── WebSocket streaming + REST API; calls router HTTP API

bskyd/main           (bluesky adapter daemon)
  └── AT Protocol polling; calls router HTTP API

reditd/main          (reddit adapter daemon)
  └── OAuth2 inbox/subreddit polling; calls router HTTP API

emaid/main           (email adapter daemon)
  └── IMAP TLS polling + SMTP STARTTLS replies; calls router HTTP API

whapd/               (whatsapp adapter daemon, TypeScript)
  └── calls router HTTP API + serves outbound endpoints

timed/main           (scheduler daemon)
  └── polls scheduled_tasks, inserts messages into shared DB

onbod/main           (onboarding daemon)
  └── state machine, /send HTTP endpoint, poll loop

grants/              (library)
  └── CheckAction, NarrowRules, MatchingRules, DeriveRules

chanlib/             (library)
  └── RouterClient, InboundMsg, InboundAttachment, NewAdapterMux — shared by all channel adapters

dbmig/               (library)
  └── Shared SQLite migration framework (keyed by service name)
```

## Message Flow

```
Channel adapter → POST /v1/messages (api) → store.PutMessage
  → gateway.messageLoop (polls every 2s)
  → store.NewMessages (unprocessed since lastTimestamp)
  → store.ActiveWebJIDs (web: JIDs with recent messages, polled same loop)
  → impulseGate.accept(jid, msgs) — weight-based batching; skip JID if under
    threshold (social verbs=0, default=100, max_hold=5min flush)
  → checkTrigger (direct mode or @name regex)
  → handleCommand (/new [#topic], /ping, /chatid, /stop)
  → prefix dispatch (@name → named group, #topic → topic session)
  → router.ResolveRoutingTarget (delegate to child group if matched)
  → queue.SendMessage (stdin pipe to running container) OR
  → queue.EnqueueMessageCheck → processGroupMessages
    → web: JID → processWebTopics (per-topic agent run)
    → filter out gateway commands (isGatewayCommand — not forwarded to agent)
    → enricher: download attachments → groups/<folder>/media/<YYYYMMDD>/ (voice → Whisper)
    → store.EnrichMessage (update content with <attachment> XML, clear attachments column)
    → store.MessagesSince (per-chat agent cursor)
    → store.FlushSysMsgs (XML system events prepended)
    → router.FormatMessages (XML message batch)
    → grants.DeriveRules → inject into start.json
    → container.Run (docker run)
    → stream output → router.FormatOutbound (strip <internal> tags)
    → HTTPChannel.Send → POST /send to channel adapter
```

## Web Channel (proxyd)

Web chat uses `web:<folder>` JIDs. Messages arrive via `proxyd` (HTTP/SSE),
are stored directly in the shared SQLite DB. The gateway poll loop discovers
active `web:` JIDs via `store.ActiveWebJIDs(since)` and routes them like any
other channel. Each web message carries a topic; `processWebTopics` in the
gateway splits messages by topic and runs one agent per topic.

**Path model**:

- `/pub/*` — public, no auth required
- `/health` — public health check
- `/slink/*` — rate-limited (10 req/min per IP); token resolved against
  `groups.slink_token`, injects `X-Folder`, `X-Group-Name`,
  `X-Slink-Token`, then proxies to webd (or vite if no webd)
- `/dash/*` — auth-gated, proxied to dashd
- `/dav/*` — auth-gated, proxied to dufs WebDAV container (strips `/dav` prefix)
- `/*` — everything else auth-gated

**Auth resolution** in `requireAuth`:

1. `Authorization: Bearer <jwt>` — validates JWT, injects `X-User-Sub`,
   `X-User-Name`, optionally `X-User-Groups` (JSON array).
2. `refresh_token` cookie — fallback for browser navigation without JS Bearer
   header. Looks up session by `auth.HashToken(cookie)`, injects `X-User-Sub`
   and `X-User-Name`.
3. Redirects to `/auth/login` if neither check passes.

**JWT plane**: user logs in via `/auth/`, proxyd validates the JWT and injects
`X-User-Sub` and optionally `X-User-Groups` (JSON array of folder names).
`groups: null` in the JWT = operator (unrestricted); `groups: []` = no access;
`groups: ["folder"]` = specific folders. Group list is read from `user_groups`
table at login time and embedded in the JWT.

`webd.requireFolder` middleware checks `X-User-Groups` on folder-specific
endpoints. Absent header = operator (no restriction).

`groupForJid` in the gateway resolves `web:<folder>` by stripping the prefix
and looking up the group by folder path, the same fallback used by `slink:`.

WebDAV at `/dav/` requires `DAV_ADDR` configured; the backing container
(`sigoden/dufs:latest`) mounts `groups/` read-only.

proxyd receives `DATA_DIR` from compose to locate `web/vhosts.json` (reloaded
every 5s without restart).

Full protocol: `specs/6/3-chat-ui.md`.

## Auth Hardening

- **Secure cookies**: `refresh_token` and OAuth state cookies include
  `Secure: true` when `authBaseURL(cfg)` starts with `https://`. Derived
  once at route registration in `auth/middleware.go`.

- **Login rate limiting**: `loginAllowed(ip)` in `auth/web.go` allows at most
  5 POST `/auth/login` attempts per IP per 15-minute sliding window.
  In-memory; resets on restart. Returns HTTP 429 on breach.

- **Telegram replay protection**: `verifyTelegramWidget` rejects payloads
  where `auth_date` is older than 5 minutes (guards against replay attacks on
  the Telegram Login Widget).

- **OAuth providers**: Google (`GOOGLE_CLIENT_ID`), GitHub (`GITHUB_CLIENT_ID`,
  optional `GITHUB_ALLOWED_ORG` for org membership gate), Discord
  (`DISCORD_CLIENT_ID`). Login page shows provider buttons when the
  corresponding env is set. All providers use the shared `createOAuthSession`
  path in `auth/oauth.go`.

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
(Discord, Go), `mastd/` (Mastodon, Go), `bskyd/` (Bluesky, Go),
`reditd/` (Reddit, Go), `emaid/` (Email IMAP/SMTP, Go), and
`whapd/` (WhatsApp, TypeScript) are external adapter daemons.
Each polls its platform API, forwards to router HTTP, and serves
`/send`, `/send-file`, `/typing`, `/health` for outbound. All
Go adapters share `chanlib/` for `RouterClient`, `InboundMsg`,
and auth middleware.

Full protocol: `specs/4/1-channel-protocol.md`.

## Inbound Media Pipeline

When a user sends a photo, voice message, or document:

1. Channel adapter (teled, discd) extracts file metadata and populates
   `Attachments []InboundAttachment` in the `POST /v1/messages` body.
2. `store.PutMessage` stores the attachment JSON in `messages.attachments`.
3. Before agent spawn, the gateway enricher fetches each attachment URL,
   writes the file to `groups/<folder>/media/<YYYYMMDD>/<filename>`, and
   calls `store.EnrichMessage` to prepend `<attachment path="..." mime="..." filename="..."/>`
   XML into the message content and clear the `attachments` column.
4. Voice files (`audio/*`) are sent to Whisper for transcription when
   `VOICE_TRANSCRIPTION_ENABLED=true`; transcript appended to the attachment tag.
5. Agent sees attachment XML inline in message content. Container path is
   `/home/node/media/...` (bound from `groups/<folder>/media/`).

teled serves `GET /files/{fileID}` as a proxy to the Telegram CDN, since
Telegram file URLs are ephemeral and require a bot token. discd uses
direct CDN URLs (no proxy needed).

**Config**: `MEDIA_ENABLED=true` (required to activate enricher), `VOICE_TRANSCRIPTION_ENABLED=true`,
`WHISPER_BASE_URL=http://...` (e.g. OpenAI Whisper container), `WHISPER_MODEL=turbo` (default).

## Key Types (core package)

| Type            | Purpose                                                                                                              |
| --------------- | -------------------------------------------------------------------------------------------------------------------- |
| `Config`        | All settings from `.env` + env vars                                                                                  |
| `Message`       | Incoming message (sender, content, reply context)                                                                    |
| `Group`         | Registered group (folder, trigger, config)                                                                           |
| `GroupConfig`   | Per-group: mounts, timeout, sidecars                                                                                 |
| `Route`         | Flat routing table entry (type, match, target)                                                                       |
| `Task`          | Scheduled task (cron, prompt, status)                                                                                |
| `Channel`       | Interface: Connect, `Send(jid, text, replyTo) (id, error)`, `SendFile(jid, path, caption)`, Owns, Typing, Disconnect |
| `SessionRecord` | Session log entry                                                                                                    |

## SQLite Schema

| Table             | Key columns                                                                                                           |
| ----------------- | --------------------------------------------------------------------------------------------------------------------- |
| `chats`           | jid (PK), name, channel, is_group, errored, agent_cursor                                                              |
| `messages`        | id (PK), chat_jid, sender, content, timestamp, verb, attachments (JSON, cleared after enrich)                         |
| `groups`          | folder (PK), name, added_at, container_config (JSON), slink_token, parent, state, spawn_ttl_days, archive_closed_days |
| `routes`          | id (auto), jid, seq, type, match, target                                                                              |
| `sessions`        | group_folder + topic (PK), session_id                                                                                 |
| `session_log`     | id (auto), group_folder, session_id, started_at, ended_at, result, error                                              |
| `system_messages` | id (auto), group_id, origin, event, body                                                                              |
| `scheduled_tasks` | id (PK), owner, chat_jid, prompt, cron, next_run, status, created_at                                                  |
| `router_state`    | key (PK), value — persists lastTimestamp, lastAgentTimestamp                                                          |
| `auth_users`      | sub (unique), username (unique), hash                                                                                 |
| `auth_sessions`   | token_hash (PK), user_sub, expires_at                                                                                 |
| `user_groups`     | user_sub + folder (PK) — restricts web user to specific group folders                                                 |
| `email_threads`   | thread_id (PK), chat_jid, subject                                                                                     |
| `onboarding`      | jid (PK), status, prompted_at                                                                                         |

WAL mode, 5s busy timeout. Migration via `PRAGMA user_version`.

Agent output, delegation, and escalation all flow through a single
`PutMessage()` path into the `messages` table. Bot-authored rows are
marked `is_from_me=1 is_bot_message=1` and filtered out of the inbound
polling query. `topic` and `routed_to` capture audit metadata. Full spec:
`specs/7/22-audit-log.md`.

## Container Lifecycle

1. `runtime.EnsureRunning()` — verify docker is reachable
2. `runtime.CleanupOrphans()` — stop stale `arizuko-*` containers
3. `container.Run()`:
   - Resolve group path via `groupfolder.Resolver`
   - `BuildMounts()` — assemble volume mounts (group, media, self, share, session, ipc, web, extra)
   - `mountsec.ValidateAdditionalMounts()` — check against allowlist
   - `seedSettings()` — write `settings.json` to `groups/<folder>/.claude/` (env vars, arizuko MCP via socat, sidecar MCP config)
   - `seedSkills()` — copy `ant/skills/` to session on first run; also seeds `.claude.json` if missing (SDK requires it; keyed by folder for stable userID hash)
   - `StartSidecars()` — launch MCP sidecar containers (if configured)
   - Container name: `arizuko-<folder>-<timestamp_ms>` for regular runs;
     `arizuko-<folder>-task-<task_id>` for isolated scheduler tasks
     (sender `scheduler-isolated:<task_id>`)
   - `docker run -i --rm` with volume mounts, write JSON to stdin, read stdout
   - Parse output between `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---` markers
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
one `ipc` server per group before container spawn, listening on
`ipc/<folder>/gated.sock`. Tools registered from MCP manifest
filtered by grants rules for the caller's group; runtime auth via
`auth.Authorize`. `set_grants`/`get_grants` tools allow agents to
read and write grant rules. `delegate_group` calls `NarrowRules`
to merge parent+child rules before persisting.

**Transport**: socat bridges the host unix socket into the container.
Agent-runner configures `arizuko` MCP server in `settings.json`
using `socat UNIX-CONNECT` to reach the socket.

**Lifecycle**: server starts before `docker run`, stops after
container exits. Socket file cleaned up on stop.

**Identity and authorization**: `ipc` resolves caller identity
from socket path (folder, tier). Authorization checks delegated
to `auth.Authorize` at runtime.

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
- Per-group state: active flag, container name
- Circuit breaker: 3 consecutive failures opens breaker (reset by new message)
- `EnqueueMessageCheck(jid)` — signal-only; queue calls back into
  `HasPendingMessages`/`processMessages` to read pending work from the DB
- `drainGroupLocked` — on completion, checks DB for more pending messages,
  starts next waiting group if capacity allows
- `SendMessage` — write to IPC input dir for live stdin piping

Delegation, escalation, and `#topic` routing are not special queue
operations — each writes a row via `PutMessage` and calls
`EnqueueMessageCheck`. There is no closure-based task machinery.

## Routing

Route table, route types, topic routing, reply routing, sticky routing,
and full message flow examples: **`docs/routing.md`**.

## Grants Engine (grants package)

Rule strings control which MCP tools and actions a group may use. Rule format:
`[!]action[(param=glob,...)]`. Evaluation is last-match-wins; no match = deny.

- `CheckAction(rules, action, params)` — returns allow/deny
- `NarrowRules(parent, child)` — merges rules; child can only narrow, never widen
- `MatchingRules(rules, action)` — returns rules matching a given action
- `DeriveRules(store, folder, tier, worldFolder)` — computes default rules from
  group tier: tier-0 gets `*`, tier-1 gets platform send actions + management
  tools, tier-2 gets send only, deeper gets `send_reply` only

Rules are derived at container spawn time and injected into `start.json`.
The `ipc` MCP manifest is filtered by grants so agents only see permitted tools.

## Compose Containers

`compose.Generate(dataDir)` builds a `docker-compose.yml` from two sources:

1. **Built-in services** — always emitted based on `.env` profile:
   - `gated` (always), `timed`, `dashd` (profile=full), `proxyd`+`vited` (WEB_PORT set)
   - `onbod` when `ONBOARDING_ENABLED=true`

2. **Extra services** — TOML files dropped into `<dataDir>/services/`.
   Each `.toml` declares one extra compose service (channel adapter or sidecar).
   `compose.Generate` reads them all and appends to the compose output.

### Service catalog (`template/services/`)

Bundled products ship in the arizuko image at `/opt/arizuko/template/services/`.
Ansible extracts them to `/srv/app/arizuko/template/services/` on deploy.

| Service       | Image                     | Role                       |
| ------------- | ------------------------- | -------------------------- |
| `teled.toml`  | `arizuko:latest`          | Telegram adapter (default) |
| `whapd.toml`  | `arizuko-whatsapp:latest` | WhatsApp adapter           |
| `discd.toml`  | `arizuko:latest`          | Discord adapter            |
| `bskyd.toml`  | `arizuko:latest`          | Bluesky adapter            |
| `mastd.toml`  | `arizuko:latest`          | Mastodon adapter           |
| `reditd.toml` | `arizuko:latest`          | Reddit adapter             |

### TOML format

```toml
image = "arizuko:latest"
entrypoint = ["teled"]          # overrides image entrypoint
depends_on = ["gated"]          # optional; defaults to [gated]
volumes = ["${DATA_DIR}/..."]   # optional extra mounts; ${VAR} interpolated from .env

[environment]
ROUTER_URL = "http://gated:${API_PORT}"   # ${VAR} interpolated
CHANNEL_SECRET = "${CHANNEL_SECRET}"
```

### Container naming

All containers named `<app>_<service>_<flavor>` e.g. `arizuko_teled_REDACTED`.
Prevents conflicts when multiple instances run on the same host.

### Enabling products per instance

Operator copies the desired service TOMLs into
`/srv/data/arizuko_<flavor>/services/` before starting the instance.
Ansible automates this via `arizuko_instances[].extra_services`.

`onbod` is auto-included in compose when `ONBOARDING_ENABLED=true`. Compose
sets `ONBOD_LISTEN_ADDR=:8092` to avoid conflict with `dashd` (`:8090`).
Without compose, onbod defaults to `:8091`. `ONBOARDING_ENABLED` defaults to
false in gated; set to `true` to surface unrouted JIDs for the onboarding handler.

## Onboarding Daemon (onbod/)

Standalone daemon. Registers itself as a channel with the router, seeds `/approve`
and `/reject` command routes in the `routes` table on startup.

State machine per JID (`onboarding` table):

```
awaiting_message → (greeting + user leaves message) → pending → (admin approves from dashboard, picks folder) → approved
                                                               → (admin rejects)                               → rejected
```

Poll loop (every 10s):

1. Prompt unanswered `awaiting_message` records
2. User leaves a message → transition to `pending`, notify tier-0 JIDs
3. Respond to pending users who send messages: "Still waiting for approval"

On approve: creates group dir, optionally copies prototype, inserts
`groups` row and default route in `routes` table, sends welcome system event message.
Uses `notify` library to fan out messages to all tier-0 root JIDs.

Prototype copy behavior: `CLAUDE.md` and `SOUL.md` are copied; session and
memory are not. Agents can also spawn children directly via the
`register_group` MCP tool with `fromPrototype=true`. Full spec: `specs/4/26-prototypes.md`.

## Scheduler (timed/)

Full spec: `specs/4/8-scheduler-service.md`.

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

## Operator Dashboard (dashd/)

Standalone read-only HTMX portal. Full spec: `specs/7/25-dashboards.md`.

Serves on `DASH_PORT` (default 8090). Opens SQLite read-only (`?mode=ro`).
Six views: portal (tile grid, 30s auto-refresh), status (channels, groups,
containers, queue, errors), tasks (scheduled tasks + run history), activity
(message flow, routing table), groups (hierarchy tree), memory (per-group
knowledge browser). Auth via JWT cookie (imports `auth` library).

URL convention: `/dash/` portal, `/dash/<name>/` page,
`/dash/<name>/x/<fragment>` HTMX partial, `/dash/<name>/api/<path>` JSON API.

dashd registers in the channels table as `receive_only: true`. The `/status`
command routes to dashd via the routes table; dashd replies via `notify/`.
Included in generated `docker-compose.yml` as `arizuko_dashd_<flavor>`.

## Diary System (diary package)

`diary.Read(groupDir, max)` reads recent `.md` files from `group/diary/`.
Extracts `summary:` from YAML frontmatter. Returns XML annotations
with age labels (today, yesterday, N days/weeks ago). Prepended to
agent prompt as `<knowledge layer="diary">`.

Two-layer memory model: `MEMORY.md` for long-term knowledge, daily diary
files for work log. Diary nudged by `/diary` skill, PreCompact hook, and
every 100 turns. Full spec: `specs/1/L-memory-diary.md`.

Episodes (compressed session transcripts) follow the same `summary:` format
and are indexed by `/recall`. Compression hierarchy: daily → weekly → monthly.
See `specs/4/24-recall.md` for the recall protocol and episode format.

## Error Handling

- Per-chat `errored` flag in `chats` table
- On agent error with no output: cursor rolled back, flag set, retry on next message
- On agent error with output: cursor advances, partial output preserved
- Circuit breaker in queue: 3 failures → stop processing until new message arrives
- Container timeout: graceful `docker stop`, then `Process.Kill`

## Mount Security (mountsec package)

`ValidateAdditionalMounts` validates group-configured extra mounts against
a caller-supplied `Allowlist`. `ValidateFilePath` guards inbound file paths
(e.g. MCP tool arguments) against symlink escapes and a default blocklist
(`.ssh`, `.gnupg`, `.env`, credentials, private keys). Container path for
validated mounts: `/workspace/extra/<name>`.

## Docker-in-Docker Path Translation

`container.hp()` translates local paths to host paths when gateway runs
in docker. `HOST_DATA_DIR` env provides the host-side base.

## Repository Layout

```
cmd/arizuko/        CLI entrypoint (generate, run, create, group, status)
core/               Config, types, Channel interface
store/              SQLite persistence (messages, groups, sessions, tasks, auth)
api/                HTTP API server (channel protocol endpoints)
auth/               Identity, authorization, JWT, OAuth, middleware
chanreg/            Channel registry, health checks, HTTP channel proxy
gateway/            Main loop, message routing, commands
container/          Docker spawn, volume mounts, sidecars, runtime, skills seeding (Go)
ant/                In-container agent (TypeScript entrypoint + skills)
  skills/           Agent-side skills seeded into ~/.claude/skills/
queue/              Per-group concurrency, stdin piping
router/             Message formatting, routing rules
compose/            Docker-compose generation from *.toml service configs
ipc/                MCP server (unix socket per group, runtime auth via auth)
diary/              YAML frontmatter diary annotations
groupfolder/        Group path resolution and validation
mountsec/           Mount allowlist validation
template/           Seed for new instances
sidecar/            MCP server binaries (whisper)
chanlib/            Shared HTTP + auth primitives for channel adapters
dbmig/              Shared SQLite migration framework
grants/             Grant rule engine
notify/             Operator notification fan-out (library)
gated/              Gateway daemon
timed/              Scheduler daemon (cron poll, messages)
onbod/              Onboarding daemon (auto-included when ONBOARDING_ENABLED=true)
dashd/              Operator dashboards
proxyd/             Web proxy (auth gate, /dash/, /auth/, Vite)
teled/              Telegram adapter (Go)
discd/              Discord adapter (Go)
mastd/              Mastodon adapter (Go)
bskyd/              Bluesky adapter (Go)
reditd/             Reddit adapter (Go)
emaid/              Email adapter (Go, IMAP/SMTP)
whapd/              WhatsApp adapter (TypeScript)
```

## Data Directory

`/srv/data/arizuko_<name>/` per instance:

- `.env` — config
- `store/` — SQLite DB (`messages.db`)
- `services/` — enabled product TOMLs; `compose.Generate` reads all `*.toml` here
- `groups/<folder>/` — group files, logs, diary, media
- `groups/<world>/share/` — cross-group shared state
- `ipc/<folder>/` — MCP unix sockets + sidecar sockets
- `groups/<folder>/.claude/` — agent session state (settings, skills, CLAUDE.md)
- `web/` — vite web app
