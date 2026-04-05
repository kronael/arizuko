# arizuko

Multitenant Claude agent router. Runs real Claude Code CLI inside Docker
containers — not an SDK wrapper. Routes messages from channel adapters
(Telegram, Discord, Mastodon, Bluesky, Reddit, WhatsApp, email, web) to
per-group containerized agents, streams responses back.

Go, SQLite, Docker.

## Why arizuko

- **Runs real Claude Code** — agents are Claude Code CLI in Docker. Full
  tool use, subagents, MCP, hooks. Not a thin SDK wrapper with a subset of
  capabilities.
- **Channel-first** — adapters are independent daemons connecting over HTTP.
  Add or swap a channel without touching the core.
- **Multitenant by design** — one gateway, many groups, many channels.
  Per-group agent containers, file workspaces, and MCP sidecars.
- **Skills survive compaction** — agent skills and diary entries persist
  across context resets and container restarts via `container/skills/`.
- **Boring stack** — Go + SQLite (WAL mode) + Docker. No framework, no ORM,
  no message queue. Schema is the contract.

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
arizuko group <instance> list                       # registered groups (folder\tname)
arizuko group <instance> add <jid> <name> [folder]  # register group + default route
arizuko group <instance> rm  <folder>               # unregister by folder
```

First group defaults to folder `main` with direct mode.
Subsequent groups use trigger mode (`@assistant_name`).

## Channels

Channel adapters are standalone daemons that register over HTTP:

- `teled` — Telegram (Go)
- `discd` — Discord (Go)
- `mastd` — Mastodon WebSocket + REST (Go)
- `bskyd` — Bluesky AT Protocol polling (Go)
- `reditd` — Reddit OAuth2 inbox/subreddit polling (Go)
- `emaid` — Email IMAP/SMTP (Go)
- `whapd` — WhatsApp (TypeScript)
- `proxyd` — Web chat via SSE + slink token auth (Go)

All Go adapters share `chanlib/` for router client, auth middleware, and inbound message types.

## Agent capabilities

Each group agent runs inside a Docker container with persistent file workspaces. Capabilities available to agents via MCP tools and skills:

**Diary and memory** — two-layer system: `MEMORY.md` for long-term preferences and patterns (under 200 lines), daily diary entries in `diary/YYYYMMDD.md` for work log. Diary summaries from the last 14 days are injected into each new session as `<knowledge layer="diary">`. Episodes (compressed session transcripts) are injected alongside. See `specs/1/L-memory-diary.md`.

**Knowledge retrieval** — `/recall` searches across knowledge stores (facts, diary, users, episodes) using LLM semantic grep over `summary:` frontmatter. Agent-initiated, read-only. See `specs/4/24-recall.md`.

**Task scheduling** — `timed` daemon polls `scheduled_tasks` every 60s and inserts due tasks as messages into the shared DB. Agents create tasks via the `manage_tasks` MCP tool. Schedules support cron expressions, intervals, and one-shot runs.

**Operator dashboard** — `dashd` serves a read-only HTMX portal at `/dash/` with six views: portal (tile grid), status (channels, groups, containers, queue, errors), tasks (scheduled tasks with run history), activity (message flow, routing table), groups (hierarchy tree), and memory (per-group knowledge browser). Auto-refresh via HTMX polling. See `specs/7/25-dashboards.md`.

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
| [kanipi](https://github.com/onvos/kanipi)                | onvos             | MIT         | © 2026 REDACTED                         | TypeScript proof-of-concept this was rewritten from; routing model, MCP IPC design, skill system |
| [ElizaOS](https://github.com/elizaOS/eliza)              | elizaOS           | MIT         | © 2026 Shaw Walters and elizaOS Contributors | character.json agent persona model, plugin ecosystem thinking                                    |
| [Claude Code](https://github.com/anthropics/claude-code) | Anthropic         | Proprietary | © Anthropic PBC                              | The agent runtime everything runs on — tools, subagents, MCP, skills                             |
| [smolagents](https://github.com/huggingface/smolagents)  | Hugging Face      | Apache-2.0  | © Hugging Face                               | Code-as-action framing; thinking about what the minimal agent loop looks like                    |
| [OpenClaw](https://github.com/openclaw/openclaw)         | Peter Steinberger | MIT         | © 2025 Peter Steinberger                     | Multi-channel binding architecture, single-process gateway design                                |
| [NemoClaw](https://github.com/NVIDIA/NemoClaw)           | NVIDIA            | Apache-2.0  | © NVIDIA Corporation                         | Landlock + seccomp + netns sandboxing model for agent containers                                 |
| [Muaddib](https://github.com/pasky/muaddib)              | Petr Baudis       | MIT         | © 2025 Muaddib contributors                  | QEMU micro-VM isolation, 3-tier chronicle memory design                                          |
| [Hermes](https://github.com/NousResearch/hermes-agent)   | Nous Research     | MIT         | © 2025 Nous Research                         | Self-improving skill learning across sessions                                                    |
| [takopi](https://github.com/banteg/takopi)               | banteg            | MIT         | © 2025 banteg                                | Telegram→agent dispatch, live progress streaming, the agents page at REDACTED/agents        |

If you build on arizuko, this is all we ask:

> Built on [arizuko](https://github.com/onvos/arizuko) by onvos. © 2026 REDACTED (MIT)

## License

[MIT](LICENSE) — do whatever you want, keep the copyright notice.

## Migrating from kanipi

See [MIGRATION.md](MIGRATION.md) for a concise guide covering non-obvious
differences: IPC transport (files → MCP/unix socket), schema changes, auth
password hashing, env var renames, and feature gaps.
