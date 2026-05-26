# Architecture

## Core vs integrations

arizuko is built as a **core** of always-present primitives plus
**integrations** that plug into the core per deployment. The split is
visible throughout this document and in the [README](README.md) tables.
New integrations are added via the extension points described in
[EXTENDING.md](EXTENDING.md); the core evolves as a unit.

- **Core daemons / libraries** define the system shape: gateway, store,
  ipc, auth, grants, proxyd, webd, dashd, timed, onbod, vited, davd, the
  container runner, chanlib/chanreg, plus the `gated` daemon that wires
  them. The package graph below is core.
- **Integrations** are pluggable: per-platform channel adapters
  (`teled`, `whapd`, `mastd`, `discd`, `slakd`, `bskyd`, `reditd`, `emaid`,
  `twitd`, `linkd`) talking to core over the channel protocol; optional
  capability hooks (Whisper transcription via `WHISPER_BASE_URL`, TTS
  via `ttsd` + `TTS_BASE_URL` (`specs/5/T-voice-synthesis.md`), oracle
  skill in `ant/skills/oracle/` driving the `codex` CLI
  (`specs/5/H-call-llm-mcp.md`), crackbox egress isolation
  (`CRACKBOX_ADMIN_API set`), sandbox backend choice (Docker today, KVM
  via `crackbox/pkg/host/`)).

A minimal deployment runs core plus one channel adapter; a maxed-out
deployment runs all of them.

## Package Dependency Graph (core)

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
dashd/    operator dashboard (HTMX read views + TIER 1 routes/groups/secrets CRUD)
webd/     web chat channel adapter (HTTP/SSE, registers as "web")
vited/    Vite dev server / static origin behind proxyd
proxyd/   reverse proxy: auth, vhost routing, slink rate limiting
grants/   CheckAction, MatchingRules, DeriveRules
chanlib/  RouterClient, InboundMsg, auth middleware, URLCache (CDN-id
          proxy cache for discd/mastd/reditd), fsutil (CopyDirNoSymlinks,
          CopyFile), env helpers (EnvOr/EnvInt/EnvDur), ShortHash — shared
          by adapters + gateway + container + onbod + webd
theme/    shared CSS + HTML helpers (used by onbod, dashd)
db_utils/ SQL migration runner
```

## Integrations

```
teled/ discd/ slakd/ mastd/ bskyd/ reditd/ emaid/ whapd/ twitd/ linkd/
        channel adapters — separate processes, register with core via
        the HTTP channel protocol (see "Channel Protocol" below)

sidecar/ whisper-cpp container; gateway calls Whisper for inbound
        voice when VOICE_TRANSCRIPTION_ENABLED=true
crackbox/ optional egress-isolation proxy + KVM sandbox library; pulled
        in when CRACKBOX_ADMIN_API set (see "Compose Containers" below).
        Shippable separately; specs/5/A-orthogonal-components.md
```

TTS (`ttsd/`, `specs/5/T-voice-synthesis.md`) and the oracle skill
(`ant/skills/oracle/`, `specs/5/H-call-llm-mcp.md`) ship as
optional integrations rather than core daemons; both opt-in via
env vars / folder secrets.

Some integrations have no daemon and no MCP surface at all —
**host-tool capabilities** are CLIs installed in the agent image
(or mounted from the host) that the agent runs as ordinary
subprocesses, with a SKILL.md as the discovery surface. `oracle`
is the canonical example. See `EXTENDING.md` "Host-tool
capabilities" for the pattern + the current list.

## Message Flow

```
Channel adapter → POST /v1/messages (api) → store.PutMessage
  → gateway.messageLoop → pollOnce (polls every PollInterval)
  → store.NewMessages (since lastTimestamp)
  → resolveGroup (route table lookup)
  → handleCommand (prefix dispatch)
  → route lookup: trigger fires a turn; #observe stores only
  → resolveOrEngaged: on route miss or #observe, fires turn if chat_reply_state.engaged_until active
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

**Path model** (overview; full prefix table + DB-backed `web_routes`
fallthrough in `ROUTING.md` "HTTP Routing (proxyd)"):

- `/pub/*`, `/health`, `/invite/*`, `/p/*` — public
- `/slink/*` — 301-redirects to `/chat/<token>/` (since v0.41.0;
  `groups.slink_token` column dropped in migration 0059; header is
  `X-Chat-Token`)
