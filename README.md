# arizuko

<p align="center">
  Run persistent AI agents for teams. Each folder is an agent with its own memory, persona, and ACL.
</p>

<p align="center">
  <a href="#run-agents-in-real-channels">Use cases</a> •
  <a href="#overview">Overview</a> •
  <a href="#when-to-use-arizuko">When to use</a> •
  <a href="#getting-started">Getting Started</a> •
  <a href="#security-model">Security</a> •
  <a href="#docs">Docs</a>
</p>

## Run agents in real channels

- **Team agent in Slack / Discord / Telegram** — mention-based or DM, with per-channel persona and memory
- **Long-running personal assistant** — persists conversation history, diary, and user profile across restarts
- **Multi-channel orchestrator** — one agent across Slack + Discord + email + WhatsApp + web chat, same database
- **Scheduler / cron-bot** — `timed` injects turns into the message bus on a schedule, no webhook needed
- **Email or webhook agent** — `emaid` ingests IMAP with DMARC filtering; arbitrary callers POST to `/hook/<token>`
- **RAG over team docs** — mount your repo via WebDAV (`davd`) and let the agent grep, read, and cite; `find_messages` adds FTS5 full-text search over conversation history
- **Multi-tenant agent platform** — one deployment, arbitrary folder depth; `corp/eng/sre` and `solo/inbox` run the same code

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

Shared state lives in one SQLite database (messages, routing, grants); per-group agent state — the Claude Code session, skills, memory, diary — lives in the mounted group folder. Containers are ephemeral: one spawns per turn, mounts the group folder, runs, and exits.

## Direction

That's what arizuko is today: a single-host, multi-tenant agent router with channels, folders, and a shared SQLite. Three frames describe where it's heading.

**Multi-tenant by primitive.** The same code runs `solo/inbox` and `corp/eng/sre/oncall`. Folder hierarchies have no fixed depth. Every primitive — grants, channels, secrets, routes, scheduled work — scales from one-user-one-channel to a fleet of agents under shared admin. Adding tenants is adding rows + folders; the daemon graph is invariant.

**Agent-as-data.** Each agent is a folder of values — `PERSONA.md`, `skills/`, `MEMORY.md`, `.diary/`, ACL rules, route rows, secret references. The runtime is an interpreter over those values. The plan is to move cold-tier config (ACL, routes, persona, skills, scheduled tasks, invites) toward git as the source of truth ([specs/7/3](specs/7/3-git-as-truth.md)), with SQLite as a rebuildable cache. Forking, auditing, and distributing an agent then ride native git verbs instead of bespoke ones.

**Agent-first managed (target state).** The agent and the operator will speak the same language. The plan ([specs/5/5](specs/5/5-uniform-mcp-rest.md)) is one hand-rolled handler per cold-tier resource with two faces — REST for humans + external tools, MCP for in-container agents — over one auth gate (`auth.Authorize`) and one tx-bound audit row. The first resource (`proxyd/resource.go`) already runs that pattern; the rest follow incrementally. Declarative intent is then carried by YAML manifests dispatched through the same gate ([specs/7/5](specs/7/5-yaml-manifests.md)): operator writes the YAML, `arizuko apply` walks it row by row, daemons see resreg-shaped mutations identical to any other call.

Nothing in this direction breaks what already runs. The migration is incremental: resource by resource, daemon by daemon, the surface unifies, the cold tier moves to git, the manifest format absorbs the imperative knobs. Containers, channel adapters, the message bus, the per-folder runtime — all unchanged.

## When to use arizuko

Use arizuko if the agent needs to live in real channels and keep a separate identity per team, customer, or workflow.

Choose arizuko when you need:

- one agent per folder, with separate persona, memory, skills, and ACL
- channel-native operation in Slack, Discord, Telegram, email, web chat, or webhooks
- scheduled work, delegation, and long-running background behavior
- self-hosting on one machine with Docker and SQLite
- a system you can inspect as files, routes, and database rows

