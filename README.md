# arizuko

<p align="center">
  arizuko hosts many specialized agents on one platform — each a folder (persona, memory, skills, routing) you shape by talking to it: an LLM edits the files that are the agent, and compartmentalized daemons keep that shaping bounded to your permissions. Every change lands as plain files on your own box — diff them, tar the directory for a complete backup, put them under git if you want history. What you ship is products you own — a Slack team agent, an AWS SRE, a company brain.
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

Models and agent harnesses keep improving — that isn't the race arizuko runs. The gap is the layer above them: managing users, context, and organization across many agents, built as composable primitives you own rather than one platform you rent — a web-native Linux, where publishing a page is a file write, not a deploy. See [`specs/5/A`](specs/5/A-primitives-framing.md) for the framing.

```
# A message arrives in Slack.
@andy can you summarize the open PRs?

# 1. slakd posts to routd  →  routd.db
# 2. routd resolves folder via route table, builds the prompt, calls runed
# 3. runed spawns a Docker container for that group
# 4. routd hosts the agent's MCP socket in-process; socat bridges it in
# 5. Claude Code agent runs, calls tools, submits turn
# 6. routd delivers reply back to Slack via slakd
```

Agents coordinate through the same conversation plane they serve users on. A container can route to a sibling, delegate to a child, schedule a cron task, or ingest webhooks — by posting to `routd`. No separate coordination bus.

Conversation state lives in `routd.db`; per-group agent state — the Claude Code session, skills, memory, diary — lives in the mounted group folder. Containers are ephemeral: `runed` spawns one per turn, mounts the group folder, runs it, and tears it down.

## The shape

Everything reduces to six primitives in a fixed pipeline. Trace one event top to bottom:

```
Slack @mention in #eng
  → Event          one inbox row (routd.db)
  → Routing        → corp/eng/oncall
  → Agent          folder loaded: persona, skills, memory
  → Authorization  may it read / send / delegate here?
  → Turn           one ephemeral container run (runed)
  → State          DB rows written + folder edits
  → reply in the Slack thread
```

It looks like feature sprawl until you trace one example — then it's six steps every time. Channels, tasks, webhooks, secrets, delegation, workflows are all recompositions of these six (plus identity as the coordinate system they're addressed in), never new machinery ([specs/5/A](specs/5/A-primitives-framing.md)).

Those primitives stack into what you deploy and ship:

| Layer      | What it is           | Examples                                                           |
| ---------- | -------------------- | ------------------------------------------------------------------ |
| Primitives | invariant concepts   | Event, Routing, Agent, Authorization, Turn, State (+ Identity)     |
| Components | Go packages          | `store`, `router`, `auth`, `grants`, `ipc`, `runed`, `groupfolder` |
| Daemons    | deployable processes | `authd` + `routd` + `runed`; `webd`, `timed`, `onbod`, `slakd`, …  |
| Products   | installable agents   | Slack team agent, reality agent, company brain                     |

A product is the bottom layer: the same pipeline, a different folder. The public docs collapse Components into Daemons (you deploy daemons, not packages) — operators see three layers, the spec names all four.

## Direction

That's what arizuko is today: a single-host, multi-tenant agent router with channels, folders, and a shared SQLite. Three frames describe where it's heading.

**Multi-tenant by primitive.** The same code runs `solo/inbox` and `corp/eng/sre/oncall`. Folder hierarchies have no fixed depth. Every primitive — grants, channels, secrets, routes, scheduled work — scales from one-user-one-channel to a fleet of agents under shared admin. Adding tenants is adding rows + folders; the daemon graph is invariant.

**Agent-as-data.** Each agent is a folder of values — `PERSONA.md`, `skills/`, `MEMORY.md`, `.diary/`, ACL rules, route rows, secret references. The runtime is an interpreter over those values. The plan is to move cold-tier config (ACL, routes, persona, skills, scheduled tasks, invites) toward git as the source of truth ([specs/9/3](specs/9/3-git-as-truth.md)), with SQLite as a rebuildable cache. Forking, auditing, and distributing an agent then ride native git verbs instead of bespoke ones.

