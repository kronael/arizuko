# arizuko

arizuko runs many AI agents on one Linux server you own. Each agent is a
folder of plain files: who it is, what it remembers, what it can do, and which
chats reach it. You change an agent by talking to it, and you read the result
as a diff on disk. Small daemons around the folders decide which chats reach
which agent and what each agent may do beyond its own folder.

[Install](INSTALL.md) • [How it works](ARCHITECTURE.md) •
[Security](SECURITY.md) • [Routing](ROUTING.md) • [Extending](EXTENDING.md) •
[Web docs](template/web/pub/arizuko/) (served at `/pub/arizuko/` on every
instance)

Self-hosted only, on one Linux host with Docker and KVM. Sized for agent teams,
not consumer traffic.

## What you can run with it

- A team agent in Slack, Discord or Telegram that answers on mention or in DMs,
  with its own persona and memory per channel.
- A personal assistant that keeps conversation history, a diary and a profile
  of you across restarts.
- One agent across several channels at once. Slack, Discord, email, WhatsApp
  and web chat all feed the same store.
- Scheduled work. `timed` fires a turn on a cron schedule; no webhook needed.
- An email or webhook agent. `emaid` reads IMAP and checks DMARC; any caller
  can POST to `/hook/<token>`.
- Search over your own documents. Mount a tree over WebDAV (`davd`) and the
  agent reads and cites it; `find_messages` ranks past conversation by
  content.
- Many tenants on one deployment. `corp/eng/sre` and `solo/inbox` run the
  same code at any folder depth.

## How a message becomes a reply

A folder is an agent. It holds `PERSONA.md`, a `skills/` directory,
`MEMORY.md`, a diary and its permission rows. Folders nest (`corp/sales`,
`corp/eng/sre`), and each one keeps only the conversations routed to it.

```
# A message arrives in Slack.
@andy can you summarize the open PRs?

# 1. slakd posts it to routd, which appends it to the message store
# 2. routd matches the route table, builds the prompt, calls runed
# 3. runed boots one KVM guest for that folder's turn
# 4. routd serves the agent's MCP socket; runed bridges it into the guest over vsock
# 5. the agent runs, calls tools, submits its turn
# 6. routd delivers the reply back to Slack through slakd
```

Agents coordinate on the same conversation plane they serve people on. A turn
can route to a sibling, delegate to a child, schedule a task or publish a web
page by posting to `routd`. There is no second coordination bus.

Conversation state lives in routd's tables. Agent state lives in the folder:
the session transcript, skills, memory, diary. A guest is ephemeral: `runed`
boots one per turn, shares the folder into it, runs the turn and powers it
off. The session id is declared before the run, so a turn killed by a restart
is resumed by the next one in that topic.

Every event runs the same six steps:

<!-- twin:six-steps -->

```
Slack @mention in #eng
  → Event          one inbox row (routd.messages)
  → Routing        → corp/eng/oncall
  → Agent          folder loaded: persona, skills, memory
  → Authorization  may it read / send / delegate here?
  → Turn           one ephemeral KVM guest run, then it exits
  → State          rows written, files edited
  → reply in the Slack thread
```

<!-- /twin -->

Channels, tasks, webhooks, secrets, delegation and workflows are all
compositions of these six, with identity (folders, JIDs, roles, scopes) as the
coordinate system they are addressed in ([specs/5/A](specs/5/A-primitives-framing.md)).

| Layer      | What it is           | Examples                                                          |
| ---------- | -------------------- | ----------------------------------------------------------------- |
| Primitives | the concepts         | Event, Routing, Agent, Authorization, Turn, State                 |
| Components | Go packages          | `store`, `router`, `auth`, `ipc`, `runed`, `groupfolder`          |
| Daemons    | deployable processes | `authd` + `routd` + `runed`; `webd`, `timed`, `onbod`, `slakd`, … |
| Products   | installable agents   | Slack team agent, reality agent, company brain                    |

A product is a folder's contents. The same pipeline runs every product.

## When to use arizuko

Use it when an agent has to live in real chat channels and keep a separate
identity per team, customer or workflow:

- one agent per folder, each with its own persona, memory, skills and
  permission rows
- Slack, Discord, Telegram, email, web chat and webhooks as first-class inputs
- scheduled work, delegation between agents and long-running background jobs
- one machine, inspectable as files, routes and store rows

