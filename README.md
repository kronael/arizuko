# arizuko

Multitenant Claude agent router. External channel adapters (Telegram,
Discord, Mastodon, Bluesky, Reddit, Email, WhatsApp, LinkedIn, web chat)
register with the router over HTTP; per-group Claude Code agents run in
Docker containers with an MCP socket bridged in for controlled side
effects. Go, SQLite (WAL), Docker.

## What it does

Routes inbound messages from any registered channel to the right agent
container, keyed by group folder. Each group owns its own session state,
diary, skills, and ACLs. Agents can spawn children, schedule cron tasks,
delegate to siblings, and act on behalf of users subject to grant rules.
One SQLite DB (`messages.db`) is the single source of truth; `gated`
owns the schema, other daemons connect read/write.

## Architecture at a glance

```
adapter (teled/discd/…) --HTTP--> gated (api + gateway)
                                    │
                                    ├── store (messages.db, WAL)
                                    ├── container (docker run agent)
                                    │     └── MCP over unix socket (ipc)
                                    └── chanreg (adapter health, outbound)

timed   — scheduler, writes messages
onbod   — onboarding, OAuth, gated admission
webd    — web chat channel adapter
proxyd  — auth-gated reverse proxy
dashd   — operator dashboards (read-only)
```

Full graph, message flow, container lifecycle, SQLite schema in
[ARCHITECTURE.md](ARCHITECTURE.md).

## Subsystems

arizuko has two flavors of feature: **core** primitives that define the
system shape and are always present, and **integrations** that plug into
the core and are picked per deployment. Daemon and library tables below
mark each row accordingly. See [ARCHITECTURE.md](ARCHITECTURE.md) for the
package graph and [EXTENDING.md](EXTENDING.md) for adding new integrations.

### Daemons

| name   | kind        | role                                                    | README                               |
| ------ | ----------- | ------------------------------------------------------- | ------------------------------------ |
| gated  | core        | HTTP API + message loop + container runner; owns schema | [gated/README.md](gated/README.md)   |
| timed  | core        | cron/interval scheduler                                 | [timed/README.md](timed/README.md)   |
| onbod  | core        | onboarding, OAuth, gated admission queue                | [onbod/README.md](onbod/README.md)   |
| dashd  | core        | read-only HTMX operator dashboards                      | [dashd/README.md](dashd/README.md)   |
| webd   | core        | web channel: SSE hub, slink chat + MCP transport        | [webd/README.md](webd/README.md)     |
| proxyd | core        | auth-gated reverse proxy                                | [proxyd/README.md](proxyd/README.md) |
| teled  | integration | Telegram adapter                                        | [teled/README.md](teled/README.md)   |
| discd  | integration | Discord adapter                                         | [discd/README.md](discd/README.md)   |
| mastd  | integration | Mastodon adapter                                        | [mastd/README.md](mastd/README.md)   |
| bskyd  | integration | Bluesky adapter                                         | [bskyd/README.md](bskyd/README.md)   |
| reditd | integration | Reddit adapter                                          | [reditd/README.md](reditd/README.md) |
| emaid  | integration | Email (IMAP/SMTP) adapter                               | [emaid/README.md](emaid/README.md)   |
| whapd  | integration | WhatsApp adapter (TypeScript, Baileys)                  | [whapd/README.md](whapd/README.md)   |
| twitd  | integration | X/Twitter adapter (TypeScript, browser emulation)       | [twitd/README.md](twitd/README.md)   |
| linkd  | integration | LinkedIn adapter (stub)                                 | [linkd/README.md](linkd/README.md)   |
| ttsd   | integration | OpenAI-compatible TTS proxy (Kokoro by default)         | [ttsd/README.md](ttsd/README.md)     |

A minimal deployment runs core + one channel-adapter integration; a
maxed-out deployment runs all of them. Optional capability hooks
(Whisper transcription via `WHISPER_BASE_URL`, TTS via `ttsd` +
`TTS_BASE_URL`, planned oracle skill) plug into the core via env
vars and skills, not new daemons.

