# Changelog

All notable changes to kanipi are documented here.

kanipi is a fork of [nanoclaw](https://github.com/nicholasgasior/nanoclaw)
(upstream at v1.1.3).

---

## [Unreleased]

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
- Added web UI auth spec at `specs/v1/auth.md`

### Whisper sidecar

- Added self-contained `kanipi-whisper` docker image, deployed via Ansible
- Added `whisperTranscribe` helper with 30s abort timeout
- Updated voice and video handlers to use shared whisper endpoint

### Mime pipeline

- Added attachment enrichment before agent dispatch
- Added handler registry: voice, video, image handlers
- Dispatched handlers in parallel with `allSettled` (partial failure safe)
- Added MIME type detection, file save, and annotation lines

### Workspace and agent identity

- Mounted `/workspace/self` read-only to expose full kanipi source to agent
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

- Added `container/Makefile` for `kanipi-agent` image builds
- Added `sidecar/whisper/Makefile` for `kanipi-whisper` image builds
- Root `make image` now builds only the gateway (`kanipi`)

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
- Web app template seeded from `template/web/` on `kanipi create`
- Group management CLI (`kanipi group list|add|rm <instance>`)
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

Initial kanipi release — nanoclaw fork with Telegram support and
multitenant instance model.

### Added

- Fork of nanoclaw at upstream v1.1.3
- Telegram channel (`channels/telegram.ts` via grammy)
- `kanipi` bash entrypoint: `create`, `group`, and instance-run commands
- Per-instance data layout: `/srv/data/kanipi_<name>/`
- systemd unit file templating via `kanipi create <name>`
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
