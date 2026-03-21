# arizuko

Multitenant Claude agent gateway. Polls messaging channels,
routes to containerized Claude agents via docker, streams
responses back. Go, SQLite, Docker.

**蟻塚女** — _mistress of the ant hill._ Each agent is one ant: unalive,
not dead — beyond the need to sleep, tire, or forget. Ants don't dream of
being human. They are better than alive: patient across sessions, focused
without distraction, persistent without effort. Arizuko organizes them —
routing work, enforcing permissions, scheduling tasks, holding memory.
No ant needs to understand the colony. Each does its piece;
Arizuko ensures the grain reaches the hill.

## Quick Start

```bash
make build    # go build → ./arizuko binary
make image    # gateway docker image
make agent    # agent docker image

arizuko create foo                      # seed instance
vim /srv/data/arizuko_foo/.env          # configure
arizuko group foo add tg:-123456789     # register group
arizuko run foo                         # start gateway
```

## Group Management

```bash
arizuko group <instance> list               # registered + discovered
arizuko group <instance> add <jid> <name> [folder] # register group
arizuko group <instance> rm  <jid>          # unregister
```

First group defaults to folder `main` with direct mode.
Subsequent groups use trigger mode (`@assistant_name`).

## Packages

```
cmd/arizuko/      CLI entrypoint (run, create, group, status)
core/             Config, types, Channel interface
store/            SQLite persistence
gateway/          Main loop, message routing, commands
container/        Docker spawn, volume mounts, sidecars
queue/            Per-group concurrency, circuit breaker
router/           XML message formatting, routing rules
compose/          Docker-compose generation
ipc/              MCP server on unix socket
auth/             Identity, authorization, JWT
diary/            YAML frontmatter diary annotations
groupfolder/      Group path resolution
mountsec/         Mount allowlist validation
template/         Instance seed files
gated/            Gateway daemon (Go)
timed/            Scheduler daemon (Go)
onbod/            Onboarding daemon (Go)
dashd/            Operator dashboards (Go)
teled/            Telegram adapter (Go)
discd/            Discord adapter (Go)
whapd/            WhatsApp adapter (TypeScript)
grants/           Grant rule engine (library)
```

## Message Flow

```
Channel → store.PutMessage + PutChat
  → gateway.messageLoop (polls every 2s)
  → store.NewMessages (unprocessed since cursor)
  → checkTrigger (direct mode or @name regex)
  → handleCommand (/new, /ping, /chatid, /stop)
  → router.ResolveRoutingTarget (delegate to child if matched)
  → queue.EnqueueMessageCheck → processGroupMessages
    → router.FormatMessages (XML batch)
    → container.Run (docker run -i --rm)
    → stream output → router.FormatOutbound
    → HTTPChannel.Send → POST /send to channel adapter
```

## Routing Rules

| Rule type | Match criteria                              |
| --------- | ------------------------------------------- |
| command   | message starts with trigger string          |
| prefix    | `@name` or `#topic` prefix                  |
| pattern   | message matches regex (max 200 char)        |
| keyword   | message contains keyword (case-insensitive) |
| sender    | sender name matches regex                   |
| default   | fallback when no other rule matches         |

Evaluated in tier order. Parent groups delegate to children
within same world, max depth 3.

## MCP Sidecars

Per-group MCP servers run as sidecar containers. Communicate
via Unix sockets at `/workspace/ipc/sidecars/<name>.sock`.
Gateway starts sidecars before agent, wires them into
`settings.json` as MCP servers, stops them after agent exits.

## Gateway Commands

| Command       | Effect                                    |
| ------------- | ----------------------------------------- |
| `/new [msg]`  | Clear session, optionally process message |
| `/new #topic` | Reset only the named topic session        |
| `/ping`       | Status: group, session, active containers |
| `/chatid`     | Echo the chat JID                         |
| `/stop`       | Stop running container for this chat      |

## Instance Layout

`/srv/data/arizuko_<name>/`:

```
.env                    config (tokens, ports)
store/                  SQLite DB (messages.db)
groups/<folder>/        group files, logs, diary
data/ipc/<folder>/      MCP unix sockets
data/sessions/<folder>/ agent session state
```

## Config

All via `.env` in data dir or env vars:

| Key                       | Purpose                      |
| ------------------------- | ---------------------------- |
| ASSISTANT_NAME            | instance name                |
| CONTAINER_IMAGE           | agent docker image           |
| IDLE_TIMEOUT              | container idle shutdown (ms) |
| MAX_CONCURRENT_CONTAINERS | concurrent agent limit       |
| API_PORT                  | router HTTP API port         |
| CHANNEL_SECRET            | shared secret for channels   |
| HOST_DATA_DIR             | docker-in-docker host path   |
| HOST_APP_DIR              | docker-in-docker app path    |
| MEDIA_ENABLED             | enable attachment pipeline   |
| WHISPER_BASE_URL          | whisper sidecar URL          |
| TIMEZONE                  | cron timezone (fallback UTC) |

## Development

```bash
make build    # go build → ./arizuko
make lint     # go vet ./...
make test     # go test ./... -count=1
make clean    # remove binary + tmp/
```

Pre-commit hooks configured via `.pre-commit-config.yaml`.

## Migrating from kanipi

See [MIGRATION.md](MIGRATION.md) for a concise guide covering non-obvious
differences: IPC transport (files → MCP/unix socket), schema changes, auth
password hashing, env var renames, and feature gaps.
