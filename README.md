# arizuko

<p align="center">
  Persistent AI agents for teams. Each folder is an agent with its own memory, persona, and ACL.
</p>

<p align="center">
  <a href="#what-its-good-for">Use cases</a> •
  <a href="#overview">Overview</a> •
  <a href="#getting-started">Getting Started</a> •
  <a href="#security-model">Security</a> •
  <a href="#docs">Docs</a>
</p>

## What it's good for

- **Team agent in Slack / Discord / Telegram** — mention-based or DM, with per-channel persona and memory
- **Long-running personal assistant** — persists conversation history, diary, and user profile across restarts
- **Multi-channel orchestrator** — one agent across Slack + Discord + email + WhatsApp + web chat, same database
- **Scheduler / cron-bot** — `timed` injects turns into the message bus on a schedule, no webhook needed
- **Email or webhook agent** — `emaid` ingests IMAP with DMARC filtering; arbitrary callers POST to `/hook/<token>`
- **RAG over team docs** — mount your repo via WebDAV (`davd`) and let the agent grep, read, and cite
- **Multi-tenant agent platform** — one deployment, arbitrary folder depth; `corp/eng/sre` and `solo/inbox` run the same code

A tar of `/srv/data/arizuko_<name>/` is a complete instance backup.

## Overview

A folder is an agent. It has a `PERSONA.md`, a `skills/` directory, a `MEMORY.md`, a conversation diary, and an ACL. Folders form a hierarchy (`corp/sales`, `corp/eng/sre`) — each node is an independent agent that accumulates only the conversations relevant to it.

```
# A message arrives in Slack.
@andy can you summarize the open PRs?

# 1. slakd posts to gated  →  messages.db
# 2. gated polls DB, resolves folder via route table
# 3. Docker container spawns for that group
# 4. MCP unix socket bridges into container
# 5. Claude Code agent runs, calls tools, submits turn
# 6. gated delivers reply back to Slack via slakd
```

Agents coordinate through the same message bus they serve users on. A container can route to a sibling, delegate to a child, schedule a cron task, or ingest webhooks — by writing rows to `messages.db` and calling `EnqueueMessageCheck`. No separate coordination bus.

State lives entirely in one SQLite database. Containers are stateless — they mount the group folder, run, and exit.

## Getting Started

```bash
make build                                 # ./arizuko + daemon binaries
arizuko create foo                         # seed /srv/data/arizuko_foo + .env
vim /srv/data/arizuko_foo/.env             # set CHANNEL_SECRET, AUTH_SECRET, WEB_HOST, …
arizuko group foo add tg:-123456789 main   # register first group
arizuko run foo                            # generate compose + docker compose up
```

Then add a channel adapter. Each adapter is one `[[service]]` block in the compose. See [CLAUDE.md](CLAUDE.md) for env vars and [EXTENDING.md](EXTENDING.md) for wiring new channels.

## How it works

```
adapter (teled/discd/slakd/…) --HTTP--> gated (api + gateway)
                                          │
                                          ├── store (messages.db, WAL)
                                          ├── container (docker run agent)
                                          │     └── MCP over unix socket (ipc)
                                          └── chanreg (adapter health, outbound)

timed   — scheduler, writes messages into bus
onbod   — onboarding, OAuth, gated admission
webd    — web chat channel adapter + SSE hub
proxyd  — auth-gated reverse proxy (TOML route table)
vited   — serves /pub/* + auth-gated default route
davd    — WebDAV workspace (per-group, dufs)
dashd   — operator dashboards + admin CRUD
```

Full package graph, message flow, container lifecycle, and SQLite schema in [ARCHITECTURE.md](ARCHITECTURE.md).

## Channel adapters

