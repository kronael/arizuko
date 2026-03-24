# arizuko

Multitenant Claude agent gateway. Polls messaging channels,
routes to containerized Claude agents via docker, streams
responses back. Go, SQLite, Docker.

**蟻塚女** — _mistress of the ant hill._ Arizuko organizes containerized
Claude agents: routing messages to the right agent, enforcing permissions,
scheduling tasks, maintaining memory. Each agent operates in isolation —
focused on its work, persistent across sessions. No agent needs to
understand the whole system.

## Philosophy

Arizuko is infrastructure, not a product. The goal is a hard, tested, boring
core — like a kernel — that you can build on top of without surprises. The
adapters, agents, skills, and routing rules are yours to write, fork, and share.

The design is intentionally porous: channel adapters are external processes,
skills are files in a folder, MCP sidecars are independent binaries. Every
boundary is a seam where you can cut in and replace a part without touching the
rest. This is the bazaar model — the core holds, everything around it is yours.

Agents running on arizuko are first-class participants in this: they can read
their own skills, propose modifications, and ship changes through the same
channels humans use. The system is designed to be understood and modified by
whoever — or whatever — is running it.

## Quick Start

```bash
make build    # go build → ./arizuko binary
make image    # gateway docker image
make agent    # agent docker image

arizuko create foo                      # seed instance
vim /srv/data/arizuko_foo/.env          # configure
arizuko group foo add tg:-123456789     # register group
arizuko generate foo                    # write docker-compose.yml (no docker needed)
arizuko run foo                         # generate + docker compose up
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
cmd/arizuko/      CLI entrypoint (generate, run, create, group, status)
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
chanlib/          Shared HTTP + auth primitives for channel adapters
grants/           Grant rule engine (library)
notify/           Operator notification fan-out (library)
gated/            Gateway daemon (Go)
timed/            Scheduler daemon (Go)
onbod/            Onboarding daemon (Go)
dashd/            Operator dashboards (Go)
proxyd/           Web proxy (auth gate, /dash/, /auth/, Vite)
teled/            Telegram adapter (Go)
discd/            Discord adapter (Go)
mastd/            Mastodon adapter (Go)
bskyd/            Bluesky adapter (Go)
reditd/           Reddit adapter (Go)
emaid/            Email adapter (Go, IMAP/SMTP)
whapd/            WhatsApp adapter (TypeScript)
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

## Thanks

Built on the shoulders of people doing serious work in this space.

| Project                                                  | Author            | License     | Copyright                                    | What it contributed                                                                              |
| -------------------------------------------------------- | ----------------- | ----------- | -------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| [nanoclaw](https://github.com/qwibitai/nanoclaw)         | qwibitai          | MIT         | © 2026 Gavriel                               | Direct ancestor — container-per-session model, the original shape of this system                 |
| [kanipi](https://github.com/onvos/kanipi)                | onvos             | MIT         | © 2026 onvos                                 | TypeScript proof-of-concept this was rewritten from; routing model, MCP IPC design, skill system |
| [ElizaOS](https://github.com/elizaOS/eliza)              | elizaOS           | MIT         | © 2026 Shaw Walters and elizaOS Contributors | character.json agent persona model, plugin ecosystem thinking                                    |
| [Claude Code](https://github.com/anthropics/claude-code) | Anthropic         | Proprietary | © Anthropic PBC                              | The agent runtime everything runs on — tools, subagents, MCP, skills                             |
| [smolagents](https://github.com/huggingface/smolagents)  | Hugging Face      | Apache-2.0  | © Hugging Face                               | Code-as-action framing; thinking about what the minimal agent loop looks like                    |
| [OpenClaw](https://github.com/openclaw/openclaw)         | Peter Steinberger | MIT         | © 2025 Peter Steinberger                     | Multi-channel binding architecture, single-process gateway design                                |
| [NemoClaw](https://github.com/NVIDIA/NemoClaw)           | NVIDIA            | Apache-2.0  | © NVIDIA Corporation                         | Landlock + seccomp + netns sandboxing model for agent containers                                 |
| [Muaddib](https://github.com/pasky/muaddib)              | Petr Baudis       | MIT         | © 2025 Muaddib contributors                  | QEMU micro-VM isolation, 3-tier chronicle memory design                                          |
| [Hermes](https://github.com/NousResearch/hermes-agent)   | Nous Research     | MIT         | © 2025 Nous Research                         | Self-improving skill learning across sessions                                                    |
| [takopi](https://github.com/banteg/takopi)               | banteg            | MIT         | © 2025 banteg                                | Telegram→agent dispatch, live progress streaming, the agents page at REDACTED/agents        |

If you build on arizuko, this is all we ask:

> Built on [arizuko](https://github.com/onvos/arizuko) by onvos. © 2026 onvos (MIT)

We think this is what attribution should look like: author, license, copyright,
and what the work actually contributed.

## License

[MIT](LICENSE) — do whatever you want, keep the copyright notice.

## Migrating from kanipi

See [MIGRATION.md](MIGRATION.md) for a concise guide covering non-obvious
differences: IPC transport (files → MCP/unix socket), schema changes, auth
password hashing, env var renames, and feature gaps.