- `/chat/<token>/` (GET widget + POST) and `/hook/<token>` (POST only)
  — shipped v0.41.0 ([specs/5/W-webhook-routes.md](specs/5/W-webhook-routes.md));
  unified `route_tokens` table, supersedes `groups.slink_token`.
- `/dash/*` — auth-gated, proxied to dashd
- `/dav/*` — auth-gated, proxied to dufs via TOML route (bespoke
  group-scoping + `davAllow` write-block; strips `/dav` prefix)
- `/*` — DB-backed `web_routes` longest-prefix, else auth-gated to vited

**Auth** in `requireAuth`: `Authorization: Bearer <jwt>` → `refresh_token`
cookie → redirect to `/auth/login`. JWT claims include `groups` —
a JSON array of allow-scopes computed by `store.UserScopes(sub)` from
the `acl` table (transitively expanded via `acl_membership`). Operator
membership surfaces as `**` in this list; see "Operator" below.
`webd.requireFolder` checks `X-User-Groups` on folder-specific endpoints.

WebDAV requires `DAV_ADDR`; the dufs container mounts `groups/` read-only.
proxyd reads `web/vhosts.json` every 5s. Full protocol: `specs/4/18-web-vhosts.md`.

### Route generation pipeline

Routes are declared in two source-of-truth places and merged into the
serialized table proxyd loads at startup:

```
template/services/<adapter>.toml [[proxyd_route]] ─┐
                                                   ├─► compose.collectProxydRoutes
compose.coreProxydRoutes (dashd/webd/davd/onbod) ──┘             │
                                                                  ▼
                                              PROXYD_ROUTES_JSON env on proxyd
                                                                  │
                                                                  ▼
                                       proxyd.loadConfig parses → []Route
                                                                  │
                                                                  ▼
                                  dispatchRoute longest-prefix match
                                                                  │
                                                                  ▼
                                    DB-backed `web_routes` fallthrough
                                                                  │
                                                                  ▼
                                          default: auth-gate to vited
```

Hand-wired in `proxyd/main.go` outside this table: `/auth/*`, `/health`,
`/pub/*` (with optional `PUB_REDIRECT_URL`), `/slink/*` (301→`/chat/<token>/`),
`/chat/*` and `/hook/*` (route-token bearer), `/dav/*` (`davAllow` + group-scope). All other routes flow
through the TOML/core declaration → JSON env → dispatcher path. Adding
an adapter means dropping a TOML with a `[[proxyd_route]]` block; no
proxyd or compose.go edits.

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
| `Group`       | registered (folder, name, config, parent)         |
| `GroupConfig` | per-group: mounts, timeout                        |
| `Route`       | routing table entry (seq, match, target)          |
| `Task`        | scheduled (cron, prompt, status)                  |
| `Channel`     | Connect, Send, SendFile, Owns, Typing, Disconnect |

## SQLite Schema

