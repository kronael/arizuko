# arizuko

Multitenant Claude agent gateway. Polls messaging channels,
routes to containerized Claude agents via docker, streams
responses back. Go, SQLite, Docker.

**蟻塚女** — _mistress of the ant hill._

## Quick Start

```bash
make build    # go build → ./arizuko binary
make image    # gateway docker image
make agent    # agent docker image

arizuko create foo                      # seed instance
vim /srv/data/arizuko_foo/.env          # configure
arizuko group foo add tg:-123456789     # register group
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

## Development

```bash
make build    # go build → ./arizuko
make lint     # go vet ./...
make test     # go test ./... -count=1
make clean    # remove binary + tmp/
```

Pre-commit hooks configured via `.pre-commit-config.yaml`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for package graph, schema, message flow, and config.

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

## License

[MIT](LICENSE) — do whatever you want, keep the copyright notice.

## Migrating from kanipi

See [MIGRATION.md](MIGRATION.md) for a concise guide covering non-obvious
differences: IPC transport (files → MCP/unix socket), schema changes, auth
password hashing, env var renames, and feature gaps.
