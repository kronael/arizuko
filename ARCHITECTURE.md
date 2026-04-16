# Architecture

## Package Dependency Graph

```
cmd/arizuko/main
  ├── core          Config, types, Channel interface
  ├── store         SQLite persistence
  ├── api           HTTP API: channel registration, inbound messages
  │   └── chanreg, store
  ├── auth          identity, JWT, OAuth, middleware
  ├── chanreg       channel registry, HTTP channel proxy
  ├── gateway       main loop, message routing
  │   ├── container docker spawn, volume mounts, sidecars, runtime
  │   │   └── groupfolder, mountsec
  │   ├── queue     per-group concurrency, stdin piping
  │   ├── router    message formatting, routing rules
  │   ├── ipc       MCP server on unix socket
  │   └── diary, groupfolder
  └── compose       docker-compose generation

gated/   wires core + store + gateway + api + chanreg + ipc + auth
timed/   scheduler: polls scheduled_tasks, inserts messages
onbod/   onboarding state machine
teled/ discd/ mastd/ bskyd/ reditd/ emaid/ whapd/  channel adapters
grants/  CheckAction, NarrowRules, MatchingRules, DeriveRules
chanlib/ RouterClient, InboundMsg, auth middleware (shared by adapters)
db_utils/ SQL migration runner
```

## Message Flow

```
Channel adapter → POST /v1/messages (api) → store.PutMessage
  → gateway.messageLoop (polls every 2s)
  → store.NewMessages + store.ActiveWebJIDs
  → impulseGate.accept — weight-based batching
  → checkTrigger → handleCommand → prefix dispatch
  → router.ResolveRoutingTarget
  → queue.SendMessages (steer into running container) OR
  → queue.EnqueueMessageCheck → processGroupMessages
    → enricher: download attachments → Whisper for voice
    → store.EnrichMessage + MessagesSince + FlushSysMsgs
    → router.FormatMessages (XML batch) + grants.DeriveRules → start.json
    → container.Run → stream output → router.FormatOutbound
    → HTTPChannel.Send → POST /send
```

## Web Channel (proxyd)

Web chat uses `web:<folder>` JIDs. Messages arrive via `proxyd` (HTTP/SSE) and
store directly in SQLite. The gateway poll loop discovers active `web:` JIDs
via `store.ActiveWebJIDs(since)`. Each message carries a topic;
`processWebTopics` splits by topic and runs one agent per topic.

**Path model**:

- `/pub/*`, `/health` — public
- `/slink/*` — rate-limited (10 req/min/IP); token resolved against
  `groups.slink_token`; injects `X-Folder`, `X-Group-Name`, `X-Slink-Token`
- `/dash/*` — auth-gated, proxied to dashd
- `/dav/*` — auth-gated, proxied to dufs (strips `/dav` prefix)
- `/*` — auth-gated

**Auth** in `requireAuth`: `Authorization: Bearer <jwt>` → `refresh_token`
cookie → redirect to `/auth/login`. JWT claims include `groups` (null =
operator, `[]` = none, list = specific folders) read from `user_groups` at
login. `webd.requireFolder` checks `X-User-Groups` on folder-specific
endpoints.

WebDAV requires `DAV_ADDR`; the dufs container mounts `groups/` read-only.
proxyd reads `web/vhosts.json` every 5s. Full protocol: `specs/6/3-chat-ui.md`.

## Auth Hardening

- Secure cookies when `authBaseURL(cfg)` is HTTPS
- Login rate limit: 5 POST `/auth/login` per IP per 15min (in-memory)
- Telegram widget: rejects `auth_date` > 5min (replay protection)
- OAuth: Google, GitHub (optional `GITHUB_ALLOWED_ORG`), Discord; shared
  `createOAuthSession` path in `auth/oauth.go`

## Channel Protocol

Channels are external processes registering via HTTP. Both sides are HTTP
servers. Full protocol: `specs/4/1-channel-protocol.md`.

- **Inbound**: `POST /v1/messages` → store → route
- **Outbound**: router calls `POST <url>/send` (synchronous)
- **Registration**: `POST /v1/channels/register` with name, URL, JID prefixes,
  capabilities; returns session token