| Table              | Key columns                                                                                                                      |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| `chats`            | jid (PK), agent_cursor, sticky_group, sticky_topic, is_group                                                                     |
| `messages`         | id (PK), chat_jid, sender, content, timestamp, verb, source, attachments, topic, errored, is_observed                            |
| `groups`           | folder (PK), name, container_config, parent, model (`slink_token` column dropped in migration 0059)                              |
| `route_tokens`     | token_hash (PK), jid, owner_folder, created_at — shipped v0.41.0 ([specs/5/W-webhook-routes.md](specs/5/W-webhook-routes.md))    |
| `routes`           | id (PK), seq, match, target (`<folder>[#<mode>]`), observe_window_messages, observe_window_chars                                 |
| `sessions`         | group_folder + topic (PK), session_id, parent_topic, forked_at, observed_cursor (spec 6/F)                                       |
| `session_log`      | id, group_folder, session_id, started_at, ended_at, result, error                                                                |
| `system_messages`  | id, group_id, origin, event, body                                                                                                |
| `scheduled_tasks`  | id (PK), owner, chat_jid, prompt, cron, next_run, status, context_mode                                                           |
| `task_run_logs`    | id (PK), task_id, run_at, duration_ms, status, error                                                                             |
| `router_state`     | key (PK), value                                                                                                                  |
| `channels`         | name (PK), url, jid_prefixes, capabilities                                                                                       |
| `auth_users`       | sub (unique), username (unique), hash, name                                                                                      |
| `auth_sessions`    | token_hash (PK), user_sub, expires_at                                                                                            |
| `acl`              | principal + action + scope + params + predicate + effect (PK), granted_by, granted_at                                            |
| `acl_membership`   | child + parent (PK), added_by, added_at — role memberships + JID→sub claims                                                      |
| `chat_reply_state` | jid + topic (PK), last_reply_id, engaged_until, engaged_folder                                                                   |
| `email_threads`    | thread_id (PK), chat_jid, subject                                                                                                |
| `onboarding`       | jid (PK), status, prompted_at, token, token_expires, user_sub, gate, queued_at                                                   |
| `onboarding_gates` | gate (PK), limit_per_day, enabled                                                                                                |
| `invites`          | token (PK), target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count                                              |
| `secrets`          | scope_kind + scope_id + key (PK), value (AES-256-GCM encrypted, v1: prefix, key=SHA-256(SECRETS_KEY or AUTH_SECRET)), created_at |
| `identities`       | id (PK), name, created_at — canonical cross-channel user (advisory, spec 5/9)                                                    |
| `identity_claims`  | sub (PK), identity_id, claimed_at — sender-sub → identity merge                                                                  |
| `turn_results`     | folder + turn_id (PK), session_id, status, recorded_at — per-turn submit_turn outcomes                                           |
| `network_rules`    | folder + target (PK), created_at, created_by — crackbox egress allowlist                                                         |
| `web_routes`       | path_prefix (PK), access (public/auth/deny/redirect), redirect_to, folder, created_at                                            |

WAL mode, 5s busy timeout, migrations via `db_utils.Migrate` (`migrations`
table keyed by service+version).

`messages.source` is the canonical adapter-of-record stamped by
`api.handleMessage`; outbound reads `store.LatestSource(jid)`. All agent
output, delegation, and escalation flow through `PutMessage` — bot rows are
`is_from_me=1 is_bot_message=1` and filtered from inbound polling. `topic`
and `routed_to` capture audit metadata. Spec: `specs/3/c-audit-log.md`.

## Container Lifecycle

One container per group, long-lived across turns. The group folder
mounts at `/home/node/` so conversation state, diary, skills, and the
Claude Code session jsonl persist between invocations. Sibling groups
get their own containers on their own networks with their own DB
views — the cross-tenant boundary is the container, not the turn.
Threat model + isolated axes in `SECURITY.md` § Model.

**Why one container per group matters for context.** Each container is
one agent with one context window. Sales, SRE, and recruiting all run
separate containers — they don't share a context. Mixing unrelated
concerns in a single context causes the model to blend them: wrong
answers, hallucinated references, noise from irrelevant history. The
container boundary is the context boundary: each group gets a clean,
persistent, focused context that accumulates only the conversations
relevant to it.

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
   - `docker run -i --rm`; spawn + wait. Per-turn results arrive
     over MCP (`submit_turn`), not stdout.
   - Output: `{ status, error }` from runner; `newSessionId` and
     `result` flow back to gateway via `submit_turn` callbacks.
   - Timer timeout: graceful stop then kill
   - Log: `groups/<folder>/logs/container-<ts>.log`

Session: new session ID updates `store.sessions`. Error with no output →
evict session (cursor rolled back, retry). Error with output → cursor
advances (partial work preserved).

## IPC (ipc package)

MCP server on unix socket (`mark3labs/mcp-go`). One per group at
`ipc/<folder>/gated.sock`. Tools filtered by grants for the caller's group.
Runtime auth via `auth.Authorize`. `list_acl(folder)` inspects the
effective ACL rows for the caller's principal set.

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
full flow examples: `ROUTING.md`. The autoviv pattern (tier-1 agent calls
`register_group` to auto-create per-channel children when unrouted JIDs
arrive) is `template/web/pub/concepts/autoviv.html` — no new mechanism,
just routes + MCP + operator grant composing.

## Grants Engine (grants package)

Rule format: `[!]action[(param=glob,...)]`. Last-match-wins; no match = deny.