### Libraries

All libraries are core unless marked otherwise.

| name        | kind        | role                                                                          | README                                         |
| ----------- | ----------- | ----------------------------------------------------------------------------- | ---------------------------------------------- |
| cmd/arizuko | core        | CLI entrypoint (`create`, `run`, `group`, `gate`, `invite`, `chat`, `status`) | [cmd/arizuko/README.md](cmd/arizuko/README.md) |
| gateway     | core        | poll loop, routing, autocalls, impulse gate                                   | [gateway/README.md](gateway/README.md)         |
| core        | core        | types, config, `Channel` interface                                            | [core/README.md](core/README.md)               |
| store       | core        | SQLite access + migrations                                                    | [store/README.md](store/README.md)             |
| api         | core        | router-side HTTP API                                                          | [api/README.md](api/README.md)                 |
| chanreg     | core        | channel registry + `HTTPChannel`                                              | [chanreg/README.md](chanreg/README.md)         |
| chanlib     | core        | shared HTTP/auth primitives for adapters                                      | [chanlib/README.md](chanlib/README.md)         |
| router      | core        | message formatting, route evaluation                                          | [router/README.md](router/README.md)           |
| queue       | core        | per-group concurrency + circuit breaker                                       | [queue/README.md](queue/README.md)             |
| container   | core        | docker runner + skill seeding                                                 | [container/README.md](container/README.md)     |
| compose     | core        | `docker-compose.yml` generator                                                | [compose/README.md](compose/README.md)         |
| ipc         | core        | MCP server on unix socket                                                     | [ipc/README.md](ipc/README.md)                 |
| auth        | core        | identity, JWT, OAuth, policy, HMAC                                            | [auth/README.md](auth/README.md)               |
| grants      | core        | grant rule engine                                                             | [grants/README.md](grants/README.md)           |
| diary       | core        | diary reader for prompt injection                                             | [diary/README.md](diary/README.md)             |
| db_utils    | core        | embedded-FS migration runner                                                  | [db_utils/README.md](db_utils/README.md)       |
| theme       | core        | shared CSS/HTML helpers                                                       | [theme/README.md](theme/README.md)             |
| groupfolder | core        | group-folder path validation                                                  | [groupfolder/README.md](groupfolder/README.md) |
| mountsec    | core        | mount allowlist + path validation                                             | [mountsec/README.md](mountsec/README.md)       |
| template    | core        | instance seed files + adapter TOMLs                                           | [template/README.md](template/README.md)       |
| sidecar     | integration | whisper transcription image                                                   | [sidecar/README.md](sidecar/README.md)         |

The `ant/` directory (in-container agent, TypeScript) has its own
layered docs and is not indexed here.

### Components (orthogonal siblings)

Shippable separately, usable outside arizuko. No imports of
arizuko-internal packages. From arizuko's perspective these are
**integrations** — opted in per deployment (e.g. `EGRESS_ISOLATION=true`
pulls in crackbox); from their own perspective they are standalone
binaries. See [`specs/8/b-orthogonal-components.md`](specs/8/b-orthogonal-components.md).

| name     | kind        | role                                                                                                                          | README                                   |
| -------- | ----------- | ----------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------- |
| crackbox | integration | umbrella: `egred` proxy daemon (shipped) + `pkg/host/` KVM lib (shipped, see [specs/6/12](specs/6/12-crackbox-sandboxing.md)) | [crackbox/README.md](crackbox/README.md) |

## Features

