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

No test runner configured — tests are `*.test.ts` files
next to source. Run individually with `npx tsx src/foo.test.ts`.

Pre-commit hooks (prettier, typecheck, hygiene) configured
via `.pre-commit-config.yaml`. Prettier uses single quotes.

## Architecture

TypeScript (ESM, NodeNext). Gateway polls channels for
messages, routes to containerized Claude agents via docker.

**Flow**: Channel → DB (store message) → message loop polls
→ GroupQueue → runContainerAgent (docker exec) → stream
output back to channel.

Key modules:

- `index.ts` — main loop, channel init, message routing
- `config.ts` — config from `.env` + env vars (no web config)
- `db.ts` — SQLite (better-sqlite3) for messages, state, tasks
- `container-runner.ts` — spawns agent containers, streams I/O
- `container-runtime.ts` — docker lifecycle, orphan cleanup
- `group-queue.ts` — per-group message queueing, stdin piping
- `router.ts` — message formatting, channel→JID resolution
- `ipc.ts` — container↔gateway communication (file-based)
- `task-scheduler.ts` — cron-based scheduled tasks
- `channels/` — telegram (grammy), whatsapp (baileys), discord (discord.js)

**Web**: vite dev server managed by bash entrypoint (not
the TS gateway). No `web-server.ts` — web is external.

**Container model**: each agent runs in a docker container.
Gateway mounts group folder + state into container. Agent
reads prompt from stdin, writes results to stdout as JSON.
`container/agent-runner/` is the in-container entrypoint.

## Layout

```
src/                  gateway source (TypeScript)
container/            agent container build
  agent-runner/       in-container agent entrypoint
  build.sh            agent image builder
  skills/             agent-side skills
template/             seed for new instances
  web/                vite web app template
  workspace/skills/   curated skills (ship, reload, info, web)
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

Channels enabled by token presence (telegram/discord) or
auth dir existence (whatsapp).

## Entrypoint

`kanipi create <name>` — seed data dir, .env, systemd unit.
`kanipi group list|add|rm <instance>` — manage registered groups.
`kanipi <instance>` — cd to home, run gateway + vite
(restart loop). VITE_PORT/WEB_HOST configured in .env.

Group commands use `node -e` with better-sqlite3 against
`/srv/data/kanipi_$instance/store/db.sqlite`. `group add`
creates the DB + schema if missing (solves bootstrap).
First group defaults to folder=main, requires_trigger=0.
Subsequent groups require folder arg and use trigger mode.