- `CheckAction(rules, action, params)` — allow/deny
- `MatchingRules(rules, action)` — rules matching an action
- `DeriveRules(store, folder, tier, worldFolder)` — tier-0 `*`, tier-1
  platform send + management, tier-2 send only, deeper `send_reply` only

Rules derived at spawn and injected into `start.json`. The MCP manifest is
filtered by grants.

## Operator

"Operator" is **not a role flag or sentinel** — it is membership in the
`role:operator` principal in the unified ACL. Any sub joined via
`acl_membership(<sub>, role:operator)` inherits the seeded row
`acl(role:operator, *, **, allow)` and matches every authorization check.
`auth.Authorize` handles tier-0 visibility uniformly; there is no
`groups.is_operator` column, no `router_state['operator_jid']` sentinel,
and no nil-default routing target.

Cross-group system notifications (errors, health events, scheduled digests)
resolve their destination by querying `acl_membership` for `role:operator`
members and routing messages into their existing folders — not by seeding
a specially-flagged group.

What this means in practice: adding a user to `role:operator` is how you
make them the operator. Removing the membership demotes them. dashd is the
management UI; its sessions are scoped to users with at least one allow
row in `acl` (directly or via membership).

Open design questions for proactive-operator delivery (not yet shipped):

- Error / health-check trigger plumbing (`InsertSysMsg` from `gated`).
- Dedup / rate-limit policy for error bursts.
- Listener-digest delivery format.

These are mechanism questions; the addressing question (membership, not
flag) is settled.

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
`discd.toml`, `slakd.toml`, `bskyd.toml`, `mastd.toml`, `reditd.toml`, `linkd.toml`.

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

onbod auto-included when `ONBOARDING_ENABLED=true`. All Go daemons
listen on :8080 internally except ttsd at :8880 — historical default
that predates the invariant.

`crackbox` (sibling component, see `specs/5/A-orthogonal-components.md`)
and an `agents` internal network are emitted when `CRACKBOX_ADMIN_API` is set.
The internal network has no default route to the internet; crackbox is
the only container with both NICs (internal + default bridge). gated
spawns agent containers on the `agents` network and registers their
per-spawn IP with crackbox via the admin API. crackbox enforces the
per-folder allowlist by host name on every CONNECT/HTTP request. See
`crackbox/README.md` and `SECURITY.md` (Network egress isolation).

## Onboarding (onbod/)

Self-service token-based onboarding with optional gated admission. Turns
inbound JIDs into provisioned groups via OAuth + `container.SetupGroup`.
See `onbod/README.md`.

## Scheduler (timed/)

Standalone daemon that turns `scheduled_tasks` rows into messages on the
shared DB; gateway picks them up via normal poll. See `timed/README.md`.

## Operator Dashboard (dashd/)

HTMX portal over the shared SQLite plus TIER 1 write surface (routes
editor, groups CRUD, per-user secrets) gated by admin auth. Spec:
`specs/3/d-dashboards.md`. See `dashd/README.md`.

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

## Observability

Three substrates, clearly split:

- **`audit_log` SQLite table** — source of truth for state-changing
  operations. ACID, transactional with the mutation. Forensic queries
  via `sqlite3`. Spec [`5/I`](specs/5/I-tool-call-logging.md) +
  [`6/F`](specs/6/F-audit-stream.md).
- **slog → journald** — operational telemetry for everything (state
  changes + reads). High-rate, lossy by design (journald rotation,
  level filtering). `journalctl -u arizuko_<inst>` is the default
  operator view.
- **OTLP export (opt-in)** — slog stream tee'd to any OTel-compatible
  collector when `OTEL_EXPORTER_OTLP_ENDPOINT` is set in the instance
  `.env`. Logs not spans; `turn_id` → deterministic TraceID across
  daemons. Library: [`obs/`](obs/). Spec:
  [`5/O`](specs/5/O-otlp-export.md). Off by default.

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

## Long-running tasks — the 900s container timeout

The Claude Code SDK inside the agent container hard-aborts any single
turn at **900 seconds (15 min) of wall time**. This is a property of
the upstream SDK, not arizuko — there is no platform-side knob to
raise it. Observed 4+ times in May 2026 (atlas 2026-05-07/19/25,
krons 2026-05-25, atlas-support 2026-05-19).