**Agent-first managed (target state).** The agent and the operator will speak the same language. The plan ([specs/5/17](specs/5/17-openapi-mcp.md), rolled out by [specs/5/16](specs/5/16-mcp-rest-unification.md)) is one handler per cold-tier resource with two faces — REST authored for humans + external tools, MCP derived for in-container agents — over one auth gate (`auth.Authorize`) and one tx-bound audit row. The first resource (`proxyd_routes`) already runs that pattern; the rest follow incrementally. Declarative intent is then carried by YAML manifests dispatched through the same gate ([specs/5/8](specs/5/8-yaml-manifests.md)): operator writes the YAML, `arizuko apply` walks it row by row, daemons see resreg-shaped mutations identical to any other call.

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
vim /srv/data/arizuko_foo/.env             # set AUTH_SECRET, WEB_HOST, …
arizuko group foo add tg:-123456789 main   # register first group
arizuko run foo                            # generate compose + docker compose up
```

Full walkthrough — prerequisites, blocker fixes, per-adapter tokens, troubleshooting: [INSTALL.md](INSTALL.md).

A first deployment runs the core plane — `authd` (token authority), `routd` (conversation/router), `runed` (agent execution) — plus one adapter (`teled`, `slakd`, `discd`, `webd`, or `emaid`). Add `dashd` for operator UI, `timed` for scheduled tasks, `onbod` for invite flows, `crackbox` for default-deny egress. Each adapter ships as a `template/services/<name>.toml` — no Go edits required. See [EXTENDING.md](EXTENDING.md) for wiring new channels.

A tar of `/srv/data/arizuko_<name>/` is a complete instance backup — the per-daemon SQLite databases (`routd.db`, `runed.db`, `auth.db`, … WAL), group folders, per-user memory, secrets, agent files.

## How it works

```
adapter (teled/discd/slakd/…) --HTTP--> routd (conversation plane)
                                          │     └── routd.db (WAL)
                                          │     └── hosts agent MCP socket in-process (ipc)
                                          ├──HTTP--> runed (execution plane)
                                          │           └── runed.db; docker.sock + crackbox
                                          │           └── spawns the agent container per turn
                                          └──HTTP--> authd (token authority)
                                                      └── auth.db; ES256 minter + JWKS

timed   — scheduler, federates due tasks over routd
onbod   — onboarding, OAuth, admission queue (onbod.db)
webd    — web chat channel adapter + SSE hub
proxyd  — auth-gated reverse proxy (TOML route table)
davd    — WebDAV workspace (per-group, dufs)
dashd   — operator dashboards + admin CRUD
```

Each core daemon owns and migrates its own database; no daemon migrates another's. Adapters, `webd`, `proxyd`, and `timed` reach `routd`; `routd` reaches `runed`; every daemon verifies tokens minted by `authd`.

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
- **Egress isolation**: `crackbox` enforces default-deny on agent outbound traffic via per-source-IP allowlists. Credential/placeholder swap at the boundary (`egred` HTTPS-MITM, `specs/8/Z-egred-mitm.md`) is planned, not shipped.
- **ACL**: `auth.Authorize` — one `acl` table, deny-wins, tier defaults in code. MCP tools gated per-action per-principal.
- **Secret injection**: folder secrets are AES-256-GCM encrypted at rest; injected into the container at spawn time, never written to disk in plaintext.
- **Identity relay**: `proxyd` stamps `X-User-*` headers, proving the channel with a `service:proxyd` ES256 bearer (verified via `auth.ProxydTransit`); backends trust the headers only when that proof holds. Client-supplied `X-User-*` headers are stripped.

Full threat model in [SECURITY.md](SECURITY.md).

## What's planned

- Uniform MCP+REST across the cold tier — one handler per resource, both faces (REST authored, MCP derived) ([spec](specs/5/17-openapi-mcp.md))
- End-user agent provisioning — POST a definition, get a tenant + chat token ([spec](specs/5/5-tenant-self-service.md))

## Build & test

Build, test, image, and smoke targets: [INSTALL.md](INSTALL.md).

Beyond unit/e2e tests, [`anteval/`](anteval/) is the **agent-capability gate** — a
black-box prober that runs real tasks through the public surfaces (REST/HTTP/MCP +
a callback sink) against a _live_ instance and grades observable effects, proving
the in-container agent can self-modify, spawn+grant subagents, publish web, and
build chat apps reachable over REST and MCP. Spec: [`specs/5/9`](specs/5/9-agent-capability-eval.md).

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
