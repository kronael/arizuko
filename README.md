# arizuko

Multitenant Claude agent gateway. Polls messaging channels,
routes to containerized Claude agents via docker, streams
responses back. Go, SQLite, Docker.

## Quick Start

```bash
make build    # go build → ./arizuko binary
make image    # gateway docker image
make agent    # agent docker image

arizuko create foo                      # seed instance
vim /srv/data/arizuko_foo/.env          # configure
arizuko group foo add tg:-123456789     # register group
arizuko run                             # start gateway
```

## Group Management

```bash
arizuko group <instance> list               # registered + discovered
arizuko group <instance> add <jid> [folder] # register group
arizuko group <instance> rm  <jid>          # unregister
```

First group defaults to folder `main` with direct mode.
Subsequent groups use trigger mode (`@assistant_name`).

## Packages

```
cmd/arizuko/     CLI entrypoint (run, create, group)
core/            Config, types, Channel interface
store/           SQLite persistence (12 tables, WAL mode)
gateway/         Main loop, message routing, commands
container/       Docker spawn, volume mounts, sidecars
queue/           Per-group concurrency, circuit breaker
router/          XML message formatting, 5-tier routing rules
ipc/             File-based request/reply, SIGUSR1 wake
scheduler/       Cron/interval/once task runner
diary/           YAML frontmatter diary annotations
groupfolder/     Group path resolution
mountsec/        Mount allowlist validation
runtime/         Docker binary abstraction, orphan cleanup
logger/          slog JSON init
template/        Instance seed files
sidecar/         MCP server binaries (whisper)
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
    → channel.Send
```

## Routing Rules

| Rule type | Match criteria                              |
| --------- | ------------------------------------------- |
| command   | message starts with trigger string          |
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

| Command      | Effect                                    |
| ------------ | ----------------------------------------- |
| `/new [msg]` | Clear session, optionally process message |
| `/ping`      | Status: group, session, active containers |
| `/chatid`    | Echo the chat JID                         |
| `/stop`      | Stop running container for this chat      |

## Instance Layout

`/srv/data/arizuko_<name>/`:

```
.env                    config (tokens, ports)
store/                  SQLite DB (messages.db)
groups/<folder>/        group files, logs, diary
data/ipc/<folder>/      IPC directories
data/sessions/<folder>/ agent session state
```

## Config

All via `.env` in data dir or env vars:

| Key                       | Purpose                      |
| ------------------------- | ---------------------------- |
| ASSISTANT_NAME            | instance name                |
| TELEGRAM_BOT_TOKEN        | enables telegram channel     |
| DISCORD_BOT_TOKEN         | enables discord channel      |
| CONTAINER_IMAGE           | agent docker image           |
| IDLE_TIMEOUT              | container idle shutdown (ms) |
| MAX_CONCURRENT_CONTAINERS | concurrent agent limit       |
| HOST_DATA_DIR             | docker-in-docker host path   |
| HOST_APP_DIR              | docker-in-docker app path    |
| MEDIA_ENABLED             | enable attachment pipeline   |
| WHISPER_BASE_URL          | whisper sidecar URL          |
| TIMEZONE                  | cron timezone (fallback UTC) |

Channels enabled by token presence.

## Development

```bash
make build    # go build → ./arizuko
make lint     # go vet ./...
make test     # go test ./... -count=1
make clean    # remove binary + tmp/
```

Pre-commit hooks configured via `.pre-commit-config.yaml`.