In logs:

```
Query timeout (900000ms) reached, aborting
Container exited with code 1
```

User-visible impact: the in-flight reply is dropped; any inbound
messages that arrived during the timing-out turn get tagged
`errored=1` in `messages`. The agent typically recovers on the next
inbound (cursor advanced, fresh container), but the errored rows
stay flagged in the DB and surface as failed turns in the UI.

Mitigations available to skill authors and agents (no platform
change required):

- **Split into multiple turns** — emit partial results, ask the user
  for "continue" rather than packing a 15-minute job into one turn.
- **Checkpoint progress** — write intermediate state to `~/facts/`
  or `~/tmp/` between subtasks so a fresh container can resume
  instead of restarting from zero.
- **Fewer parallel subagents** — every Task subagent shares the
  parent turn's wall budget; 8 parallel critics is 8× the chance of
  one stalling out the whole turn.
- **Background long jobs** — pass `run_in_background: true` to the
  Task tool for genuinely long work; the parent turn returns
  immediately and the agent can poll/resume later.
- **Self-terminate agentic loops** — count actual iterations and
  stop well before the cap; a loop that ran 28 of an intended 30
  iterations and saved partial output is better than one that hit
  900s mid-iteration 30 and dropped everything.

## Prompt Assembly

Every inbound turn the gateway emits an envelope of small XML-shaped
blocks, prepended (or attached) to the agent's prompt. They share
three properties: **XML-shaped**, **never persisted to `messages`**,
**per-turn scope only** (recomputed next turn). The blocks emitted
today:

- `<autocalls>` — zero-arg facts (`now`, `instance`, `folder`,
  `tier`, `session`); `gateway/autocalls.go`
- `<persona name=…>` — `PERSONA.md` frontmatter `summary:` re-anchor;
  `gateway/persona.go`
- `<previous_session/>` — last session id/timing on a fresh session;
  `gateway/gateway.go`
- `<knowledge layer=…>` — recent diary entries with age labels;
  `diary/diary.go`
- `<messages>` + `<reply-to>` + `<message>` — inbound batch;
  `router/router.go`
- `<attachment …/>` — inbound media path + optional `transcript=`;
  `gateway/gateway.go`
- `<observed>` — trailing window of `is_observed=1` messages stored
  under this folder via `#observe` routes; `gateway/gateway.go`
- `<topic name=…/>` — scope envelope on every turn (empty for main).
  Parent topic context arrives via the forked Claude Code session,
  not via injection (spec 6/F rev6); `gateway/gateway.go`
- `<surface>slack-pane</surface>` + `<pane-context jid=…/>` —
  emitted when the trigger arrives via an open Slack assistant pane
  (spec 6/D); `gateway/gateway.go`

Full table with line cites and the convention for adding a block
lives in `gateway/README.md` ("Per-turn ephemeral XML blocks"). See
`EXTENDING.md` for the autocall extension point in particular.

## MCP Surface

Action tools mutate state: messaging (`send`, `reply`, `send_file`,
`forward`, `edit`), feed (`post`, `quote`, `repost`, `like`,
`dislike`, `delete`), control (`schedule_task`, `register_group`,
`set_routes`, …). Adapters lacking a native primitive return a
typed `chanlib.UnsupportedError` whose hint redirects to a
concrete alternative (e.g. `dislike` on emoji platforms hints
`like(emoji='👎')`). Read-only introspection lives in the
`inspect_*` family (`ipc/inspect.go`): `inspect_messages`,
`inspect_routing`, `inspect_tasks`, `inspect_session`. Tier 0
sees all instances; tier ≥1 is scoped to its own folder subtree.
Full tool table in `ant/skills/self/SKILL.md`.

## Mount Security (mountsec)

`ValidateAdditionalMounts` validates group-configured mounts against a
caller-supplied `Allowlist`. `ValidateFilePath` guards inbound paths (MCP
tool arguments) against symlink escapes and a blocklist (`.ssh`, `.gnupg`,
`.env`, credentials, private keys). Container path: `/workspace/extra/<name>`.

## Docker-in-Docker Paths

`container.hp()` translates local to host paths when gateway runs in docker.
`HOST_DATA_DIR` and `HOST_APP_DIR` provide the host-side base paths.
