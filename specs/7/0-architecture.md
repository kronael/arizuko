# arizuko master architecture — microservice design

**Status**: design (updated 2026-03-14)

## Design principles

1. **Boundaries for agents**: the reason to split is not
   "small is good" — it's that clear boundaries make each
   piece testable in isolation and writable by an AI agent.
   An agent can build a channel adapter from scratch given
   the protocol spec. No context about the rest of the system
   needed.
2. **Contract is the database**: SQLite schema is the API.
   Co-located services share one `.db` file directly. Each
   service owns its tables, shares the message bus. Language
   per service is free — anything that speaks SQLite.
3. **Each daemon is independently testable**: open a test DB,
   run migrations, verify behavior. No integration environment
   needed to test a single component.
4. **Channels are external**: orchestrator doesn't start, stop,
   or manage channels. They're independent containers that
   register via HTTP on the docker network. Channels are the
   only services that use HTTP instead of direct DB access
   (they may run on remote hosts without filesystem access).
5. **Service-namespaced migrations**: each service owns its
   migration runner and `.sql` files. See `specs/7/7-microservices.md`.

## Component map

```
     ┌──────────────────────────────────────────────┐
     │              SQLite (messages.db)             │
     │                                               │
     │  messages, chats, routes, registered_groups,  │
     │  sessions, scheduled_tasks, auth, jobs, ...   │
     └──┬────┬────┬────┬────────────────────────────┘
        │    │    │    │
   ┌────┴┐ ┌┴───┐│ ┌──┴──────────┐
   │sched│ │ API││ │ orchestrator │
   │     │ │    ││ │  routing     │
   └─────┘ └────┘│ │  queue       │
                  │ │  containers  │──── agent containers
                  │ │  MCP/IPC     │     (docker-in-docker)
                  │ └─────────────┘
                  │
           HTTP API (:8080)
                  │
     ┌────────────┤ docker network
     │            │
 ┌───┴──┐   ┌────┴┐  ┌────┐ ┌────┐
 │  tg  │   │ dc  │  │ wa │ │ em │
 └──────┘   └─────┘  └────┘ └────┘
 channel adapters (HTTP, external)
```

Co-located services (orchestrator, scheduler, API) share
the SQLite file directly. Channel adapters are external —
they use HTTP because they may run on remote hosts without
filesystem access.

## How it works

### Channel → Router (inbound)

Channel receives a platform event, POSTs it to router:

```
POST /v1/messages
{"chat_jid": "telegram:-1001234", "sender": "telegram:123", "content": "hello", ...}
→ 200 {"ok": true}
```

Router stores in SQLite, routes to the appropriate group.

### Router → Channel (outbound)

Router calls channel's HTTP endpoint to send:

```
POST http://channel-url/send
{"chat_jid": "telegram:-1001234", "content": "reply", ...}
→ 200 {"ok": true, "message_id": "tg-456"}
```

Synchronous. When the call returns, the message was delivered
to the platform. No outbox, no polling, no ack protocol.

### Registration

Channel registers on startup:

```
POST /v1/channels/register
{
  "name": "telegram",
  "url": "http://telegram:9001",
  "jid_prefixes": ["telegram:"],
  "capabilities": {"send_text": true, "send_file": true, "typing": true}
}
→ 200 {"ok": true, "token": "<session-token>"}
```

Router health-checks registered channels every 30s.
Three failures → auto-deregister. Channel re-registers
on restart and router replays queued outbound.

Full protocol: `specs/7/1-channel-protocol.md`.

## Component 1: Channel Adapter

One process per platform. Independently replaceable.
Language follows best library for that platform.

**Two-sided HTTP**:

- Client side: calls router API to register and deliver messages
- Server side: listens for router's send/typing/health calls

