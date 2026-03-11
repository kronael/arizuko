# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is arizuko

Nanoclaw fork — multitenant Claude agent gateway with
multi-channel support (telegram, whatsapp, discord, email).
systemd-managed instances, MCP sidecar extensibility.

## Build & Test

```bash
make build    # go build → ./arizuko + channels/telegram binary
make lint     # go vet ./...
make test     # go test ./... -count=1
make image    # router docker image
make agent    # agent docker image (make -C container image)
```

Tests use `modernc.org/sqlite` (pure Go, no CGO).
Pre-commit hooks configured via `.pre-commit-config.yaml`.

## Architecture

Go binary (router). Channels are external processes that register
via HTTP; router polls stored messages, routes to containerized
Claude agents via docker, streams output back to channels.

**Flow**: Channel adapter → HTTP API → store.PutMessage →
gateway.messageLoop polls → GroupQueue → container.Run (docker run)
→ stream output → HTTPChannel.Send → channel adapter.

See ARCHITECTURE.md for package graph, schema, container model.

## Packages

- `cmd/arizuko/` — entrypoint (run, create, group subcommands)
- `core/` — Config, types (Message, Group, Task, Channel interface)
- `store/` — SQLite persistence (messages, groups, sessions, tasks, auth)
- `gateway/` — main loop, message routing, commands (/new, /ping, /chatid, /stop)
- `container/` — docker spawn, volume mounts, sidecars, skills seeding
- `queue/` — per-group concurrency, stdin piping, circuit breaker
- `router/` — XML message formatting, routing rules, outbound filtering
- `chanreg/` — channel registry, health checks, HTTP channel proxy (outbound)
- `api/` — HTTP API server (channel registration, inbound messages, chat metadata)
- `ipc/` — file-based IPC watcher (request/reply + legacy fire-and-forget)
- `scheduler/` — cron/interval/once task runner (robfig/cron)
- `diary/` — YAML frontmatter diary annotations for agent context
- `groupfolder/` — group path resolution and validation
- `mountsec/` — mount allowlist validation
- `runtime/` — docker binary abstraction, orphan cleanup
- `logger/` — slog JSON handler init

## Layout

```
cmd/arizuko/       CLI entrypoint
core/              Config, types, Channel interface
store/             SQLite (messages.db)
gateway/           Main loop + commands
container/         Docker runner + sidecars
  agent-runner/    In-container agent entrypoint
  skills/          Agent-side skills
queue/             Per-group concurrency
router/            Message formatting + routing
chanreg/           Channel registry + HTTP proxy
api/               Router HTTP API server
channels/telegram/ Standalone telegram adapter
ipc/               File-based IPC
scheduler/         Task scheduler
diary/             Diary annotations
groupfolder/       Path validation
mountsec/          Mount security
runtime/           Docker lifecycle
logger/            Logging init
template/          Instance seed files
sidecar/           MCP server binaries
```

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Container output delimited by `---NANOCLAW_OUTPUT_START---` / `---NANOCLAW_OUTPUT_END---`
- IPC: atomic writes via tmp+rename, SIGUSR1 for immediate wake

## Data Dir

`/srv/data/arizuko_<name>/` per instance:

- `.env` — config (gateway reads from cwd)
- `store/` — SQLite DB
- `groups/<folder>/` — group files, logs, diary
- `data/ipc/<folder>/` — IPC directories
- `data/sessions/<folder>/.claude/` — agent session state

## Config

All config via `.env` in data dir or env vars (`core.LoadConfig`).
Key values: `ASSISTANT_NAME`, `TELEGRAM_BOT_TOKEN`, `DISCORD_BOT_TOKEN`,
`EMAIL_IMAP_HOST`, `CONTAINER_IMAGE`, `IDLE_TIMEOUT`, `MAX_CONCURRENT_CONTAINERS`,
`API_PORT`, `CHANNEL_SECRET`.

`HOST_DATA_DIR` and `HOST_APP_DIR` for docker-in-docker path translation.
Channels enabled by token/config presence.

## Entrypoint

`arizuko run` — load config, open store, start gateway.
`arizuko create <name>` — seed data dir, .env, default group.
`arizuko group <instance> list|add|rm` — manage registered groups.

## Design Philosophy

Minimal and orthogonal — components independently useful with
narrow responsibility. Each subsystem (channels, memory, IPC,
scheduler) operates on clean interfaces, knows nothing of others.
Complexity is a liability. Agent self-extension (skills, MCP
servers, CLAUDE.md, memory) is the primary extension mechanism.

## Operational check (post-deploy)

```bash
# 1. Service health
sudo systemctl status arizuko_<instance>

# 2. Startup sequence — expect: "state loaded", "channel connected",
#    "ipc watcher started", "scheduler loop started", "arizuko running"
sudo journalctl -u arizuko_<instance> --since "5 minutes ago" --no-pager | head -30

# 3. Errors
sudo journalctl -u arizuko_<instance> --since "5 minutes ago" --no-pager \
  | grep -iE 'error|warn|fatal|crash|unhandled|reject'

# 4. Container orphans
sudo docker ps --filter "name=arizuko-" --format "{{.Names}} {{.Status}}"

# 5. IPC file accumulation
find /srv/data/arizuko_<instance>/data/ipc/*/requests/ -name '*.json' 2>/dev/null | wc -l
```

Red flags: `"error in message loop"`, `"container timeout"`,
`"circuit breaker open"`, `"agent error"`, no log activity >30s.

Key error emitters: `gateway/gateway.go` (message loop),
`queue/queue.go` (concurrency/circuit breaker),
`container/runner.go` (spawn/timeout), `ipc/watcher.go` (drain).

## Shipping changes

1. Add entry to `CHANGELOG.md`
2. Add migration file `container/skills/self/migrations/NNN-desc.md`
3. Update `container/skills/self/MIGRATION_VERSION`
4. Update `container/skills/self/SKILL.md`
5. Rebuild agent image

## Tagging a new version

1. Update `CHANGELOG.md` — move [Unreleased] to `[vX.Y.Z] — YYYY-MM-DD`
2. Update `README.md` and `ARCHITECTURE.md` if needed
3. `git tag vX.Y.Z`
4. Tag docker images: `docker tag arizuko:latest arizuko:vX.Y.Z` and same for `arizuko-agent`
5. Add `.diary/YYYYMMDD.md` entry

## Related projects

- `/home/onvos/app/eliza-atlas` — ElizaOS fork; reference for facts/memory
- `/home/onvos/app/refs/brainpro` — reference for daily notes pattern
