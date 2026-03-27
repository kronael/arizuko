# CLAUDE.md

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
make agent    # agent docker image (make -C ant image)

# Run a single test package
go test ./gateway/... -count=1 -run TestName
```

Tests use `modernc.org/sqlite` (pure Go, no CGO).
Exception: `gated` requires `CGO_ENABLED=1` (see Makefile).
Pre-commit hooks configured via `.pre-commit-config.yaml`.

## Architecture

**Flow**: Channel adapter → HTTP API → store.PutMessage →
gateway.messageLoop polls → GroupQueue → container.Run (docker run)
→ stream output → HTTPChannel.Send → channel adapter.

See ARCHITECTURE.md for package graph, schema, container model.

## Layout

```
cmd/arizuko/       CLI entrypoint (generate, run, create, group, status)
core/              Config, types, Channel interface
store/             SQLite (messages.db)
gateway/           Main loop + commands
container/         Docker runner + sidecars + runtime (Go package only)
ant/               In-container agent (TypeScript entrypoint, skills, Dockerfile)
  skills/          Agent-side skills seeded into ~/.claude/skills/
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
template/          Instance seed files; services/ is the catalog of available adapters
  services/        Channel adapter service TOMLs (teled, whapd, discd, …)
  web/             Vite web app scaffold
sidecar/           MCP server binaries
gated/             Gateway daemon (standalone binary)
timed/             Scheduler daemon (standalone binary)
onbod/             Onboarding daemon (auto-included when ONBOARDING_ENABLED=true)
dashd/             Operator dashboards
grants/            Grant rule engine
notify/            Operator notifications (library)
chanlib/           Shared primitives for channel adapter daemons
teled/             Telegram adapter (Go)
discd/             Discord adapter (Go)
mastd/             Mastodon adapter (Go)
bskyd/             Bluesky adapter (Go)
reditd/            Reddit adapter (Go)
whapd/             WhatsApp adapter (TypeScript)
proxyd/            Web proxy daemon (auth, /dash/, /auth/, Vite)
cfg/               Instance config files (per-deploy .env snapshots)
```

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Container output delimited by `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---`
- IPC: MCP over unix socket, socat bridge into container

## Data Dir

`/srv/data/arizuko_<name>/` per instance:

- `.env` — config (gateway reads from cwd)
- `store/` — SQLite DB
- `groups/<folder>/` — group files, logs, diary
- `data/ipc/<folder>/` — MCP unix sockets
- `groups/<folder>/.claude/` — agent session state (skills, settings, CLAUDE.md)

## Config

All config via `.env` in data dir or env vars (`core.LoadConfig`).
Key values: `ASSISTANT_NAME`, `CONTAINER_IMAGE`, `IDLE_TIMEOUT`,
`MAX_CONCURRENT_CONTAINERS`, `API_PORT`, `CHANNEL_SECRET`,
`ONBOARDING_ENABLED`.

`HOST_DATA_DIR` and `HOST_APP_DIR` for docker-in-docker path translation.
API server always starts (default port 8080).

## Entrypoint

`arizuko generate <instance>` — write `docker-compose.yml` to the data dir (no docker needed).
`arizuko run <instance>` — generate compose then run `docker compose up` (what systemd calls via `docker run arizuko:latest arizuko generate <name>` + `docker compose up`).
`arizuko create <name>` — seed data dir, .env, default group.
`arizuko group <instance> list|add|rm` — manage registered groups.
`arizuko status <instance>` — show compose services and registered channels.

Daemons are standalone binaries: `gated`, `timed`, `teled`, `discd`, `mastd`, `bskyd`, `reditd`, `onbod`, `dashd`. Each in `<name>/main.go`.

## Service Architecture

Daemons end in `d` (4+d naming), libraries don't. Shared SQLite DB (WAL mode).

| Name      | Type    | Role                                                                  |
| --------- | ------- | --------------------------------------------------------------------- |
| `gated`   | daemon  | Message loop, routing, containers                                     |
| `timed`   | daemon  | Cron poll, writes to messages                                         |
| `onbod`   | daemon  | Onboarding state machine (auto-included when ONBOARDING_ENABLED=true) |
| `dashd`   | daemon  | Operator dashboards (HTMX)                                            |
| `ipc`     | library | MCP server, identity stamping                                         |
| `auth`    | library | Authorization policy, JWT, OAuth                                      |
| `grants`  | library | Grant rule engine                                                     |
| `notify`  | library | Operator notifications                                                |
| `chanlib` | library | Shared HTTP + auth primitives for channel adapters                    |
| `teled`   | daemon  | Telegram adapter (Go)                                                 |
| `discd`   | daemon  | Discord adapter (Go)                                                  |
| `mastd`   | daemon  | Mastodon adapter (Go)                                                 |
| `bskyd`   | daemon  | Bluesky adapter (Go)                                                  |
| `reditd`  | daemon  | Reddit adapter (Go)                                                   |
| `whapd`   | daemon  | WhatsApp adapter (TypeScript)                                         |
| `proxyd`  | daemon  | Web proxy: auth gate, /dash/, /auth/, Vite                            |
| `vited`   | service | Vite dev server (arizuko-vite image)                                  |
| `emaid`   | daemon  | Email adapter (IMAP/SMTP, Go)                                         |

Go daemons: `<name>/main.go`. TS daemons: `<name>/src/main.ts`.
Libraries: `ipc/`, `auth/`, `chanlib/`. Host CLI: `cmd/arizuko/main.go`.

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
ls /srv/data/arizuko_<instance>/data/ipc/*/gated.sock 2>/dev/null
```

Red flags: `"error in message loop"`, `"container timeout"`,
`"circuit breaker open"`, `"agent error"`, no log activity >30s.

Key error emitters: `gateway/gateway.go` (message loop),
`queue/queue.go` (concurrency/circuit breaker),
`container/runner.go` (spawn/timeout), `ipc/ipc.go` (MCP).

## Shipping changes

1. Add entry to `CHANGELOG.md`
2. Add migration file `ant/skills/self/migrations/NNN-desc.md`
3. Update `ant/skills/self/MIGRATION_VERSION`
4. Update `ant/skills/self/SKILL.md`
5. Rebuild agent image

## Tagging a new version

1. Update `CHANGELOG.md` — move [Unreleased] to `[vX.Y.Z] — YYYY-MM-DD`
2. Update `README.md` and `ARCHITECTURE.md` if needed
3. `git tag vX.Y.Z`
4. Tag docker images: `docker tag arizuko:latest arizuko:vX.Y.Z` and same for `arizuko-ant`
5. Add `.diary/YYYYMMDD.md` entry

## Migrating from kanipi

See `MIGRATION.md` at repo root — non-obvious differences only (IPC transport,
schema, auth hashing, env vars, feature gaps).

## Related projects

- `/home/onvos/app/eliza-atlas` — ElizaOS fork; reference for facts/memory
- `/home/onvos/app/refs/brainpro` — reference for daily notes pattern
