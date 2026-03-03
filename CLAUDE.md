# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is kanipi

Nanoclaw fork — multitenant Claude agent gateway with
multi-channel support (telegram, whatsapp, discord).
systemd-managed instances, MCP sidecar extensibility.

## Build & Test

```bash
make build          # tsc compile (src/ → dist/)
make lint           # typecheck without emitting
make image          # gateway docker image
make agent-image    # agent docker image
npm run dev         # tsx dev mode
```

Tests use vitest (not in package.json, run via npx). Run all:
`npx vitest run`. Run one file: `npx vitest run src/foo.test.ts`.
Many tests require docker — they'll fail without it.

Pre-commit hooks (prettier, typecheck, hygiene) configured
via `.pre-commit-config.yaml`. Prettier uses single quotes.

## Architecture

TypeScript (ESM, NodeNext). Gateway polls channels for
messages, routes to containerized Claude agents via docker.

**Flow**: Channel → DB (store message) → message loop polls
→ GroupQueue → runContainerAgent (docker run) → stream
output back to channel.

Key modules:

- `index.ts` — main loop, channel init, message routing
- `config.ts` — config from `.env` + env vars (no web config)
- `db.ts` — SQLite (better-sqlite3) for messages, state, tasks
- `container-runner.ts` — spawns agent containers, streams I/O
- `container-runtime.ts` — docker lifecycle, orphan cleanup
- `group-queue.ts` — per-group message queueing, stdin piping
- `router.ts` — message formatting, channel→JID resolution
- `ipc.ts` — container↔gateway communication (file-based, fs.watch-driven)
- `task-scheduler.ts` — cron-based scheduled tasks
- `mount-security.ts` — validates additional mounts against `~/.config/nanoclaw/mount-allowlist.json` (stored outside project to prevent agent tampering)
- `channels/` — telegram (grammy), whatsapp (baileys), discord (discord.js)

**Web**: vite dev server managed by bash entrypoint (not
the TS gateway). No `web-server.ts` — web is external.

**Container model**: each agent runs in a docker container.
Gateway mounts group folder + state into container. Agent
reads prompt from stdin, writes results to stdout as JSON.
`container/agent-runner/` is the in-container entrypoint. IPC is
signal-driven: gateway writes a file then sends SIGUSR1 to the
container; agent wakes immediately on signal, falls back to 500ms poll.

**Docker-in-docker path translation**: when the gateway itself
runs in docker, `process.cwd()` paths are container-internal.
`config.ts:detectHostPath()` reads `/proc/self/mountinfo` to
find the host-side path of `PROJECT_ROOT` so child containers
receive correct host mount paths. `HOST_PROJECT_ROOT_PATH` is
the translated value (falls back to `process.cwd()` on bare metal).
`container-runner.ts:hostPath()` applies the same translation for
session dirs; `chownRecursive()` ensures they are writable by
node uid 1000 inside agent containers.

## Layout

```
src/                  gateway source (TypeScript)
container/            agent container build
  agent-runner/       in-container agent entrypoint
  build.sh            agent image builder
  skills/             agent-side skills
template/             seed for new instances
  web/                vite web app template
sidecar/              MCP server binaries
kanipi                bash entrypoint (create/run/group/vite)
```

## Data Dir

`/srv/data/kanipi_<name>/` per instance:

- `.env` — config (gateway reads from cwd)
- `store/` — SQLite DB, whatsapp auth
- `groups/main/logs/` — conversation logs
- `web/` — vite web app (seeded from template/web/)
- `data/` — IPC, sessions

## Config

All config via `.env` in data dir or env vars. Key values:
`ASSISTANT_NAME`, `TELEGRAM_BOT_TOKEN`, `DISCORD_BOT_TOKEN`,
`CONTAINER_IMAGE`, `IDLE_TIMEOUT`, `MAX_CONCURRENT_CONTAINERS`.

`CONTAINER_IMAGE` is read from both `.env` and env vars (env var
wins). Most other container settings are env-var only.

`env.ts` handles raw `.env` file loading; `config.ts` exports
typed constants derived from env. Always import from `config.ts`.

Channels enabled by token presence (telegram/discord) or
auth dir existence (whatsapp).

## Entrypoint

`kanipi create <name>` — seed data dir, .env, systemd unit.
`kanipi config <instance> group list|add|rm` — manage registered groups.
`kanipi <instance>` — cd to home, run gateway + vite
(restart loop). VITE_PORT/WEB_HOST configured in .env.

Group commands use `node -e` with better-sqlite3 against
`/srv/data/kanipi_$instance/store/messages.db`. `group add`
creates the DB + schema if missing (solves bootstrap).
First group defaults to folder=main, requires_trigger=0.
Subsequent groups require folder arg and use trigger mode.

## Shipping changes (agent skills / web convention)

When making notable kanipi changes:

1. Add entry to `CHANGELOG.md`
2. Add migration file `container/skills/self/migrations/NNN-desc.md`
3. Update `container/skills/self/MIGRATION_VERSION` to match highest N
4. Update "Latest migration version" in `container/skills/self/SKILL.md`
5. Rebuild agent image
