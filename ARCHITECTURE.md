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
  │   ├── container docker spawn, volume mounts, runtime
  │   │   └── groupfolder, mountsec
  │   ├── queue     per-group concurrency, stdin piping
  │   ├── router    message formatting, routing rules
  │   ├── ipc       MCP server on unix socket
  │   └── diary, groupfolder
  └── compose       docker-compose generation

gated/    wires core + store + gateway + api + chanreg + ipc + auth
timed/    scheduler: polls scheduled_tasks, inserts messages
onbod/    onboarding state machine + gated admission queue
dashd/    operator dashboard (HTMX, read-only SQLite)
webd/     web chat channel adapter (HTTP/SSE, registers as "web")
proxyd/   reverse proxy: auth, vhost routing, slink rate limiting
teled/ discd/ mastd/ bskyd/ reditd/ emaid/ whapd/ linkd/  channel adapters
grants/   CheckAction, NarrowRules, MatchingRules, DeriveRules
chanlib/  RouterClient, InboundMsg, auth middleware, URLCache (CDN-id
          proxy cache for discd/mastd/reditd), fsutil (CopyDirNoSymlinks,
          CopyFile), env helpers (EnvOr/EnvInt/EnvDur), ShortHash — shared
          by adapters + gateway + container + onbod + webd
theme/    shared CSS + HTML helpers (used by onbod, dashd)
db_utils/ SQL migration runner
```

## Message Flow

```
Channel adapter → POST /v1/messages (api) → store.PutMessage
  → gateway.messageLoop → pollOnce (polls every PollInterval)
  → store.NewMessages (since lastTimestamp)
  → resolveGroup (route table lookup)
  → handleCommand (prefix dispatch)
  → impulseGate.accept — weight-based batching (if enabled)
  → queue.SendMessages (steer into running container) OR
  → queue.EnqueueMessageCheck → processGroupMessages
    → enrichAttachments: download media → Whisper for voice
    → store.EnrichMessage + MessagesSince + FlushSysMsgs
    → gateway.renderAutocalls (<autocalls> block, prompt top)
    → router.FormatMessages (XML batch, errored rows tagged
      errored="true") + grants.DeriveRules → start.json
    → container.Run → stream output → router.FormatOutbound
    → HTTPChannel.Send → POST /send