Do not choose arizuko if your main problem is enterprise document search — it has no connector sync pipeline or vector index. Pair it with a retrieval system and use arizuko as the agent layer that acts on the results (see [What arizuko does not include](#what-arizuko-does-not-include)).

If you want a local coding assistant for one developer machine, a single-user tool (brainpro, Hermes) is a closer fit. arizuko is built for persistent agents that sit behind channels and serve teams.

## Getting Started

**You need:** Docker, a Linux host, credentials for at least one channel adapter, and write access to `/srv/data/`.

```bash
make build                                 # ./arizuko + daemon binaries
arizuko create foo                         # seed /srv/data/arizuko_foo + .env
vim /srv/data/arizuko_foo/.env             # set CHANNEL_SECRET, AUTH_SECRET, WEB_HOST, …
arizuko group foo add tg:-123456789 main   # register first group
arizuko run foo                            # generate compose + docker compose up
```

A first deployment runs `gated` + one adapter (`teled`, `slakd`, `discd`, `webd`, or `emaid`). Add `dashd` for operator UI, `timed` for scheduled tasks, `onbod` for invite flows, `crackbox` for default-deny egress. Each adapter ships as a `template/services/<name>.toml` — no Go edits required. See [EXTENDING.md](EXTENDING.md) for wiring new channels.

A tar of `/srv/data/arizuko_<name>/` is a complete instance backup — `messages.db` (WAL), group folders, per-user memory, secrets, agent files.

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

Optional capability hooks: Whisper transcription (`WHISPER_BASE_URL`), TTS (`ttsd` + `TTS_BASE_URL`), second LLM (`OPENAI_API_KEY`/`CODEX_API_KEY` in folder secrets).

Full daemon and library tables in [ARCHITECTURE.md](ARCHITECTURE.md).

## What arizuko does not include

- connector crawlers for Confluence, Notion, Google Drive, Jira, or Salesforce
- a built-in embedding pipeline or vector database
- permission sync from external systems into per-document ACLs
- multi-model routing across providers (the agent runtime is Claude Code)
- a managed control plane or hosted SaaS path

Documents can be mounted or written into a group's workspace; agents can read them directly. For large-scale retrieval, pair arizuko with a separate retrieval stack and use it as the part that receives messages, keeps per-group memory, enforces grants, schedules tasks, and takes actions across channels.

## Security model

- **Container isolation**: each group runs in a separate Docker container on a separate network. Sibling groups never share a context window.
- **Egress isolation**: `crackbox` (`egred` proxy daemon) enforces default-deny on agent outbound traffic. Agents receive proxy tokens; real credentials are swapped at the boundary.
- **ACL**: `auth.Authorize` — one `acl` table, deny-wins, tier defaults in code. MCP tools gated per-action per-principal.
- **Secret injection**: folder secrets are AES-256-GCM encrypted at rest; injected into the container at spawn time, never written to disk in plaintext.
- **Signed requests**: `proxyd` signs identity headers; every backend verifies via `auth/middleware.go`. Unsigned `X-User-Sub` headers are stripped.

Full threat model in [SECURITY.md](SECURITY.md).

## What's planned

- Proactive interjection — lurk-mode + validator chain ([spec](specs/5/33-proactive-interjection.md))
- Capability-token auth — the `auth/` library is shipped (offline JWT verify, OAuth, ACL, middleware); per-tenant token minting + revocation and `PROXYD_HMAC_SECRET` / `CHANNEL_SECRET` retirement are the target ([spec](specs/5/1-auth-standalone.md))
- Daemon genericization — `gated` split into `routd` / `runed` / `mcpd`; capability scopes replace folder-depth tiers ([spec](specs/5/U-genericization.md))
- Uniform MCP+REST across the cold tier — one hand-rolled handler per resource, both faces ([spec](specs/5/5-uniform-mcp-rest.md))
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
