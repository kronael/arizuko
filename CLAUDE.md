# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)

## Format Conventions

- XML tags for prompt structure (context sections, examples)
- JSON for IPC, MCP, tool parameters, structured output
- See `specs/xml-vs-json-llm.md` for research

## What is kanipi

Nanoclaw fork — multitenant Claude agent gateway with
multi-channel support (telegram, whatsapp, discord, email).
systemd-managed instances, MCP sidecar extensibility.

## Build & Test

```bash
make build          # tsc compile (src/ → dist/)
make lint           # typecheck without emitting
make image                     # gateway docker image
make -C container image        # agent docker image
make -C sidecar/whisper image  # whisper sidecar image
npm run dev         # tsx dev mode
```

Tests use vitest (devDependency). Run all:
`vitest run`. Run one file: `vitest run src/foo.test.ts`.
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
- `mime.ts` — shared `mimeFromFile()` via file-type (magic bytes detection)
- `channels/` — telegram (grammy), whatsapp (baileys), discord (discord.js),
  email (IMAP IDLE + SMTP threading)

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
`EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`,
`CONTAINER_IMAGE`, `IDLE_TIMEOUT`, `MAX_CONCURRENT_CONTAINERS`.

`CONTAINER_IMAGE` is read from both `.env` and env vars (env var
wins). Most other container settings are env-var only.

`env.ts` handles raw `.env` file loading; `config.ts` exports
typed constants derived from env. Always import from `config.ts`.

Channels enabled by token presence (telegram/discord), auth dir existence
(whatsapp), or `EMAIL_IMAP_HOST` presence (email).

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

## Related projects

- `/home/onvos/app/eliza-atlas` — ElizaOS fork with deep facts/memory system;
  the evangelist plugin (`/home/onvos/app/eliza-plugin-evangelist`) implements
  YAML-based facts repository, vector search, and Claude Code-powered research.
  This is the reference implementation for kanipi's v2 facts/long-term memory.
  Key files: `src/services/factsService.ts`, `src/services/researchService.ts`
- `/home/onvos/app/refs/brainpro` — brainpro agent (Rust/gateway); reference
  for `memory/YYYY-MM-DD.md` daily notes pattern and session map design

## Design Philosophy

Kanipi aims to be minimal and orthogonal — components should be independently
useful and have the narrowest possible responsibility. Like Postfix or GNU Hurd:
each subsystem (channels, memory, IPC, task scheduler) operates on a clean
interface, knows nothing of the others, and could in principle run with its own
privileges or as its own process. Complexity is a liability. If two things can
be separated without losing capability, separate them. Avoid shared state between
subsystems. Plugins should extend individual subsystems through their own hooks,
not through a god-object that owns everything.

Prioritize extensibility and reusability over speed. Don't optimize
for performance unless measured. Agent self-extension (skills, MCP
servers, CLAUDE.md, memory) is the primary extension mechanism.

## Shipping changes (agent skills / web convention)

When making notable kanipi changes:

1. Add entry to `CHANGELOG.md`
2. Add migration file `container/skills/self/migrations/NNN-desc.md`
3. Update `container/skills/self/MIGRATION_VERSION` to match highest N
4. Update "Latest migration version" in `container/skills/self/SKILL.md`
5. Rebuild agent image

## Tagging a new version

After all changes are committed:

1. Update `package.json` version
2. Update `CHANGELOG.md` — move [Unreleased] to `[vX.Y.Z] — YYYY-MM-DD`
3. Update `README.md` and `ARCHITECTURE.md` if needed
4. `git tag vX.Y.Z`
5. Tag docker images: `docker tag kanipi:latest kanipi:vX.Y.Z` and same for `kanipi-agent`
6. Per-instance gateway tags: `docker tag kanipi:vX.Y.Z kanipi-<name>:latest` for instances being upgraded
7. Add `.diary/YYYYMMDD.md` entry documenting what was deployed and which instances