| feature                                 | code                                                       | spec                                                             |
| --------------------------------------- | ---------------------------------------------------------- | ---------------------------------------------------------------- |
| multitenant routing + delegation        | [gateway/](gateway/), [router/](router/)                   | [ROUTING.md](ROUTING.md)                                         |
| MCP tooling (action + inspect families) | [ipc/ipc.go](ipc/ipc.go), [ipc/inspect.go](ipc/inspect.go) | [specs/5/30-inspect-tools.md](specs/5/30-inspect-tools.md)       |
| channel adapters (HTTP protocol)        | [chanlib/](chanlib/), `<adapter>/`                         | [specs/4/1-channel-protocol.md](specs/4/1-channel-protocol.md)   |
| web auth + onboarding (OAuth, gated)    | [proxyd/](proxyd/), [onbod/](onbod/)                       | [specs/5/28-mass-onboarding.md](specs/5/28-mass-onboarding.md)   |
| scheduler (cron + interval)             | [timed/main.go](timed/main.go)                             | [specs/4/8-scheduler-service.md](specs/4/8-scheduler-service.md) |
| containerized agents (per-group, MCP)   | [container/](container/), [ant/](ant/)                     | [ARCHITECTURE.md](ARCHITECTURE.md)                               |

Full feature history in [CHANGELOG.md](CHANGELOG.md); current spec status
in [specs/index.md](specs/index.md).

## Docs

- [ARCHITECTURE.md](ARCHITECTURE.md) — package graph, message flow, schema
- [ROUTING.md](ROUTING.md) — routing rules and examples
- [SECURITY.md](SECURITY.md) — threat model
- [EXTENDING.md](EXTENDING.md) — add channels, tools, skills, autocalls
- [CHANGELOG.md](CHANGELOG.md) — shipped changes
- [ROADMAP.md](ROADMAP.md) — planned work
- [MIGRATION.md](MIGRATION.md) — kanipi → arizuko
- [specs/](specs/) — per-phase specifications
- [CLAUDE.md](CLAUDE.md) — project-specific patterns, env vars

## Build & run

```bash
make build           # ./arizuko + daemon binaries
make test            # go test ./... -count=1
make images          # all docker images
make agent           # agent image only

arizuko create foo                         # seed /srv/data/arizuko_foo
vim /srv/data/arizuko_foo/.env             # configure
arizuko group foo add tg:-123456789 main   # register first group
arizuko run foo                            # generate compose + up
```

Env vars, data dir layout, and the full toolchain sit in
[CLAUDE.md](CLAUDE.md).

## Thanks

| Project                                                  | Author            | License     | Contribution                                        |
| -------------------------------------------------------- | ----------------- | ----------- | --------------------------------------------------- |
| [nanoclaw](https://github.com/qwibitai/nanoclaw)         | qwibitai          | MIT         | Container-per-session model                         |
| [kanipi](https://github.com/kronael/kanipi)              | kronael           | MIT         | TS proof-of-concept; routing, MCP IPC, skill system |
| [ElizaOS](https://github.com/elizaOS/eliza)              | elizaOS           | MIT         | character.json persona model                        |
| [Claude Code](https://github.com/anthropics/claude-code) | Anthropic         | Proprietary | The agent runtime                                   |
| [smolagents](https://github.com/huggingface/smolagents)  | Hugging Face      | Apache-2.0  | Code-as-action framing                              |
| [OpenClaw](https://github.com/openclaw/openclaw)         | Peter Steinberger | MIT         | Multi-channel binding                               |
| [NemoClaw](https://github.com/NVIDIA/NemoClaw)           | NVIDIA            | Apache-2.0  | Landlock + seccomp + netns sandboxing               |
| [Muaddib](https://github.com/pasky/muaddib)              | Petr Baudis       | MIT         | QEMU micro-VM isolation, 3-tier chronicle memory    |
| [Hermes](https://github.com/NousResearch/hermes-agent)   | Nous Research     | MIT         | Self-improving skill learning across sessions       |
| [takopi](https://github.com/banteg/takopi)               | banteg            | MIT         | Telegram dispatch, progress streaming               |

## License

[MIT](LICENSE).
