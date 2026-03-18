# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is arizuko

Nanoclaw fork — multitenant Claude agent router. External
channel adapters register via HTTP; router routes messages
to containerized Claude agents. Docker compose orchestration,
MCP sidecar extensibility.

## Build & Test

```bash
make build    # go build → ./arizuko + all daemon binaries
make lint     # go vet ./...
make test     # go test ./... -count=1
make images   # all docker images (router + adapters + agent)
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

- `cmd/arizuko/` — CLI entrypoint (run, create, group, status subcommands)
- `core/` — Config, types (Message, Group, Task, Channel interface)
- `store/` — SQLite persistence (messages, groups, sessions, tasks, auth)
- `gateway/` — main loop, message routing, commands (/new, /ping, /chatid, /stop)
- `container/` — docker spawn, volume mounts, sidecars, runtime, skills seeding
- `queue/` — per-group concurrency, stdin piping, circuit breaker
- `router/` — XML message formatting, routing rules, outbound filtering
- `chanreg/` — channel registry, health checks, HTTP channel proxy (outbound)
- `api/` — HTTP API server (channel registration, inbound messages, chat metadata)
- `ipc/` — MCP server on unix socket (mark3labs/mcp-go, per-group, runtime auth via auth)
- `auth/` — identity, authorization policy, JWT, OAuth, session middleware
- `diary/` — YAML frontmatter diary annotations for agent context
- `groupfolder/` — group path resolution and validation
- `mountsec/` — mount allowlist validation
- `compose/` — docker-compose.yml generation from \*.toml service configs
- `gated/` — gateway daemon (standalone binary)
- `timed/` — scheduler daemon (standalone binary)
- `onbod/` — onboarding daemon (planned)
- `dashd/` — operator dashboards daemon (planned)
- `teled/` — telegram adapter daemon (Go)
- `discd/` — discord adapter daemon (Go)
- `whapd/` — whatsapp adapter daemon (TypeScript/baileys)
- `grants/` — grant rule engine (library, planned)
- `notify/` — operator notifications (library, planned)

## Layout

```
cmd/arizuko/       CLI entrypoint (run, create, group, status)
core/              Config, types, Channel interface
store/             SQLite (messages.db)
gateway/           Main loop + commands
container/         Docker runner + sidecars + runtime
  agent-runner/    In-container agent entrypoint
  skills/          Agent-side skills
queue/             Per-group concurrency
router/            Message formatting + routing
chanreg/           Channel registry + HTTP proxy
api/               Router HTTP API server
compose/           Docker-compose generation
ipc/               MCP server (unix socket, runtime auth via auth)
auth/              Identity, authorization, JWT, OAuth, middleware
diary/             Diary annotations
groupfolder/       Path validation
mountsec/          Mount security
template/          Instance seed files
sidecar/           MCP server binaries
gated/             Gateway daemon (standalone binary)
timed/             Scheduler daemon (standalone binary)
onbod/             Onboarding daemon (planned)
dashd/             Operator dashboards (planned)
grants/            Grant rule engine (planned)
notify/            Operator notifications (planned)
teled/             Telegram adapter (Go)
discd/             Discord adapter (Go)
whapd/             WhatsApp adapter (TypeScript)
```

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Container output delimited by `---NANOCLAW_OUTPUT_START---` / `---NANOCLAW_OUTPUT_END---`
- IPC: MCP over unix socket, socat bridge into container

## Data Dir

`/srv/data/arizuko_<name>/` per instance:

- `.env` — config (gateway reads from cwd)
- `store/` — SQLite DB
- `groups/<folder>/` — group files, logs, diary
- `data/ipc/<folder>/` — MCP unix sockets
- `data/sessions/<folder>/.claude/` — agent session state

## Config

All config via `.env` in data dir or env vars (`core.LoadConfig`).
Key values: `ASSISTANT_NAME`, `CONTAINER_IMAGE`, `IDLE_TIMEOUT`,
`MAX_CONCURRENT_CONTAINERS`, `API_PORT`, `CHANNEL_SECRET`,
`ONBOARDING_ENABLED`.

`HOST_DATA_DIR` and `HOST_APP_DIR` for docker-in-docker path translation.
API server always starts (default port 8080).

## Entrypoint

`arizuko run <instance>` — generate compose, run `docker compose up` (host command, what systemd calls).
`arizuko create <name>` — seed data dir, .env, default group.
`arizuko group <instance> list|add|rm` — manage registered groups.
`arizuko status <instance>` — show compose services and registered channels.

Daemons are standalone binaries: `gated`, `timed`, `teled`, `discd`, `onbod`, `dashd`. Each in `<name>/main.go`.

## Design Philosophy

Minimal and orthogonal — components independently useful with
narrow responsibility. Each subsystem (channels, memory, IPC,
scheduler) operates on clean interfaces, knows nothing of others.
Complexity is a liability. Agent self-extension (skills, MCP
servers, CLAUDE.md, memory) is the primary extension mechanism.

## Service Architecture

Daemons end in `d` (4+d naming), libraries don't. Shared SQLite DB (WAL mode).

| Name     | Type    | Role                              |
| -------- | ------- | --------------------------------- |
| `gated`  | daemon  | Message loop, routing, containers |
| `timed`  | daemon  | Cron poll, writes to messages     |
| `onbod`  | planned | Onboarding state machine          |
| `dashd`  | planned | Operator dashboards (HTMX)        |
| `ipc`    | library | MCP server, identity stamping     |
| `auth`   | library | Authorization policy, JWT, OAuth  |
| `grants` | planned | Grant rule engine                 |
| `notify` | planned | Operator notifications            |
| `teled`  | daemon  | Telegram adapter (Go)             |
| `discd`  | daemon  | Discord adapter (Go)              |
| `whapd`  | daemon  | WhatsApp adapter (TypeScript)     |
| `emaid`  | planned | Email adapter                     |

Deployment: `arizuko compose <instance>` generates docker-compose.yml.
Go daemons: `<name>/main.go`. TS daemons: `<name>/src/main.ts`.
Libraries: `ipc/`, `auth/`. Host CLI: `cmd/arizuko/main.go`.

See `specs/7/0-architecture.md` for full spec.

## Operational check (post-deploy)

```bash
# 1. Service health
sudo systemctl status arizuko_<instance>

# 2. Startup sequence — expect: "state loaded", "channel connected",
#    "scheduler started", "arizuko running"
sudo journalctl -u arizuko_<instance> --since "5 minutes ago" --no-pager | head -30

# 3. Errors
sudo journalctl -u arizuko_<instance> --since "5 minutes ago" --no-pager \
  | grep -iE 'error|warn|fatal|crash|unhandled|reject'

# 4. Container orphans
sudo docker ps --filter "name=arizuko-" --format "{{.Names}} {{.Status}}"

# 5. MCP socket check
ls /srv/data/arizuko_<instance>/data/ipc/*/nanoclaw.sock 2>/dev/null
```

Red flags: `"error in message loop"`, `"container timeout"`,
`"circuit breaker open"`, `"agent error"`, no log activity >30s.

Key error emitters: `gateway/gateway.go` (message loop),
`queue/queue.go` (concurrency/circuit breaker),
`container/runner.go` (spawn/timeout), `ipc/ipc.go` (MCP).

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