```

## Web Channel (proxyd)

Web chat uses `web:<folder>` JIDs. `webd` is a channel adapter that registers
with the router as `"web"` (prefix `web:`), receives messages via HTTP/SSE,
and stores them through the standard `/v1/messages` API. When
`processGroupMessages` encounters a `web:` JID it delegates to
`processWebTopics`, which splits by topic and runs one agent per topic.

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
- OAuth: Google (optional `GOOGLE_ALLOWED_EMAILS`), GitHub (optional
  `GITHUB_ALLOWED_ORG`), Discord, Telegram widget; shared
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
| `Group`       | registered (folder, name, config, state)          |
| `GroupConfig` | per-group: mounts, timeout                        |
| `Route`       | routing table entry (seq, match, target)          |
| `Task`        | scheduled (cron, prompt, status)                  |
| `Channel`     | Connect, Send, SendFile, Owns, Typing, Disconnect |

## SQLite Schema

| Table              | Key columns                                                                              |
| ------------------ | ---------------------------------------------------------------------------------------- |
| `chats`            | jid (PK), agent_cursor, sticky_group, sticky_topic                                       |
| `messages`         | id (PK), chat_jid, sender, content, timestamp, verb, source, attachments, topic, errored |
| `groups`           | folder (PK), name, container_config, slink_token, parent, state, spawn_ttl_days          |
| `routes`           | id (PK), seq, match, target, impulse_config                                              |
| `sessions`         | group_folder + topic (PK), session_id                                                    |
| `session_log`      | id, group_folder, session_id, started_at, ended_at, result, error                        |
| `system_messages`  | id, group_id, origin, event, body                                                        |
| `scheduled_tasks`  | id (PK), owner, chat_jid, prompt, cron, next_run, status, context_mode                   |
| `task_run_logs`    | id (PK), task_id, run_at, duration_ms, status, error                                     |
| `router_state`     | key (PK), value                                                                          |
| `channels`         | name (PK), url, jid_prefixes, capabilities                                               |
| `auth_users`       | sub (unique), username (unique), hash, name                                              |
| `auth_sessions`    | token_hash (PK), user_sub, expires_at                                                    |
| `user_groups`      | user_sub + folder (PK), granted_at                                                       |
| `user_jids`        | user_sub + jid (PK), jid (unique), claimed                                               |
| `grant_rules`      | folder (PK), rules (JSON)                                                                |
| `grants`           | id, jid, role, granted_by, granted_at                                                    |
| `chat_reply_state` | jid + topic (PK), last_reply_id                                                          |
| `email_threads`    | thread_id (PK), chat_jid, subject                                                        |
| `onboarding`       | jid (PK), status, prompted_at, token, token_expires, user_sub, gate, queued_at           |
| `onboarding_gates` | gate (PK), limit_per_day, enabled                                                        |
| `invitations`      | token (PK), folder, created_by, created_at, uses, max_uses, expires                      |

WAL mode, 5s busy timeout, migrations via `db_utils.Migrate` (`migrations`
table keyed by service+version).

`messages.source` is the canonical adapter-of-record stamped by
`api.handleMessage`; outbound reads `store.LatestSource(jid)`. All agent
output, delegation, and escalation flow through `PutMessage` — bot rows are
`is_from_me=1 is_bot_message=1` and filtered from inbound polling. `topic`
and `routed_to` capture audit metadata. Spec: `specs/7/22-audit-log.md`.

## Container Lifecycle

1. `container.EnsureRunning()` — verify docker
2. `container.CleanupOrphans()` — stop stale `arizuko-*`
3. `container.Run()`:
   - Resolve path via `groupfolder.Resolver`
   - `buildMounts()` → `mountsec.ValidateAdditionalMounts()`
   - `seedSettings()` — write `settings.json` (env, arizuko MCP via socat)
   - `seedSkills()` — copy `ant/skills/` (re-seeds every run); seed `.claude.json`
   - Name: `arizuko-<instance>-<folder>-<ts_ms>` or overridden by caller
     (e.g. task containers set `timed-isolated:<id>` sender)
   - Env: `WEB_PREFIX` (`pub` for root, `pub/<folder>` for children),
     `ARIZUKO_IS_ROOT`, `ARIZUKO_DELEGATE_DEPTH`, `WEB_HOST`,
     `ARIZUKO_ASSISTANT_NAME`, plus group overrides
   - `docker run -i --rm`; parse output between
     `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---`
   - Output: `{ status, result, newSessionId, error }`
   - Timer timeout: graceful stop then kill
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
full flow examples: `ROUTING.md`.

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

1. **Built-in** — emitted per `.env` profile and feature flags:
   `gated` (always), `timed` (profile != minimal/web),
   `webd`+`proxyd`+`vited` (WEB_PORT set, profile != minimal),
   `dashd` (profile=full), `davd` (profile=full, WEBDAV_ENABLED=true),
   `onbod` (profile=full, ONBOARDING_ENABLED=true)
2. **Extra** — `<dataDir>/services/*.toml` appended

Bundled catalog at `template/services/` (ships in image, Ansible extracts to
`/srv/app/arizuko/template/services/`): `teled.toml`, `whapd.toml`,
`discd.toml`, `bskyd.toml`, `mastd.toml`, `reditd.toml`, `linkd.toml`.

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

onbod auto-included when `ONBOARDING_ENABLED=true`. All daemons
listen on :8080 inside containers.

## Onboarding (onbod/)

Self-service token-based onboarding with optional gated admission.

State machine per JID (`onboarding` table):

```
awaiting_message → token_used (clicked link) → [queued] → approved (OAuth + world created)
```

Poll loop (10s): picks up `awaiting_message` rows with no `prompted_at`,
generates one-time token (24h TTL), sends auth link via gated outbound API.

When `ONBOARDING_GATES` is set (comma-separated gate specs like
`github:org=mycompany:10/day`, `google:domain=example.com:20/day`, `*:50/day`),
users who click the link enter `queued` status with a matched gate.
`admitFromQueue` runs every ~60s and promotes queued users up to each gate's
daily limit. Queue position page auto-refreshes (30s). Without gates, users
go directly to `approved`.

Web dashboard at `/onboard`: token landing → OAuth → username picker →
world creation via `container.SetupGroup`. Second-JID auto-link when
user already has a world.

Prototype copy: `CLAUDE.md` and `SOUL.md` only (no session or memory). Agents
spawn children via `register_group` MCP with `fromPrototype=true`. Spec:
`specs/4/26-prototypes.md`.

## Scheduler (timed/)

Spec: `specs/4/8-scheduler-service.md`. Polls `scheduled_tasks` every 60s.
For each due task (status=active, next_run <= now):

1. Atomically claim due tasks (`status='firing'`) to prevent double-fire
2. Insert prompt as message (sender: `timed` or `timed-isolated:<id>`)
3. Compute next run (robfig/cron or interval-ms), update `next_run`
4. Tasks without cron get `status='completed'` (one-shot)
5. Log each run to `task_run_logs`

Daily `cleanupSpawns`: closes idle child groups past `spawn_ttl_days`,
archives closed groups past `archive_closed_days` as `.tar.gz`.

Gateway picks up scheduler messages via normal poll. timed opens the same
SQLite DB (WAL mode); schema is owned by `store/migrations/` and must
already be migrated by gated before timed starts.

## Operator Dashboard (dashd/)

Standalone read-only HTMX portal on `:8080` (configurable via `DASH_PORT`
env; exposed on host only if `DASH_PORT` is set in compose, otherwise
accessed via proxyd at `/dash/`). Opens SQLite read-only. Six views: portal,
status, tasks, activity, groups, memory. Auth enforced by proxyd's
`requireAuth` middleware. Spec: `specs/7/25-dashboards.md`.

URLs: `/dash/` portal, `/dash/<name>/` page, `/dash/<name>/x/<frag>` HTMX
partial.

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

Per-message `errored` flag (`messages.errored`, migration 0030). No
per-chat quarantine.

- Agent error, no output: `store.MarkMessagesErrored(ids)` tags the
  failing batch; cursor stays so the batch reappears next poll. The
  prompt carries `errored="true"` on those rows so the agent sees
  it failed the last attempt and must try differently.
- Agent error with output: same tag + cursor advances (partial work
  preserved).
- Queue circuit breaker: 3 consecutive failures →
  `gateway.onCircuitBreakerOpen` calls `store.DeleteErroredMessages`
  and resets the session. No permanent quarantine — the next inbound
  message gets a clean run.
- Container timeout: graceful `docker stop` → `Process.Kill`.

## Prompt Assembly

Every prompt begins with an `<autocalls>` block
(`gateway/autocalls.go`) — zero-arg, one-line facts resolved at
build time. Ships `now`, `instance`, `folder`, `tier`, `session`.
Cheaper than an MCP tool when the schema cost exceeds the data
returned; agent sees the value, pays no per-turn call. Empty eval
output skips the line. See `EXTENDING.md` for adding one.

## MCP Surface

Action tools (`send_*`, `schedule_task`, `register_group`,
`set_routes`, …) mutate state. Social actions `post`, `like`,
`delete_post` (`ipc/ipc.go`) are tier 0-2; platform adapters that
implement `Post`/`Like`/`DeletePost` (discd, mastd, bskyd; reditd
for `delete_post`) service them, others return "not configured".
Read-only introspection lives in the `inspect_*` family
(`ipc/inspect.go`): `inspect_messages`, `inspect_routing`,
`inspect_tasks`, `inspect_session`. Tier 0 sees all instances;
tier ≥1 is scoped to its own folder subtree. Full tool table in
`ant/skills/self/SKILL.md`.

## Mount Security (mountsec)

`ValidateAdditionalMounts` validates group-configured mounts against a
caller-supplied `Allowlist`. `ValidateFilePath` guards inbound paths (MCP
tool arguments) against symlink escapes and a blocklist (`.ssh`, `.gnupg`,
`.env`, credentials, private keys). Container path: `/workspace/extra/<name>`.

## Docker-in-Docker Paths

`container.hp()` translates local to host paths when gateway runs in docker.
`HOST_DATA_DIR` and `HOST_APP_DIR` provide the host-side base paths.