Do not pick it for enterprise document search. There is no connector sync and
no vector index; pair it with a retrieval system and let arizuko act on the
results (see [What arizuko does not include](#what-arizuko-does-not-include)).
For a coding assistant on one developer machine, a single-user tool is a
closer fit. arizuko is for agents that sit behind channels and serve teams.

## Getting started

You need a Linux x86_64 host with `/dev/kvm`, Docker with the compose plugin,
Go, write access to `/srv/data/`, and a token for one channel.

```bash
make build                                        # ./arizuko + daemon binaries
sudo make images DATA_DIR=/srv/data/arizuko_foo   # daemon images + the guest rootfs
sudo make kernel DATA_DIR=/srv/data/arizuko_foo   # the guest kernel, built once
./arizuko create foo                              # /srv/data/arizuko_foo + .env
./arizuko packages foo add extenddb               # the store server (then init it: INSTALL.md)
vim /srv/data/arizuko_foo/.env                    # ASSISTANT_NAME, a model key, a bot token, RUNED_*_DIR
./arizuko packages foo add teled                  # one channel adapter
./arizuko group foo add telegram:group/123456789 main
./arizuko run foo                                 # generate compose, docker compose up
```

[INSTALL.md](INSTALL.md) has the full walk: prerequisites, the store server
and its per-daemon access keys, adapter tokens, and the "healthchecks green
but no reply" checklist.

A first deployment runs the core plane, `authd` (token authority), `routd`
(conversation and routing) and `runed` (turn execution), plus one adapter.
Core also runs with no adapter at all; `routd`, `dashd`, `onbod` and `timed`
answer over REST and MCP on their own, and an adapter adds a chat surface.
`dashd` adds the operator UI, `timed` scheduled tasks, `onbod` invite flows.

Every daemon's tables live on one store server, and every daemon that reaches
the store signs with its own access key, kept in its own `env/<daemon>.env`.
A daemon that has the server's address and no key refuses to start, and
because `docker compose up` runs under systemd, that one exit restart-loops the
whole instance. `arizuko generate` names such a daemon before you deploy it,
at ERROR in its log (`store enrolment is partial: these daemons reach the
store and hold no key`), and then continues. Mint the key before you enable a
package: INSTALL.md § The per-daemon store keys.

Each adapter ships as a package: a compose fragment under `template/services/`
that `arizuko packages <instance> add <name>` enables, with no Go edits.
`arizuko packages <instance> install <git-or-dir>` installs a third-party
package (compose fragment, proxyd route, grant, skills) onto a running
instance; `upgrade` and `remove` work from the `installed_packages` record.
One group can blend several products: list them in `~/products.toml` and run
`arizuko products <instance> apply <folder>`. Two products claiming the same
file stop the apply rather than silently dropping one. See
[EXTENDING.md](EXTENDING.md).

A tar of `/srv/data/arizuko_<name>/` is a complete backup when the instance is
stopped: the store server's data under `store/extenddb_db/`, the group
folders, memory, secrets and agent files.

## How it works

```
adapter (teled/discd/slakd/…) --HTTP--> routd (conversation plane)
                                          │     └── messages, routes, acl, secrets, tasks
                                          │     └── hosts the agent MCP socket in-process (ipc)
                                          ├──HTTP--> runed (execution plane)
                                          │           └── spawns, session_log; one KVM guest + egress proxy per turn
                                          └──HTTP--> authd (token authority)
                                                      └── signing_keys; ES256 minter + JWKS

timed   — scheduler, fires due tasks through routd
onbod   — onboarding, OAuth, admission queue
webd    — web chat adapter + SSE hub
proxyd  — auth-gated reverse proxy (`proxyd_routes`, read per request)
davd    — WebDAV workspace (per group, dufs)
dashd   — operator dashboards + admin CRUD
vited   — web origin for `/pub/` pages behind proxyd (`vited/`)
```

Each daemon owns and creates its own tables; the daemons that also read
routd's open its store under their own key, and `store/owner_reach.go`
declares which daemon may reach which tables. `DYNAMODB_URL` names the store
server they all open their tables on: ExtendDB speaking the DynamoDB protocol over PostgreSQL, one server
per instance or one shared by several ([specs/6/42](specs/6/42-dynamo-over-postgres.md),
[specs/6/43](specs/6/43-shared-store-server.md)). Adapters, `webd`, `proxyd`
and `timed` call `routd`; `routd` calls `runed`; every daemon verifies tokens
minted by `authd`.

Package graph, message flow, guest lifecycle and the table list are in
[ARCHITECTURE.md](ARCHITECTURE.md).

## Channel adapters

| adapter | platform           |
| ------- | ------------------ |
| teled   | Telegram           |
| discd   | Discord            |
| slakd   | Slack (Events API) |
| bskyd   | Bluesky            |
| reditd  | Reddit             |
| emaid   | Email (IMAP/SMTP)  |
| whapd   | WhatsApp (Baileys) |
| twitd   | X/Twitter          |
| linkd   | LinkedIn           |
| gitd    | GitHub (webhooks)  |

Optional hooks: Whisper transcription (`WHISPER_BASE_URL`), text to speech
(`ttsd` + `TTS_BASE_URL`), a second model (`OPENAI_API_KEY` or
`CODEX_API_KEY` in the host `.env` or a user's own `/dash/me/env` row), and a
model gateway (the `litellm` package). With the gateway, the model credential
stays in the gateway container; each turn gets a virtual key charged to its
folder and caller, and the gateway is the only ledger of spend
([specs/6/23](specs/6/23-llm-gateway.md), [specs/6/39](specs/6/39-gateway-owns-spend.md)).

## What arizuko does not include

- connector crawlers for Confluence, Notion, Google Drive, Jira or Salesforce
- an embedding pipeline or a vector database
- permission sync from external systems into per-document ACLs
- multi-model routing across providers; the agent runtime is Claude Code
- a managed control plane or a hosted service

You can mount or write documents into a group's folder, and the agent reads
them directly. For large-scale retrieval, pair arizuko with a retrieval stack
and let it do what it does: receive messages, keep per-group memory, enforce
grants, schedule tasks, act across channels.

## Security model

- One KVM guest per turn, on its own tap. Sibling groups never share a guest
  or a context window.
- Egress is default-deny by construction. Each turn gets its own `egress`
  proxy holding the folder's allowlist; the tap drops every packet that is not
  addressed to that proxy, and the proxy never terminates TLS
  ([specs/6/41](specs/6/41-qemu-substrate.md)).
- Authorization is one table and one function: `acl` rows evaluated by
  `auth.Authorize`, deny wins, no fallback. Capability comes from granted rows,
  never from a folder's depth; `role:member` is the seeded default.
- Secrets are AES-256-GCM encrypted at rest. Only model credentials reach the
  guest, and with a model gateway not even those. A connector's token is
  resolved on the host for the one call that needs it and never enters the
  guest.
- `proxyd` is the sole stamper of `X-User-*` headers and proves itself to
  backends with a `service:proxyd` ES256 bearer. Client-supplied identity
  headers are stripped.

Threat model and boundary table: [SECURITY.md](SECURITY.md).

## Direction

- Operator and agent speak the same language: one handler per cold-tier
  resource with a REST face for people and tools and an MCP face for agents,
  over one auth gate ([specs/5/17](specs/5/17-openapi-mcp.md),
  [specs/5/16](specs/5/16-mcp-rest-unification.md)). Most resources run that
  pattern today; the remaining REST twins are listed in ARCHITECTURE.md § Cold
  tier vs hot tier.
- The store is authoritative. YAML manifests are a dump and import format for
  the cold tier (`arizuko export`, `arizuko apply`), not a synced source of
  truth ([specs/5/8](specs/5/8-yaml-manifests.md)).
- Single-world composition ([specs/7/26](specs/7/26-single-world-deployment.md))
  describes the core daemons in one process. It is a design, not an
  installation mode.

Planned and not started: end-user agent provisioning, POST a definition and
get a tenant plus a chat token ([specs/5/5](specs/5/5-worlds-agents-sessions.md));
renaming `group`/`folder` to `path` across code, store and tool names
([specs/5/33](specs/5/33-paths-roles.md) decision 9).

## Build and test

Build, test, image and smoke targets: [INSTALL.md](INSTALL.md).

[`anteval/`](anteval/) is the agent-capability gate. It runs real tasks through
the public REST, HTTP and MCP surfaces of a live instance and grades the
observable effects: can the agent change its own files, spawn and grant a
subagent, publish a page, build a chat app reachable over REST and MCP
([specs/5/9](specs/5/9-agent-capability-eval.md)).

## How to read this

| Order | File                                             | The question it answers                                               |
| ----- | ------------------------------------------------ | --------------------------------------------------------------------- |
| 1     | this file                                        | What is it, and how do I start?                                       |
| 2     | [INSTALL.md](INSTALL.md)                         | How do I get an instance running, and what do I do when it is silent? |
| 3     | [ARCHITECTURE.md](ARCHITECTURE.md)               | Where does each piece live, and how does a turn run?                  |
| 4     | [ROUTING.md](ROUTING.md)                         | Which folder answers a message, and how does the reply get back?      |
| 5     | [SECURITY.md](SECURITY.md)                       | What is isolated from what, and by which mechanism?                   |
| 6     | [GRANTS.md](GRANTS.md)                           | Who may do what, and where is that decided?                           |
| 7     | [EXTENDING.md](EXTENDING.md)                     | How do I add a channel, a connector, a package or a skill?            |
| 8     | [CHANGELOG.md](CHANGELOG.md)                     | What shipped, and when?                                               |
|       | [`cmd/arizuko/README.md`](cmd/arizuko/README.md) | What does each `arizuko` subcommand do?                               |
|       | [`specs/`](specs/)                               | Which change is in flight, and why?                                   |
|       | [`BUGS.md`](BUGS.md)                             | What is open, and what waits on a person?                             |
|       | [LICENSE](LICENSE)                               | MIT.                                                                  |

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
