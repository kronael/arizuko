# arizuko

Multitenant Claude agent router. Runs real Claude Code CLI inside Docker
containers. Routes messages from channel adapters (Telegram, Discord, Mastodon,
Bluesky, Reddit, WhatsApp, email, web) to per-group containerized agents,
streams responses back.

Go, SQLite, Docker.

## Quick Start

```bash
make build    # ./arizuko + daemon binaries
make image    # gateway docker image
make agent    # agent docker image

arizuko create foo                      # seed instance
vim /srv/data/arizuko_foo/.env          # configure
arizuko group foo add tg:-123456789     # register group
arizuko run foo                         # generate + docker compose up
```

## Group Management

```bash
arizuko group <instance> list                       # registered groups
arizuko group <instance> add <jid> <name> [folder]  # register + default route
arizuko group <instance> rm  <folder>               # unregister
arizuko group <instance> grant   <sub> <pattern>    # add user_groups ACL
arizuko group <instance> ungrant <sub> <pattern>    # remove grant
arizuko group <instance> grants  [<sub>]            # list grants
```

Pattern is a glob matched against folder paths: `**` = operator, `*` = any
root folder, `pub/*` = one segment under `pub/`, literal = exact.

First group defaults to folder `main` with direct mode; subsequent groups use
trigger mode (`@assistant_name`).

## Development

```bash
make build    # go build → ./arizuko
make lint     # go vet ./...
make test     # go test ./... -count=1
make clean
```

Pre-commit hooks via `.pre-commit-config.yaml`. See
[ARCHITECTURE.md](ARCHITECTURE.md) for package graph, schema, message flow.
See [MIGRATION.md](MIGRATION.md) for kanipi migration notes.

## Thanks

| Project                                                  | Author            | License     | Contribution                                        |
| -------------------------------------------------------- | ----------------- | ----------- | --------------------------------------------------- |
| [nanoclaw](https://github.com/qwibitai/nanoclaw)         | qwibitai          | MIT         | Container-per-session model                         |
| [kanipi](https://github.com/kronael/kanipi)              | kronael           | MIT         | TS proof-of-concept; routing, MCP IPC, skill system |
| [ElizaOS](https://github.com/elizaOS/eliza)              | elizaOS           | MIT         | character.json persona model, plugin thinking       |
| [Claude Code](https://github.com/anthropics/claude-code) | Anthropic         | Proprietary | The agent runtime                                   |
| [smolagents](https://github.com/huggingface/smolagents)  | Hugging Face      | Apache-2.0  | Code-as-action framing                              |
| [OpenClaw](https://github.com/openclaw/openclaw)         | Peter Steinberger | MIT         | Multi-channel binding, single-process gateway       |
| [NemoClaw](https://github.com/NVIDIA/NemoClaw)           | NVIDIA            | Apache-2.0  | Landlock + seccomp + netns sandboxing               |
| [Muaddib](https://github.com/pasky/muaddib)              | Petr Baudis       | MIT         | QEMU micro-VM isolation, 3-tier chronicle memory    |
| [Hermes](https://github.com/NousResearch/hermes-agent)   | Nous Research     | MIT         | Self-improving skill learning across sessions       |
| [takopi](https://github.com/banteg/takopi)               | banteg            | MIT         | Telegram dispatch, progress streaming               |

If you build on arizuko:

> Built on [arizuko](https://github.com/kronael/arizuko) by kronael. © 2026 kronael (MIT)

## License

[MIT](LICENSE).