Channel self-registers with router on startup ("I handle
telegram:\*, call me at http://telegram:9001"). Router calls
channel to send outbound. Channel calls router to deliver
inbound. Both directions are synchronous HTTP.

**Contract**: `specs/7/1-channel-protocol.md`

**Lifecycle**: external. Router doesn't start or stop channels.
Each channel adapter is its own container managed by docker
compose. They self-register via HTTP — that's how router
discovers them.

**Implementations**:

- Telegram: TS (grammy) or Go (gotgbot) or Python (python-telegram-bot)
- Discord: TS (discord.js) or Go (discordgo)
- WhatsApp: TS (baileys) — no rival in other languages
- Email: Go (go-imap) or Python (aioimaplib)

**Size**: ~200-400 LOC each. An agent can write one in
10 minutes given the protocol spec.

## Component 2: Orchestrator

The main process. Polls messages, resolves routes, manages
the job queue, spawns containers, streams output back to
messages. Routing is a function, not a separate service.

**Responsibilities**:

- HTTP API: channel registration, inbound messages, admin
- Route resolution: JID → group via routes table
- Job queue: per-group serialization, concurrency cap
- Container runner: docker-in-docker lifecycle
- MCP server: per-group unix socket for agent IPC
- Session management: resume, evict on error

Opens the shared SQLite DB directly. Runs its own migration
runner with service name `orchestrator`.

See `specs/7/9-orchestrator.md` for full spec.

## Component 3: Scheduler

Separate process. Opens the shared SQLite DB directly.
Polls `scheduled_tasks` for due items, INSERTs into
`messages`. A cron daemon that writes rows.

Owns `scheduled_tasks` table. Runs its own migration
runner with service name `scheduler`.

Task CRUD: agents create/pause/cancel tasks via MCP tools
(IPC actions that write directly to the DB). The scheduler
only reads.

See `specs/7/8-scheduler-service.md` for full spec.

## Component 4: Web Server

**Open**: separate process or router-internal?

If separate, it talks to router the same way channels do —
HTTP API. Slink messages are just `POST /v1/messages` with
`origin=web`. Auth tables could be router-internal (web
calls router API for auth) or a separate auth DB.

If router-internal, it's simpler — one process, one port,
shared state. Web routes are just more handlers on the
router's HTTP server.

Leaning router-internal. Web + router share too much
state (auth, groups, messages) for separate processes
to be worth the coordination cost.

## Component 5: Agent Runner

Thin wrapper inside a docker container. Invokes Claude Code
CLI, pipes stdin/stdout, connects to router MCP socket.

**Contract**:

- IN: JSON on stdin `{prompt, sessionId, secrets, ...}`
- OUT: JSONL on stdout (results, session IDs)
- OUT: MCP client → router socket for send_message, etc.

**Interface**: stdin/stdout JSON + MCP unix socket.

Claude Code CLI is the runtime. Agent runner just configures
and invokes it. ~50-100 LOC in any language.

MCP connection to router via socat stdio-to-socket bridge:

```json
{
  "nanoclaw": {
    "command": "socat",
    "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/router.sock"]
  }
}
```

### Secret handling (5-layer model)

1. **Host-side isolation** — `.env` read into memory, never `process.env`
2. **Stdin injection** — secrets via container stdin pipe, not env/args
3. **Ephemeral disk** — `/tmp/input.json` deleted immediately after read
4. **SDK-only access** — secrets in `sdkEnv` object, not `process.env`
5. **Bash scrubbing** — `unset ANTHROPIC_API_KEY` on every command

Secrets never in: Docker CLI args, environment, mounted files,
image layers. Residual risk: secret in agent-runner process
memory. Prompt injection can introspect SDK state. Acceptable
tradeoff — SDK must run inside to keep all tools isolated.

### Container security flags

Required: `--cap-drop ALL`, `--security-opt no-new-privileges`,
`--memory 1g`, `--cpus 2`.

Network: on by default (agent needs Anthropic API). Per-agent
toggle via config.

### Mount security

17-pattern blocklist: `.ssh`, `.gnupg`, `.gpg`, `.aws`, `.azure`,
`.gcloud`, `.kube`, `.docker`, `credentials`, `.env`, `.netrc`,
`.npmrc`, `.pypirc`, `id_rsa`, `id_ed25519`, `private_key`,
`.secret`.

Allowlist file outside project root so containers can't tamper.
Folder name validation: `/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/`
with path traversal prevention.

## Component 6: MCP IPC (agent ↔ orchestrator)

Orchestrator runs an MCP server per group on a unix socket.
Agent containers connect as MCP clients.

```
Router                           Agent Container
──────                           ──────────────
MCP server on                    MCP client via socat
/data/ipc/<group>.sock           /workspace/ipc/router.sock

     ←── initialize ──────────
     ──── capabilities ───────→
     ←── tools/call ──────────
         {send_message, text}
     ──── result: {ok} ───────→
     ──── notification: ──────→   (push to agent)
         {new_message: "..."}
```

### MCP tools

| Tool                | What it does           | Min tier |
| ------------------- | ---------------------- | -------- |
| `send_message`      | send to chat channel   | 3        |
| `send_file`         | send file to chat      | 3        |
| `schedule_task`     | create scheduled task  | 2        |
| `list_tasks`        | list group's tasks     | 3        |
| `delete_task`       | delete a task          | 2        |
| `register_group`    | register new group     | 1        |
| `clear_session`     | reset agent session    | 3        |
| `delegate`          | delegate to subgroup   | 2        |
| `inject_message`    | inject as if from user | 1        |
| `escalate_group`    | escalate to parent     | 1        |
| `set_routing_rules` | update routing         | 2        |
| `pause_task`        | pause scheduled task   | 2        |
| `resume_task`       | resume paused task     | 2        |
| `cancel_task`       | cancel scheduled task  | 2        |

Replaces file-based IPC (JSON files + SIGUSR1).

### Permission tiers

Tier is computed from folder depth (slash count):

- **Tier 0 (root)**: no slash in folder — full access
- **Tier 1 (world)**: one slash — can manage own world
- **Tier 2 (agent)**: two slashes — limited access
- **Tier 3 (worker)**: three+ slashes — most restricted

Tiers gate MCP tool access. Each tool has a minimum tier;
lower-numbered tiers have more privilege. Tier 0 can call
everything. Tier 3 can only call send_message, send_file,
list_tasks, clear_session.

## Process management

Master process reads the services/ directory, generates a
docker-compose.yml, and runs `docker compose up -d`. Each
channel adapter is its own container image. Only the master
runs as root (needs docker access). All other processes run
unprivileged in containers.

### Services directory

```
/srv/data/arizuko_andy/services/
  telegram.toml         ← channel adapter config
  discord.toml          ← another adapter config
  whisper.toml          ← MCP sidecar config
```

Each `.toml` file declares the container image and config:

```toml
# /srv/data/arizuko_andy/services/telegram.toml
image = "arizuko-telegram:latest"
restart = "on-failure"

[environment]
ROUTER_URL = "http://router:8080"
TELEGRAM_BOT_TOKEN = "${TELEGRAM_BOT_TOKEN}"
```

Master reads .env and interpolates variables into the
generated compose file.

### Generated docker-compose.yml

```yaml
services:
  orchestrator:
    image: arizuko:latest
    command: ['run']
    volumes: ['./:/srv/data', '/var/run/docker.sock:/var/run/docker.sock']
    ports: ['8080:8080']
    restart: on-failure

  scheduler:
    image: arizuko-scheduler:latest
    volumes: ['./store:/srv/data/store']
    restart: on-failure

  telegram:
    image: arizuko-telegram:latest
    environment:
      ROUTER_URL: http://orchestrator:8080
      TELEGRAM_BOT_TOKEN: ${TELEGRAM_BOT_TOKEN}
    restart: on-failure
    depends_on: [orchestrator]
```

Orchestrator and scheduler share the SQLite DB via volume
mount. Only orchestrator has docker-in-docker access (for
agent containers). Channel adapters connect via HTTP over
the docker network.

### How `arizuko run` works

1. Start the master process
2. Scan `services/` directory for `.toml` files
3. Generate `docker-compose.yml` from service configs
4. Run `docker compose up -d`
5. Docker compose manages lifecycle (restart, logging, resources)
6. On master shutdown: `docker compose down`

### Install a new component

Drop a `.toml` config in the services dir:

```bash
# Install a reddit channel adapter
cp reddit.toml /srv/data/arizuko_andy/services/reddit.toml
arizuko run andy   # master regenerates compose, `docker compose up -d`
```

Remove: delete the file, master regenerates, `docker compose up -d`
removes the orphan container.

### Why docker compose, not a custom supervisor

- Restart on crash: `restart: on-failure` (free)
- Logging: `docker compose logs telegram` (free)
- Resource limits: compose resource constraints (free)
- Process isolation: separate containers, no shared memory (free)
- Docker networking: service discovery by name (free)
- We write zero supervision code

### Operations

```bash
arizuko run                # generate compose, start everything
arizuko status             # show router + registered channels + service health
docker compose logs -f telegram  # logs for one component
docker compose ps          # container-level inspection
docker compose restart telegram  # restart one component
```

### Remote channels

The services dir is for co-located containers. Channels
running on other hosts or in VMs don't need a `.toml`
file — they just register via HTTP using the external
router URL. The two mechanisms coexist: local channels
are managed via docker compose, remote channels
self-register over the network.

## Transport compatibility

Current: TCP only (`http://host:port`).

**Future**: HTTP over unix socket and vsock are natively
supported in Go. The protocol stays pure HTTP — just swap
the dialer. Not building toward this now but the design
is compatible. See `specs/7/1-channel-protocol.md` for
the transport table.

## Not components: memory, skills, character

These shape agent behavior, not system architecture.
They live inside the container, not in the router:

- **Skills**: SKILL.md files mounted into container
- **Memory**: files in group folder (diary/, CLAUDE.md)
- **Character**: character.json merged into system prompt

The architecture just mounts the right directories.

## Pipeline language: langaxe (orthogonal)

Agent-level pipelines (chaining tools, prompts, agents)
handled by langaxe (`/home/onvos/app/langaxe`). Not part
of arizuko. Router doesn't care what runs inside the
container — Claude Code, langaxe, or a shell script.

## Language per service

| Service          | Best language | Why                    |
| ---------------- | ------------- | ---------------------- |
| telegram adapter | TS or Go      | grammy / gotgbot       |
| discord adapter  | TS or Go      | discord.js / discordgo |
| whatsapp adapter | TS            | baileys has no rival   |
| email adapter    | Go or Python  | go-imap / aioimaplib   |
| orchestrator     | Go            | current implementation |
| web server       | Go            | orchestrator-internal  |
| agent runner     | any (or bash) | thin CLI wrapper       |

Only channel adapters have a strong language preference
(library ecosystems). Everything else is language-agnostic.

## Monorepo build model

Built-in components live in the monorepo. Each has a
binary and a Dockerfile in its subdirectory.

```
cmd/arizuko/         ← router binary
channels/telegram/   ← telegram adapter binary + Dockerfile
channels/discord/    ← discord adapter binary + Dockerfile
channels/whatsapp/   ← whatsapp adapter binary + Dockerfile
channels/email/      ← email adapter binary + Dockerfile
container/           ← agent runner + Dockerfile
```

`make` builds all images. `./arizuko` at repo root
generates docker-compose.yml and launches everything.

```bash
make              # build all images
./arizuko         # generate compose, docker compose up -d
./arizuko status  # show router + registered channels
```

## Extension modules (open problem)

Third-party components (new channel adapters, MCP servers)
that live outside the monorepo. The hard part.

What works now:

- Build your own image, push to a registry
- Add a `.toml` config to the services dir referencing it
- Router doesn't care — it just sees HTTP registration

What's missing:

- Discovery: how to find extensions
- Install: `arizuko install <repo>` that clones + builds + adds config
- Versioning: how to pin and upgrade extension versions
- Security: extension containers get docker network access to router
- Dependencies: extension A needs extension B

Not designed yet. The built-in model works. Extensions are
deferred until the built-in channels are validated.

### Multiple routers

Current design: one router per instance, single SQLite.
Horizontal scaling would need either:

- Multiple routers sharing PostgreSQL (big change)
- Sharding by group across router instances
- Staying single-instance (fine for chat scale)

Single instance is fine for foreseeable scale.

### Event types beyond messages

Reactions, edits, deletes, joins, leaves — separate
endpoints or one generic `/v1/events`? Keeping specific
endpoints for now, generic is more extensible but less
self-documenting.

## Historical context

Earlier designs explored:

- SQLite as shared message bus (outbox pattern) — superseded
  by REST. Channels never touch SQLite.
- Channels as in-process goroutines — works for monolith,
  but doesn't survive isolation boundaries.
- File-based IPC for agent↔router — being replaced by
  MCP over unix socket.
- Custom process supervisor — replaced by docker compose.