| adapter | platform           |
| ------- | ------------------ |
| teled   | Telegram           |
| discd   | Discord            |
| slakd   | Slack (Events API) |
| mastd   | Mastodon           |
| bskyd   | Bluesky            |
| reditd  | Reddit             |
| emaid   | Email (IMAP/SMTP)  |
| whapd   | WhatsApp (Baileys) |
| twitd   | X/Twitter          |
| linkd   | LinkedIn           |

A minimal deployment runs `gated` + one adapter. Optional capability hooks: Whisper transcription (`WHISPER_BASE_URL`), TTS (`ttsd` + `TTS_BASE_URL`), second LLM (`OPENAI_API_KEY`/`CODEX_API_KEY` in folder secrets).

Full daemon and library tables in [ARCHITECTURE.md](ARCHITECTURE.md).

## Security model

- **Container isolation**: each group runs in a separate Docker container on a separate network. Sibling groups never share a context window.
- **Egress isolation**: `crackbox` (`egred` proxy daemon) enforces default-deny on agent outbound traffic. Agents receive proxy tokens; real credentials are swapped at the boundary.
- **ACL**: `auth.Authorize` — one `acl` table, deny-wins, tier defaults in code. MCP tools gated per-action per-principal.
- **Secret injection**: folder secrets are AES-256-GCM encrypted at rest; injected into the container at spawn time, never written to disk in plaintext.
- **Signed requests**: `proxyd` signs identity headers; every backend verifies via `auth/middleware.go`. Unsigned `X-User-Sub` headers are stripped.

Full threat model in [SECURITY.md](SECURITY.md).

## What's planned

- Proactive interjection — lurk-mode + validator chain ([spec](specs/5/33-proactive-interjection.md))
- Message actions — agent-side edit, delete, pin ([spec](specs/5/Z-message-actions.md))
- Platform API — federated `/v1/*` surface across daemons ([spec](specs/5/V-platform-api.md))
- End-user agent provisioning — POST a definition, get a tenant + chat token ([spec](specs/5/3-user-spawned-agents.md))

## Build & test

```bash
make build           # go build → ./arizuko + daemon binaries
make test            # go test ./... -count=1 -short
make images          # all docker images
make agent           # agent image only (ant/)
make smoke           # post-deploy health check (SMOKE_INSTANCE=krons)
```

## Docs

- [ARCHITECTURE.md](ARCHITECTURE.md) — package graph, message flow, full daemon/library tables, schema
- [ROUTING.md](ROUTING.md) — route table syntax and examples
- [SECURITY.md](SECURITY.md) — full threat model, egress isolation, secrets boundaries
- [EXTENDING.md](EXTENDING.md) — add channels, skills, autocalls, connectors, actions
- [GRANTS.md](GRANTS.md) — ACL model, principal namespaces, action lattice
- [CHANGELOG.md](CHANGELOG.md) — shipped changes
- [specs/](specs/) — per-phase specifications

## Thanks

| Project                                                  | Author        | Contribution                                        |
| -------------------------------------------------------- | ------------- | --------------------------------------------------- |
| [nanoclaw](https://github.com/qwibitai/nanoclaw)         | qwibitai      | Container-per-session model                         |
| [kanipi](https://github.com/kronael/kanipi)              | kronael       | TS proof-of-concept; routing, MCP IPC, skill system |
| [ElizaOS](https://github.com/elizaOS/eliza)              | elizaOS       | character.json persona model                        |
| [Claude Code](https://github.com/anthropics/claude-code) | Anthropic     | The agent runtime                                   |
| [smolagents](https://github.com/huggingface/smolagents)  | Hugging Face  | Code-as-action framing                              |
| [Muaddib](https://github.com/pasky/muaddib)              | Petr Baudis   | QEMU micro-VM isolation, 3-tier chronicle memory    |
| [Hermes](https://github.com/NousResearch/hermes-agent)   | Nous Research | Self-improving skill learning across sessions       |
| [takopi](https://github.com/banteg/takopi)               | banteg        | Telegram dispatch, progress streaming               |

## License

[MIT](LICENSE).
