# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is kanipi

Nanoclaw fork — multitenant Claude agent gateway with
multi-channel support (telegram, whatsapp, discord).
systemd-managed instances, MCP sidecar extensibility.

## Build & Test

```bash
make image          # gateway docker image
make agent-image    # agent docker image
npm run build       # tsc compile (src/ → dist/)
npm run dev         # tsx dev mode
npx tsc --noEmit    # typecheck without emitting
```

No test runner configured — tests are `*.test.ts` files
next to source. Run individually with `npx tsx src/foo.test.ts`.

## Architecture

TypeScript (ESM, NodeNext). Gateway polls channels for
messages, routes to containerized Claude agents via docker.

**Flow**: Channel → DB (store message) → message loop polls
→ GroupQueue → runContainerAgent (docker exec) → stream
output back to channel.

Key modules:
- `index.ts` — main loop, channel init, message routing
- `config.ts` — all config from `.env` + env vars
- `db.ts` — SQLite (better-sqlite3) for messages, state, tasks
- `container-runner.ts` — spawns agent containers, streams I/O
- `container-runtime.ts` — docker lifecycle, orphan cleanup
- `group-queue.ts` — per-group message queueing, stdin piping
- `router.ts` — message formatting, channel→JID resolution
- `ipc.ts` — container↔gateway communication (file-based)
- `task-scheduler.ts` — cron-based scheduled tasks
- `channels/` — telegram (grammy), whatsapp (baileys), discord (discord.js)

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
  workspace/skills/   curated skills (ship, reload, info)
sidecar/              MCP server binaries
kanipi                bash entrypoint (create/run)
```

## Data Dir

`/srv/data/kanipi_<name>/` per instance:
- `.env` — config (gateway reads from cwd)
- `state/` — persistent state, SQLite DB, whatsapp auth
- `groups/main/logs/` — conversation logs
- `data/` — instance data

## Config

All config via `.env` in data dir or env vars. Key values:
`ASSISTANT_NAME`, `TELEGRAM_BOT_TOKEN`, `DISCORD_BOT_TOKEN`,
`CONTAINER_IMAGE`, `IDLE_TIMEOUT`, `MAX_CONCURRENT_CONTAINERS`.

Channels enabled by token presence (telegram/discord) or
auth dir existence (whatsapp).

## Entrypoint

`kanipi create <name>` — seed data dir, .env, systemd unit.
`kanipi <instance>` — cd to home, exec node dist/index.js.