- **Health**: `GET /health` every 30s; 3 failures = auto-deregister; outbound
  queues in `HTTPChannel.outbox`
- **Auth**: `CHANNEL_SECRET` for registration; session token for
  channel→router; shared secret for router→channel
- **Typing**: `/typing` handlers route through `TypingRefresher`
  (`chanlib.TypingRefresher` / `whapd/src/typing.ts`) — re-sends presence on
  short interval with hard TTL

Packages: `chanreg/` (registry, health, `HTTPChannel`), `api/` (router-side
handlers), `chanlib/` (shared by Go adapters).

## Inbound Media Pipeline

1. Adapter populates `Attachments []InboundAttachment` in `/v1/messages`
2. `store.PutMessage` stores attachment JSON
3. Enricher fetches URLs, writes to `groups/<folder>/media/<YYYYMMDD>/`,
   calls `store.EnrichMessage` to prepend
   `<attachment path="..." mime="..." filename="..."/>` and clear column
4. Voice (`audio/*`) → Whisper if `VOICE_TRANSCRIPTION_ENABLED=true`
5. Container path: `/home/node/media/...`

teled serves `GET /files/{fileID}` as Telegram CDN proxy (bot-token URLs are
ephemeral). discd uses direct CDN URLs.

Config: `MEDIA_ENABLED=true`, `VOICE_TRANSCRIPTION_ENABLED=true`,
`WHISPER_BASE_URL`, `WHISPER_MODEL=turbo`.

## Key Types (core)

| Type          | Purpose                                           |
| ------------- | ------------------------------------------------- |
| `Config`      | settings from `.env` + env vars                   |
| `Message`     | incoming (sender, content, reply context)         |
| `Group`       | registered (folder, trigger, config)              |
| `GroupConfig` | per-group: mounts, timeout, sidecars              |
| `Route`       | routing table entry (type, match, target)         |
| `Task`        | scheduled (cron, prompt, status)                  |
| `Channel`     | Connect, Send, SendFile, Owns, Typing, Disconnect |

## SQLite Schema

| Table             | Key columns                                                                     |
| ----------------- | ------------------------------------------------------------------------------- |
| `chats`           | jid (PK), errored, agent_cursor, sticky_group, sticky_topic                     |
| `messages`        | id (PK), chat_jid, sender, content, timestamp, verb, source, attachments        |
| `groups`          | folder (PK), name, container_config, slink_token, parent, state, spawn_ttl_days |
| `routes`          | id, seq, match, target, impulse_config                                          |
| `sessions`        | group_folder + topic (PK), session_id                                           |
| `session_log`     | id, group_folder, session_id, started_at, ended_at, result, error               |
| `system_messages` | id, group_id, origin, event, body                                               |
| `scheduled_tasks` | id (PK), owner, chat_jid, prompt, cron, next_run, status                        |
| `router_state`    | key (PK), value                                                                 |
| `auth_users`      | sub (unique), username (unique), hash                                           |
| `auth_sessions`   | token_hash (PK), user_sub, expires_at                                           |
| `user_groups`     | user_sub + folder (PK) — restricts web user to folders                          |
| `email_threads`   | thread_id (PK), chat_jid, subject                                               |
| `onboarding`      | jid (PK), status, prompted_at                                                   |

WAL mode, 5s busy timeout, migrations via `PRAGMA user_version`.

`messages.source` is the canonical adapter-of-record stamped by
`api.handleMessage`; outbound reads `store.LatestSource(jid)`. All agent
output, delegation, and escalation flow through `PutMessage` — bot rows are
`is_from_me=1 is_bot_message=1` and filtered from inbound polling. `topic`
and `routed_to` capture audit metadata. Spec: `specs/7/22-audit-log.md`.

## Container Lifecycle

