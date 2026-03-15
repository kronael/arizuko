# Changelog

All notable changes to arizuko are documented here.

arizuko is a fork of [nanoclaw](https://github.com/nicholasgasior/nanoclaw)
(upstream at v1.1.3).

---

## [Unreleased]

## [v1.1.0] — 2026-03-15

Microservice architecture. Scheduler extracted to standalone daemon,
schema simplified, dead code removed, specs aligned with code.

### Architecture

- **services/timed/**: standalone scheduler daemon (~150 LOC), polls
  scheduled_tasks, inserts into messages. Zero dependencies on gateway.
  Own migration runner (service name: `timed`).
- **Daemon specs**: gated (9), timed (8), actid (10), authd (11) —
  one spec per daemon with clear table ownership.
- **0-architecture.md**: lean service overview replacing 579-line monolith.

### Breaking: scheduled_tasks schema

- `group_folder` → `owner`
- `schedule_type` + `schedule_value` → `cron` (nullable, NULL = one-shot)
- Removed: `context_mode`, `last_run`, `last_result`
- Removed: `task_run_logs` table
- Migration renumbered (0003-0004)

### Removed

- `scheduler/` package (embedded in gateway, replaced by services/timed/)
- `actions/` package (dead code, unused)
- `store.DueTasks()`, `store.LogRun()`, `store.AllTasks()`,
  `store.UnreportedRuns()`, `store.MarkRunsReported()` (old scheduler methods)
- `container.Input.IsTask` field (dead)
- `core.Task.SchedTyp`, `SchedVal`, `CtxMode` fields (old schema)
- `gateway.formatTaskRuns()` (used removed task_run_logs)

### Changed

- `schedule_task` MCP tool: takes `targetJid`, `prompt`, `cron` (optional).
  No more `schedule_type`/`schedule_value`/`context_mode` params.
- `store.UpdateTask`: consolidated from two SQL queries to one.
- `store.ListTasks`: replaced duplicate `AllTasks()`.

### Tests

- 10 timed daemon unit tests (migration, poll, cron, one-shot, paused, future)
- 12 microservice contract integration tests (schema compat, message insertion,
  task lifecycle, table ownership isolation)
- 16 store task edge case tests (duplicates, nonexistent, empty patch, filters)

### Specs

- Tool names aligned with code: `delegate_group`, `reset_session`,
  `get_routes`/`set_routes`/`add_route`/`delete_route`
- Parameter names: camelCase (`targetJid`, `taskId`)
- actid/authd marked as design (currently inline in gated)

---

## [v0.0.1] — 2026-03-07

Go rewrite. All core gateway functionality ported from TypeScript.
16 packages, ~4,700 LOC Go (vs ~9,400 LOC TS).

### Packages

- **core/** — Config, types (Message, Group, Task, Channel interface)
- **store/** — SQLite persistence (12 tables, WAL mode, PRAGMA user_version migrations)
- **gateway/** — Main loop, message routing, commands (/new, /ping, /chatid, /stop)
- **container/** — Docker spawn, 8 volume mount types, MCP sidecar lifecycle, skills seeding
- **queue/** — Per-group concurrency, stdin piping, circuit breaker (3 failures)
- **router/** — XML message formatting, 5-tier routing rules, outbound filtering
- **ipc/** — File-based request/reply + legacy fire-and-forget, SIGUSR1 wake
- **scheduler/** — Cron/interval/once task runner (robfig/cron), run logging
- **diary/** — YAML frontmatter diary annotations with age labels
- **groupfolder/** — Group path resolution and validation
- **mountsec/** — Mount allowlist validation (blocked patterns, read-only enforcement)
- **runtime/** — Docker binary abstraction, orphan cleanup
- **logger/** — slog JSON handler init
- **cmd/arizuko/** — CLI entrypoint (run, create, group subcommands)

### Features

- Per-chat error tracking with cursor rollback on agent failure
- Forward/reply message metadata (forwarded_from, reply_to_text, reply_to_sender)
- MCP sidecar management (start/stop/settings wiring via Unix sockets)
- Gateway capabilities manifest (.gateway-caps TOML)
- Per-channel output styling (outputStyle in settings.json)
- Diary annotations injected into agent context
- Docker-in-docker path translation via HOST_DATA_DIR/HOST_APP_DIR

### Not ported

- Channel adapters (telegram, discord, whatsapp, email) — Channel interface exists
- Action registry (unified action system with typed schemas)
- Web proxy + auth layer
- MIME enricher (attachment pipeline)
- Slink web channel

---

## TypeScript releases (pre-Go rewrite)

---

## [v0.6.3] — 2026-03-06

### Features

- Media-aware file sending: telegram routes photos/videos/audio/animations
  to native API methods (inline display); whatsapp routes by MIME type
- Diary spec: Stop hook nudge after 100 turns, task tracking in entries,
  terse summary format

### Fixes

- Replace agent error retry loop with circuit breaker (manual retry only,
  warns after 3 consecutive failures per group)
- Telegram: removed dead `method` variable in sendDocument

---

## [v0.6.2] — 2026-03-06

### Features

- Telegram: images (PNG/JPG/GIF/WEBP) sent via `sendPhoto` for inline
  display instead of `sendDocument` (file attachment)

### Fixes

- Agent CLAUDE.md: `send_file` no longer prompts follow-up text description

---

## [v0.6.1] — 2026-03-06

### Fixes

- Container stop: `exec()` → `execFileSync`/`spawn` (no shell anywhere)
- Command handlers: `await` instead of fire-and-forget (race condition)
- Cursor rollback: restore cursor on agent error when no output was sent
  (previousCursor was saved but never used — messages in DB but invisible)
- Routing schema: `.max(200)` on pattern/sender Zod fields (was only
  enforced at runtime, silent failure)
- Sidecar socket cleanup: catch only ENOENT (was `catch {}`)
- Agent container: use `bunx tsc` for build, validate-only compile step

---

## [v0.6.0] — 2026-03-06

### Fixes

- IPC: catch only ENOENT on file cleanup (was swallowing all errors)
- IPC: validate envelope id/type fields, reject malformed requests
- IPC: delete failed files instead of accumulating in errors/ dir
- Routing: cap regex pattern length at 200 chars (ReDoS mitigation)
- Config: validate TIMEZONE via Intl.DateTimeFormat, fallback to UTC
- Sidecar: use spawn() instead of exec() for lifecycle (shell injection fix)

### Features

- **Hierarchical group routing**: parent groups delegate to children via
  routing rules (command, pattern, keyword, sender, default). Authorization
  enforces same-world, direct parent-child only. Max delegation depth 3.
- **Sidecar isolation**: per-group MCP sidecars via `SIDECAR_<NAME>_IMAGE`
  env vars. Socket transport at `/workspace/ipc/sidecars/<name>.sock`.
  Gateway manages lifecycle (start, probe, reconcile settings, stop).
- **Action input validation**: Zod schemas on all actions; malformed
  IPC requests rejected with typed error replies.
- **New actions**: `delegate_group`, `set_routing_rules`
- **Session history**: `session_history` table replaces `sessions`;
  new-session injection includes last 2 previous sessions

---

## [v0.5.0] — 2026-03-06

### Features

- **Action registry**: unified action system — all IPC handlers, MCP
  tools, and commands reference a single `Action` interface with typed
  Zod schemas and authorization. `src/action-registry.ts` + `src/actions/`
- **Request-response IPC**: agents write to `requests/`, poll `replies/`.
  Gateway dispatches through action registry and writes typed replies.
  Fire-and-forget IPC retained for backwards compat during rollout.
- **Tool discovery**: gateway writes `action_manifest.json` at spawn
  time. Agent MCP server reads manifest for dynamic tool registration.
- **Agent MCP self-registration**: agent-written `mcpServers` in
  `settings.json` are merged with built-in `nanoclaw` server.
  Dynamic `allowedTools` includes `mcp__<name>__*` wildcards.
- **Message threading types**: `SendOpts { replyTo }` on Channel
  interface, `replyTo` field on `NewMessage`

### Breaking

- `processTaskIpc` moved from `ipc.ts` to `ipc-compat.ts`
- IPC handlers refactored into `src/actions/` modules

---

## [v0.4.0] — 2026-03-06

### Breaking

- `NANOCLAW_IS_MAIN` env var → `NANOCLAW_IS_ROOT`
- `/workspace/global` mount → `/workspace/share`
- `isMain` removed from `ContainerInput` interface

### Changes

- `isMain` → `isRoot(folder)` — structural check (`!folder.includes('/')`)
  replaces hardcoded `MAIN_GROUP_FOLDER = 'main'` comparison
- `groups/global/` → `groups/<world>/share/` — shared state lives inside
  world root, always mounted (rw for root, ro for children)
- Folder validation allows `/` separator for future hierarchy
- Reserved folder `global` → `share`

---

## [v0.3.0] — 2026-03-06

### Features

- **System messages**: `system_messages` and `sessions` DB tables. Gateway
  enqueues context annotations (new-session history, new-day marker, command
  context) and flushes them as XML before user messages in agent stdin.
- **Session recording**: every container spawn/exit recorded in `sessions`
  table with timing, message count, result, and error. New-session injection
  includes last 2 previous sessions as `<previous_session>` XML elements.
- **Command registry** (`src/commands/`): pluggable handlers replace
  hardcoded telegram commands. `/new` (session reset with continuity),
  `/ping`, `/chatid` shipped. Commands intercepted in message loop before
  agent routing.
- **`reset_session` IPC**: agent can clear its own session via IPC message.
- **Error notification**: on agent error, user receives retry prompt and
  message cursor rolls back. If output was already sent, cursor is preserved
  to prevent duplicate delivery.
- **Agent SKILL.md**: documents system message origins, session history
  access (`~/.claude/projects/`), group configuration files, whisper
  language config. Migrations 005-007.
- **agent-runner CLAUDE.md**: session layout documentation for in-container
  agent.

### Fixes

- System message format corrected (origin+event attributes, no colon).
- Voice transcription label now `[voice/auto→en: ...]` (was `[voice: ...]`).

---

## [v0.2.8] — 2026-03-05

### Features

- Agent self-skill documents session history access (`~/.claude/projects/`)
  and `.whisper-language` group configuration file.
- Migration 005: whisper language config docs. Migration 006: session history.

### Fixes

- System message format corrected in specs/SKILL.md (origin+event, no colon).
- Voice transcription label now `[voice/auto→en: ...]` (was `[voice: ...]`).

---

## [v0.2.7] — 2026-03-05

### Fixes

- **Voice transcription in active sessions**: second voice message in a
  running container session was missing transcription. Root cause: message
  objects fetched before `waitForEnrichments`, then used stale after wait.
  Both dispatch paths (new container + stdin pipe) now re-fetch from DB
  after enrichment completes, so voice/video content is always included.
- IPC drain race: concurrent `drainGroupMessages` calls for same group
  caused duplicate file sends. Fixed with per-group boolean lock.

### Features

- Whisper large-v3 model for better multilingual accuracy.
- Per-group language configuration via `.whisper-language` file.
- Parallel transcription passes: auto-detect + each configured language.
  Output labeled `[voice/auto→{detected}]` or `[voice/{forced}]`.
- Sidecar returns detected language in response; whisper.ts returns
  `WhisperResult { text, language }`.
- Whisper timeout increased to 60s for large-v3 multi-pass.

### Testing

- `src/mime-enricher.test.ts`: 7 tests covering enrichment pipeline,
  race condition (fast-settling enrichment before wait), error swallowing.
- `src/mime-handlers/voice.test.ts`: updated for multi-pass labels and
  `WhisperResult` return type.
- `src/mime-handlers/whisper.test.ts`: updated for `WhisperResult`,
  60s abort timeout.
- `specs/2/2-autotesting.md`: test strategy for all subsystems.

---

## [v0.2.6] — 2026-03-04

### Testing

- `vitest` added as devDependency; `make test` and npm scripts use bare
  `vitest run` (no npx/bunx wrapper)
- `src/config.test.ts`: live-binding assertions for config overrides;
  `_resetConfig()` restores defaults from env in `afterEach`
- `container-runner.ts`: `export let _spawnProcess = spawn` seam allows
  mocking docker without a running daemon
- Fixed container-runner test mocks: missing `HOST_APP_DIR`/`WEB_HOST`
  constants; `readFileSync` mock returning `''` now returns `'{}'`
- `specs/1/b-testing.md`: all testability gaps marked shipped

### Config

- 7 constants changed `const` → `let` in `config.ts`: `SLINK_ANON_RPM`,
  `SLINK_AUTH_RPM`, `WHISPER_BASE_URL`, `VOICE_TRANSCRIPTION_ENABLED`,
  `VIDEO_TRANSCRIPTION_ENABLED`, `MEDIA_ENABLED`, `MEDIA_MAX_FILE_BYTES`
- `_overrideConfig` mutates live bindings directly (was partial)
- `_resetConfig()` added to restore defaults from env; both gated behind
  `NODE_ENV=test`

---

## [v0.2.5] — 2026-03-04

### Gateway

- Fix `hostPath()` to replace `PROJECT_ROOT` instead of `APP_DIR`, fixing
  wrong host mount paths for IPC/session dirs when running inside Docker
- Fix `ipc.ts` file sending: use `HOST_GROUPS_DIR` (host path) instead of
  `GROUPS_DIR` (container-internal path), fixing ENOENT on `sendDocument`

### Skills

- Auto-migration nudge: gateway prepends annotation to agent prompt when
  group skills are behind `MIGRATION_VERSION`
- `MIGRATION_VERSION` bumped to 4

### Specs

- All `specs/1/` marked with shipped/partial/open status
- `specs/1/X-sync.md` rewritten as solved

### Cleanup

- Delete stale `template/workspace/mcporter.json` artifact
- Fix stale template path in `container/skills/howto/SKILL.md`

---

## [v0.2.4] — 2026-03-04

### CLI

- `arizuko config <instance> user list|add|rm|passwd` for local user management;
  passwords hashed with argon2; values passed via env vars to prevent shell injection

### Auth

- `POST /auth/refresh`: token rotation — issues new access + refresh token pair,
  invalidates old refresh token
- `POST /auth/refresh` JWT now carries correct user name (was using sub string)
- OAuth providers deferred to `specs/v3/auth-oauth.md`

### Specs

- `specs/1/3-auth.md`: updated to reflect v1 implementation

---

## [v0.2.3] — 2026-03-04

### Gateway

- Email channel: IMAP IDLE loop with SMTP reply threading, routes to main
  group; enabled by `EMAIL_IMAP_HOST` config
- `send_file` Discord support: `sendDocument` via `AttachmentBuilder`
- `send_file` WhatsApp support: `sendDocument` via baileys document message
- `src/mime.ts`: shared `mimeFromFile()` helper using file-type (magic bytes)
- `email_threads` table in DB: `getEmailThread`, `getEmailThreadByMsgId`,
  `storeEmailThread` for SMTP reply threading
- Explicit `DATA_DIR`/`HOST_DATA_DIR`/`HOST_APP_DIR` env vars replace brittle
  `/proc/self/mountinfo` host-path detection; gateway cwd stays at `/srv/app`

### Agent skills

- Migration 004: enforce `send_file` for file delivery (CLAUDE.md rule);
  `send_file` accepts any `/workspace` path, not restricted to `media/`

---

## [v0.2.2] — 2026-03-04

### Gateway

- Outbound file sending: `send_file` MCP tool lets agents send files to users
  as document attachments (Telegram); IPC `type:'file'` handler with
  path-safety check against GROUPS_DIR
- Session error eviction: on agent error output, session ID is not persisted;
  on error status, the session pointer is removed from DB (JSONL kept on disk)
  so the next retry starts a fresh session rather than re-entering a corrupted one
- Inject `NANOCLAW_IS_MAIN` into agent `settings.json` on every spawn (was
  never set, so agents always saw it as empty)

### Agent skills

- `migrate` skill: replace `/workspace/global` dir-existence check with
  `NANOCLAW_IS_MAIN != 1` check — the dir always exists due to Dockerfile
  mkdir, making the old check unreliable for main-group detection

---

## [v0.2.1] — 2026-03-04

### Agent runner

- Progress updates: every 100 SDK messages, emits last assistant text snippet
  to the channel so users see activity on long runs
- `error_max_turns` recovery: resumes the session with `maxTurns=3` and asks
  Claude to summarise what was accomplished and what remains, then prompts the
  user to say "continue"

---

## [v0.2.0] — 2026-03-04

### Slink web channel

- Added `POST /pub/s/:token` endpoint — web channel for groups registered as `web:<name>`
- Served `REDACTED.js` client widget at `/pub/REDACTED.js`
- Verified JWT signatures (HS256) for authenticated senders
- Added anon/auth rate limiting via `SLINK_ANON_RPM` / `SLINK_AUTH_RPM` config
- Supported `media_url` attachments with MIME type guessing
- Added SSE stream at `/_REDACTED/stream` for agent-to-browser push
- Added `slink_token` column on `registered_groups`; added `generateSlinkToken` helper
- Fixed expired JWT treated as anon (now returns 401)
- Fixed slink deduplication and SSE error logging

### Auth layer

- Added auth DB schema: `users`, `sessions`, `oauth_accounts` tables
- Added auth query functions: `createUser`, `getUserByProvider`, `createSession`, etc.
- Added `AUTH_SECRET` config constant for JWT signing
- Added web UI auth spec at `specs/1/3-auth.md`

### Whisper sidecar

- Added self-contained `arizuko-whisper` docker image, deployed via Ansible
- Added `whisperTranscribe` helper with 30s abort timeout
- Updated voice and video handlers to use shared whisper endpoint

### Mime pipeline

- Added attachment enrichment before agent dispatch
- Added handler registry: voice, video, image handlers
- Dispatched handlers in parallel with `allSettled` (partial failure safe)
- Added MIME type detection, file save, and annotation lines

### Workspace and agent identity

- Mounted `/workspace/self` read-only to expose full arizuko source to agent
- Replaced `SOUL.md` with ElizaOS-style `character.json`
- Added per-query field randomisation and global override merge in `character.json`
- Split `web/pub/` as unauthenticated boundary; `/pub/` prefix is public

### Skills and migrations

- Added `self` skill: agent introspection — layout, skills, channels, migration version
- Added `migrate` skill: main-group skill sync + migration runner across all groups
- Added migration system: `container/skills/self/migrations/` with versioned files
- Added migration 001: move `web/` root files to `web/pub/` per new layout convention
- Added YAML frontmatter to `web/SKILL.md`
- Updated `info/SKILL.md` to report migration version and warn if migrations pending

### Build

- Added `container/Makefile` for `arizuko-agent` image builds
- Added `sidecar/whisper/Makefile` for `arizuko-whisper` image builds
- Root `make image` now builds only the gateway (`arizuko`)

### Testing

- Added testability seams: `_initTestDatabase`, `setDatabase`, `_overrideConfig`
- Reached 306 tests across 22 files

---

## [v0.1.2] — 2026-03-01

### Added

- Signal-driven IPC: gateway sends SIGUSR1 after writing IPC file; agent
  wakes immediately, falls back to 500ms poll — eliminates busy-waiting

### Fixed

- Race condition in wakeup/timer assignment in agent IPC polling
- `cleanupOrphans` dual-filter restored to OR logic (AND regression in v0.1.1)
- Typing indicator now stops correctly when agent finishes responding
- Extracted `signalContainer` and `scanGroupFolders` helpers to deduplicate
  signal-sending logic

---

## [v0.1.1] — 2026-03-01

### Added

- Skills consolidated into `container/skills/`; seeded once per group on
  first container run
- Vite web server integrated into gateway startup via IPC restart
- Web app template seeded from `template/web/` on `arizuko create`
- Group management CLI (`arizuko group list|add|rm <instance>`)
- `hello` and `howto` skills bundled in agent image
- Pre-commit hooks: prettier, typecheck, hygiene (`.pre-commit-config.yaml`)
- Makefile targets: `build`, `lint`, `test`
- Discord channel via discord.js (`channels/discord.ts`)
- Env-based channel toggling: Telegram by `TELEGRAM_BOT_TOKEN`, Discord by
  `DISCORD_BOT_TOKEN`, WhatsApp by `store/auth/creds.json` presence

### Changed

- `TELEGRAM_ONLY` flag removed; channel selection is token/credential-driven
- Unified `ChannelOpts` type across all three channel modules

### Fixed

- Render markdown as HTML in Telegram; keep typing indicator alive during
  long responses
- Agent-team subcontainers cleaned up on gateway startup
- Fallback to script-relative template dir when not running inside container
- Docker-in-docker mount paths and agent container write permissions
- Bootstrap chicken-and-egg: `group add` now creates DB schema if missing
- `appDir` used for skills source path instead of `process.cwd()`

---

## [v0.1.0] — 2026-03-01

Initial arizuko release — nanoclaw fork with Telegram support and
multitenant instance model.

### Added

- Fork of nanoclaw at upstream v1.1.3
- Telegram channel (`channels/telegram.ts` via grammy)
- `arizuko` bash entrypoint: `create`, `group`, and instance-run commands
- Per-instance data layout: `/srv/data/arizuko_<name>/`
- systemd unit file templating via `arizuko create <name>`
- `container/agent-runner/` in-container Claude Code entrypoint
- Docker-in-docker host path translation (`detectHostPath()` via
  `/proc/self/mountinfo`)

### Inherited from nanoclaw v1.1.x

- Mount project root read-only (container escape prevention)
- Symlink and path-escape blocking in skills file ops
- `fetchLatestWaWebVersion` to prevent WhatsApp 405 failures
- Host timezone propagation to agent container
- `assistantName` passed to agent (was hardcoded as `'Andy'`)
- Idle preemption correctly triggered for scheduled tasks
