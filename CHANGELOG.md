# Changelog

All notable changes to kanipi are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

kanipi is a fork of [nanoclaw](https://github.com/nicholasgasior/nanoclaw)
(upstream at v1.1.3). Upstream commits are noted where relevant; kanipi
commits begin at the `[fork]` entry on 2026-02-28.

---

## [Unreleased]

Changes on `main` since the last milestone grouping.

---

## [0.5] — 2026-03-01 — Signal-driven IPC

### Added

- SIGUSR1 + inotify wakeup replaces polling loop in IPC layer; eliminates
  busy-waiting between gateway and agent containers.

### Changed

- Extracted helper functions from IPC module; fixed wakeup/timer race
  condition in polling path.

### Fixed

- Stop typing indicator correctly when agent finishes responding.
- Restore dual-filter (OR logic) in `cleanupOrphans`; previous refactor
  accidentally used AND and left orphan containers alive.
- Simplify `cleanupOrphans` and `sendMessage` chunking.
- Render markdown as HTML in Telegram messages; keep typing indicator alive
  during long responses.
- Disable agent-teams feature (IPC model is incompatible — documented in
  specs/v2/agent-teams-ipc.md); chown web dir so agent can write to it.
- Clean up agent-team subagent containers on gateway startup.
- Fall back to script-relative template dir when gateway is not running
  inside a container.

---

## [0.4] — 2026-03-01 — Skills seeding, web serving, group CLI

### Added

- Consolidated skills into `container/skills/`; skills are seeded once per
  group on first container run, not on every invocation.
- Vite web server integrated into gateway startup via IPC restart mechanism.
- Web app template seeded from `template/web/` on instance creation.
- `CLAUDE_CODE_OAUTH_TOKEN` added to `.env` template.
- Group management CLI (`kanipi group list|add|rm <instance>`); first group
  defaults to `folder=main` / `requires_trigger=0`, subsequent groups use
  trigger mode.
- `hello` skill and `howto` template page bundled in agent image.
- `howto` skill; welcome message spec added to v2.
- Pre-commit hooks: prettier, typecheck, hygiene checks via
  `.pre-commit-config.yaml`.
- Makefile targets: `build`, `lint`, `test`.

### Changed

- Use `template/env.example` instead of inline heredoc in entrypoint.
- Extracted `APP_DIR` constant; deduplicated `groupDir` mount logic in
  container runner.
- Unified channel opts into shared `ChannelOpts` type (replaces
  per-channel opts interfaces); affects all three channel modules.
- Minimise diff from upstream gateway code to ease future backports.
- Dockerfile: use `CMD` instead of `ENTRYPOINT` for the agent runner.

### Fixed

- Use `appDir` for skills source path instead of `process.cwd()`.
- Remove dead `HOST_*` exports; log chown failures instead of silently
  swallowing them.
- Docker-in-docker mount path translation; agent container permissions
  (`chownRecursive` to uid 1000).
- Bootstrap chicken-and-egg: `group add` now creates the DB and schema if
  the store does not yet exist.
- Instance mount point stabilised at `/srv/app/home` after several
  renames that caused conflicts with the `kanipi` script name.

---

## [0.3] — 2026-03-01 — Discord channel, env-based toggling

### Added

- Discord channel via discord.js (`channels/discord.ts`).
- Env-based channel toggling: Telegram enabled by `TELEGRAM_BOT_TOKEN`,
  Discord by `DISCORD_BOT_TOKEN`, WhatsApp by presence of
  `store/auth/creds.json`.
- Gitignore for local runtime dirs (`store/`, `groups/`, `data/`).
- `CLAUDE.md` rewritten with architecture overview, module map, and build
  commands.
- Specs directory structure: `specs/v1/`, `specs/v2/`, `specs/v3/`.
  - v1: CLI spec, IPC signal spec, agent-teams spec, sync spec.
  - v2: architecture, channels, DB bootstrap, files, plugins, voice/
    transcription, agent-teams IPC analysis.
  - v3: Go rewrite architecture exploration.

### Changed

- Removed `TELEGRAM_ONLY` flag; channel selection is now purely
  token/credential driven.
- `store/` directory name retained from upstream (not renamed to `state/`
  despite a brief intermediate rename).

---

## [0.2] — 2026-02-28 — Initial kanipi fork

### Added

- Fork of nanoclaw at upstream v1.1.3, targeting multitenant deployments
  with systemd-managed instances.
- Telegram channel (`channels/telegram.ts` via grammy) as the first
  supported channel alongside existing WhatsApp.
- `kanipi` bash entrypoint: `create`, `group`, and instance-run commands.
- Per-instance data directory layout:
  `/srv/data/kanipi_<name>/` with `store/`, `groups/`, `data/`, `web/`.
- systemd unit file templating via `kanipi create <name>`.
- `container/agent-runner/` as the in-container Claude Code entrypoint.
- Docker-in-docker path translation (`detectHostPath()` via
  `/proc/self/mountinfo`; `HOST_PROJECT_ROOT_PATH` config value).
- `hostPath()` helper in container-runner for session dir translation.

### Removed

- Upstream-only files not relevant to kanipi deployment model.

---

## Upstream baseline — nanoclaw v1.1.x (2026-02-21 – 2026-02-25)

Changes inherited from nanoclaw upstream before the fork was cut.
Included here for traceability; these are not kanipi commits.

### Added (upstream)

- Slack channel skill (`/add-slack`).
- Gmail channel skill (`/add-gmail`) with graceful startup when credentials
  are missing and poll backoff.
- Qodo skills and codebase intelligence integration.
- `/update` skill for pulling upstream changes with auto version bumping.
- `.nvmrc` pinning Node 22.

### Changed (upstream)

- Removed deterministic caching system from skills engine.
- CI optimisation, logging improvements, codebase formatting pass.

### Fixed (upstream)

- Mount project root read-only to prevent container escape (security).
- Block symlink escapes in skills file ops (two separate fixes).
- Block group folder path escapes.
- Pass host timezone to container; reject UTC-suffixed timestamps.
- Pass `assistantName` to container agent instead of hardcoding `'Andy'`.
- Use `'Assistant'` as fallback name instead of `'AssistantNameMissing'`.
- `fetchLatestWaWebVersion` to prevent 405 connection failures.
- Add `.catch()` handlers to fire-and-forget async calls.
- Fix QR data handling in WhatsApp auth.
- Pause malformed scheduled tasks instead of crashing.
- Correctly trigger idle preemption in streaming input mode.
- Only preempt idle containers when scheduled tasks enqueue.
- Replace hardcoded `/Users/user` fallback with `os.homedir()`.
- Filter empty messages from polling queries.
- AskUserQuestion tool replaces freeform 'ask the user' in skills.
- Improve type safety and add error logging in several modules.