1. `runtime.EnsureRunning()` — verify docker
2. `runtime.CleanupOrphans()` — stop stale `arizuko-*`
3. `container.Run()`:
   - Resolve path via `groupfolder.Resolver`
   - `BuildMounts()` → `mountsec.ValidateAdditionalMounts()`
   - `seedSettings()` — write `settings.json` (env, arizuko MCP via socat,
     sidecars)
   - `seedSkills()` — copy `ant/skills/` on first run; seed `.claude.json`
   - `StartSidecars()`
   - Name: `arizuko-<folder>-<ts_ms>` or
     `arizuko-<folder>-task-<task_id>` (sender `scheduler-isolated:<id>`)
   - Env: `WEB_PREFIX` (`pub` for root, `pub/<folder>` for children),
     `ARIZUKO_IS_ROOT`, `ARIZUKO_DELEGATE_DEPTH`, `WEB_HOST`,
     `ARIZUKO_ASSISTANT_NAME`, plus group overrides
   - `docker run -i --rm`; parse output between
     `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---`
   - Output: `{ status, result, newSessionId, error }`
   - Timer timeout: graceful stop then kill
   - `StopSidecars()`
   - Log: `groups/<folder>/logs/container-<ts>.log`

Session: new session ID updates `store.sessions`. Error with no output →
evict session (cursor rolled back, retry). Error with output → cursor
advances (partial work preserved).

## IPC (ipc package)

MCP server on unix socket (`mark3labs/mcp-go`). One per group at
`ipc/<folder>/gated.sock`. Tools filtered by grants for the caller's group.
Runtime auth via `auth.Authorize`. `set_grants`/`get_grants` read/write
rules; `delegate_group` calls `NarrowRules` to merge parent+child.

socat bridges the host socket into the container; agent-runner configures
`arizuko` MCP server in `settings.json` with `socat UNIX-CONNECT`. Server
starts before `docker run`, stops after exit; socket cleaned up.

Identity resolved from socket path (folder, tier); authorization delegated
to `auth.Authorize`.

## Sidecars (container/sidecar.go)

Per-group MCP sidecars from `GroupConfig.Sidecars`:

1. `StartSidecars()` — `docker run -d` per sidecar with socket volume at
   `ipc/sidecars/<name>.sock`, resource limits, network mode
2. Socket wired into agent `settings.json` as `mcpServers` entry with
   `socat UNIX-CONNECT`
3. `StopSidecars()` — `docker stop` then `rm -f`

## Queue (queue package)

`GroupQueue` serializes agent invocations per group:

- `maxConcurrent` global (default 5), per-group active flag + container name
- Circuit breaker: 3 consecutive failures opens; reset by new message
- `EnqueueMessageCheck(jid)` — signal-only; queue calls back into
  `HasPendingMessages`/`processMessages`
- `drainGroupLocked` — on completion, starts next waiting group if capacity
- `SendMessages(jid, []string)` — steer batch into running container (write
  one IPC input file per message, signal once). Agent drains via
  `PostToolUse` hook for mid-loop injection, with `pollIpcDuringQuery` as
  next-turn fallback; `drainIpcInputMutex` prevents double-drain

Delegation, escalation, and `#topic` routing are not special — each writes
a row via `PutMessage` and calls `EnqueueMessageCheck`.

## Routing

Route table, route types, topic routing, reply routing, sticky routing,
full flow examples: `docs/routing.md`.

## Grants Engine (grants package)

Rule format: `[!]action[(param=glob,...)]`. Last-match-wins; no match = deny.

- `CheckAction(rules, action, params)` — allow/deny
- `NarrowRules(parent, child)` — child can only narrow
- `MatchingRules(rules, action)` — rules matching an action
- `DeriveRules(store, folder, tier, worldFolder)` — tier-0 `*`, tier-1
  platform send + management, tier-2 send only, deeper `send_reply` only

Rules derived at spawn and injected into `start.json`. The MCP manifest is
filtered by grants.

## Compose Containers

`compose.Generate(dataDir)` builds `docker-compose.yml` from:

1. **Built-in** — always emitted per `.env` profile: `gated`, `timed`,
   `dashd` (profile=full), `proxyd`+`vited` (WEB_PORT set),
   `onbod` (ONBOARDING_ENABLED=true)
2. **Extra** — `<dataDir>/services/*.toml` appended

Bundled catalog at `template/services/` (ships in image, Ansible extracts to
`/srv/app/arizuko/template/services/`): `teled.toml`, `whapd.toml`,
`discd.toml`, `bskyd.toml`, `mastd.toml`, `reditd.toml`.

TOML format:

```toml
image = "arizuko:latest"
entrypoint = ["teled"]
depends_on = ["gated"]
volumes = ["${DATA_DIR}/..."]

[environment]
ROUTER_URL = "http://gated:${API_PORT}"
CHANNEL_SECRET = "${CHANNEL_SECRET}"
```

Container naming: `<app>_<service>_<flavor>` (e.g. `arizuko_teled_REDACTED`).
Operator copies desired TOMLs into `/srv/data/arizuko_<flavor>/services/`
before start; Ansible via `arizuko_instances[].extra_services`.

onbod auto-included when `ONBOARDING_ENABLED=true`. Compose sets
`ONBOD_LISTEN_ADDR=:8092` (dashd uses `:8090`); standalone default `:8091`.

## Onboarding (onbod/)

Registers as a channel, seeds `/approve` and `/reject` routes on startup.

State machine per JID (`onboarding` table):

```
awaiting_message → (greeting + user message) → pending → approved | rejected
```

Poll loop (10s):

1. Prompt unanswered `awaiting_message`
2. Transition on first message → `pending`, notify tier-0 JIDs
3. Reply to pending users: "Still waiting for approval"

Approve: creates group dir, optionally copies prototype, inserts `groups`
row + default route, sends welcome. `notify` fans out to tier-0 root JIDs.

Prototype copy: `CLAUDE.md` and `SOUL.md` only (no session or memory). Agents
spawn children via `register_group` MCP with `fromPrototype=true`. Spec:
`specs/4/26-prototypes.md`.

## Scheduler (timed/)

Spec: `specs/4/8-scheduler-service.md`. Polls `scheduled_tasks` every 60s.
For each due task (status=active, next_run ≤ now):

1. Insert prompt as message (sender: `scheduler`)
2. Compute next run (robfig/cron), update `next_run`
3. Tasks without cron get `next_run=NULL` (one-shot)

Gateway picks up scheduler messages via normal poll. timed opens the same
SQLite DB (WAL); own migration runner keyed by `"timed"` in shared
`migrations` table. `timed/migrations/0001-schema.sql` creates
`scheduled_tasks` idempotently.

## Operator Dashboard (dashd/)

Standalone read-only HTMX portal on `DASH_PORT` (default 8090). Opens SQLite
read-only. Six views: portal, status, tasks, activity, groups, memory. Auth
via JWT cookie (imports `auth`). Spec: `specs/7/25-dashboards.md`.

URLs: `/dash/` portal, `/dash/<name>/` page, `/dash/<name>/x/<frag>` HTMX
partial, `/dash/<name>/api/<path>` JSON.

Registered as `receive_only: true`. `/status` command routes to dashd; dashd
replies via `notify/`.

## Diary (diary package)

`diary.Read(groupDir, max)` reads recent `.md` from `group/diary/`, extracts
`summary:` frontmatter, returns XML with age labels (today, yesterday, N
days/weeks ago). Prepended to agent prompt as `<knowledge layer="diary">`.

Two-layer memory: `MEMORY.md` for long-term, daily files for work log. Nudged
by `/diary` skill, PreCompact hook, every 100 turns. Spec:
`specs/1/L-memory-diary.md`.

Episodes (compressed transcripts) follow the same `summary:` format, indexed
by `/recall`. Compression: daily → weekly → monthly. Spec:
`specs/4/24-recall.md`.

## Error Handling

- Per-chat `errored` flag in `chats`
- Agent error, no output: cursor rolled back, flag set, retry
- Agent error with output: cursor advances, partial preserved
- Queue circuit breaker: 3 failures → stop until new message
- Container timeout: graceful `docker stop` → `Process.Kill`

## Mount Security (mountsec)

`ValidateAdditionalMounts` validates group-configured mounts against a
caller-supplied `Allowlist`. `ValidateFilePath` guards inbound paths (MCP
tool arguments) against symlink escapes and a blocklist (`.ssh`, `.gnupg`,
`.env`, credentials, private keys). Container path: `/workspace/extra/<name>`.

## Docker-in-Docker Paths

`container.hp()` translates local to host paths when gateway runs in docker.
`HOST_DATA_DIR` env provides the host-side base.
