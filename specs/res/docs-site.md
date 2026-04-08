---
status: draft
---

# Documentation Site Spec

## Overview

Three-part documentation site for arizuko:

1. **Landing** — systems-first one-pager for builders
2. **Docs** — subsystem reference, how things connect
3. **Cookbooks** — walkthroughs with design rationale

This spec defines the information architecture, content
outline, and style rules for each part. It is written
so that someone can produce the actual content from it
without further clarification — except where open questions
are listed.

---

## Background from Research

Key facts grounding this spec:

- Arizuko is a Go + SQLite + Docker stack. No framework, no ORM.
- The schema is the contract between services. Daemons share one DB.
- Channel adapters are separate HTTP processes implementing a
  3-endpoint protocol (register, inbound, outbound). Any language works.
- Agents are Claude Code CLI running inside Docker containers — not
  an SDK wrapper. Full tool use, subagents, MCP hooks.
- The hierarchy (tier 0/1/2/3 from folder depth) is the permission
  system. No separate auth table needed for MCP calls.
- Memory is filesystem: diary entries, facts, episodes, user files.
  The gateway injects summaries at prompt time. Skills are markdown.
- A group is a directory: CLAUDE.md, SOUL.md, diary/, facts/, users/.
  The group folder IS the agent's home.
- The existing landing page at `/web/web/arizuko/index.html` is
  feature-list-forward. The new site should be systems-forward.
- Three recently written specs exist that expand the scope:
  `cli-chat.md` (terminal mode), `memory-skills-standalone.md`
  (portable memory layer), `crackbox-sandboxing.md` (QEMU backend).
  These are not yet shipped but are relevant to the docs.

---

## 1. Information Architecture

```
/                   landing — the system model + selling points
/docs/              deep dive — subsystem reference
  /docs/overview    architecture diagram + message flow
  /docs/gateway     gated: message loop, routing, queue
  /docs/store       SQLite schema, WAL mode, ownership
  /docs/container   Docker lifecycle, mounts, input/output protocol
  /docs/ipc         MCP over unix socket, identity, tools
  /docs/auth        tier model, grants, web auth
  /docs/channels    channel protocol, adapters, chanreg
  /docs/adapters    teled, discd, mastd, bskyd, reditd, emaid, whapd
  /docs/memory      diary, episodes, facts, users, injection
  /docs/scheduler   timed, cron tasks, one-shot, isolated context
  /docs/onboarding  onbod state machine, prototype copy
  /docs/dashboard   dashd, six views, HTMX polling
  /docs/web         proxyd, webd channel adapter, slink auth
  /docs/compose     generate, run, service naming conventions
  /docs/cli         arizuko create/group/status/chat commands
/cookbooks/         walkthroughs — design choices explained
  /cookbooks/add-channel-adapter
  /cookbooks/custom-mcp-tool
  /cookbooks/two-telegram-instances
  /cookbooks/per-user-agents
  /cookbooks/memory-skills-standalone
  /cookbooks/cli-chat-mode
  /cookbooks/scheduled-tasks
  /cookbooks/onboarding-flow
  /cookbooks/grant-rules
  /cookbooks/crackbox-backend      (QEMU, future — mark as planned)
```

Navigation is flat: three top-level items. Each docs page
is standalone. Cookbooks link to relevant docs pages.

---

## 2. Landing Page Spec

### Hero

Single strong statement of what arizuko IS, not what it does.

Proposed hero text (writer has latitude; do not use marketing clichés):

> Routes messages to containerized Claude agents. Each group is a
> directory. The schema is the contract. No framework.

Subhead (optional, one line): "Go · SQLite · Docker"

Do NOT use: "powerful", "seamless", "robust", "next-generation",
"cutting-edge", or any enterprise-adjacent language.

### What makes a good selling point here

The audience is builders who will read the source and form opinions.
Selling points must be defensible claims grounded in how the system
actually works. Each point should have a concrete referent — a specific
design decision, not a vague benefit.

### Section: The System Model

This section explains the architecture in 150-250 words using a
structural description, not a feature list. It should answer:
"what IS this thing?"

Key points to convey:

- Outside-in design: the boundary (channel protocol) is defined first.
  Adapters plug in; the core does not know they exist.
- The schema is the contract. Services share a SQLite DB with WAL mode.
  If a service can query the table it owns, it works.
- Groups are directories. The agent's $HOME is the group folder.
  Memory, diary, facts, CLAUDE.md, SOUL.md — all plain files on disk.
- Agents are Claude Code CLI in Docker containers. Full tool use,
  subagents, MCP hooks. The container runs once per message, exits,
  and the state persists on the mounted volume.
- IPC is MCP over a unix socket. The gateway starts a server per group
  before the container runs. Socat bridges it in. The agent sees 20
  tools.
- Tier is derived from folder depth, not configuration.

This section links to `/docs/overview` for the full picture.

### Section: Selling Points

Each selling point is: **bold headline** + one-paragraph explanation.

These are ordered by distinctiveness — most unusual/defensible first:

1. **Real Claude Code, not an SDK wrapper**
   Agents run `claude code` CLI inside Docker. Not an API wrapper with
   a subset of capabilities. Full tool use, subagents, MCP, pre-compact
   hooks. What you can do in Claude Code, you can do here. The container
   image is thin: node + agent-runner entrypoint, ~50 LOC wrapping the
   SDK query loop.

2. **The schema is the contract**
   Daemons share one SQLite database in WAL mode. Each daemon owns
   specific tables and runs its own migrations. The channel protocol is
   three HTTP endpoints. Nothing prevents you from writing a new adapter
   in Rust, Python, or shell — you just need to speak the protocol.
   Language doesn't matter. Contract matters.

3. **Groups are directories**
   A group is a folder: `groups/root/`. That folder is the agent's $HOME
   inside the container. Diary entries, facts, user profiles, CLAUDE.md —
   all plain files. Backup is `cp -r`. Diff is `git diff`. The agent
   can edit its own CLAUDE.md, write new skills, and update its memory
   using the same tools it uses for any file. No hidden state.

4. **Memory that survives compaction**
   Context windows fill. When Claude Code compacts, the pre-compact hook
   fires: the agent writes a diary entry, the session is archived into
   an episode. On next invocation, the gateway injects the last 14 days
   of diary summaries and recent episodes into the prompt. The agent
   picks up where it left off. This works across container restarts.

5. **Channel adapters register, not configure**
   Adapters are standalone daemons that POST `/v1/channels/register` on
   startup. The gateway never imports adapter code. Add a new platform
   by writing a daemon that speaks three endpoints. The gateway discovers
   it at runtime.

6. **Tier is folder depth**
   `groups/root/` is tier 0 (root). `groups/root/research/` is tier 1.
   `groups/root/research/qa/` is tier 2. Permissions flow from depth.
   Tier 0 gets all MCP tools. Tier 3+ gets `send_reply` only. No
   permission table. No RBAC configuration. Depth is authority.

7. **Grants narrow, never widen**
   When a group spawns a child, it can restrict what tools the child
   can call. It cannot give the child permissions it does not itself
   have. `NarrowRules(parent, child)` enforces this at delegation time.
   The result is injected into the container's start.json.

8. **Boring stack**
   Go, SQLite (WAL mode), Docker. No message queue. No framework. No
   ORM. The queue is an in-memory struct with a mutex. The IPC is a
   unix socket. The channel protocol is plain HTTP. Every component can
   be understood by reading one file.

### Section: Stat strip (optional, concise)

A row of numbers if verifiable. Do not inflate. Candidates:

- Go packages in core
- MCP tools available to agents
- Channel adapters shipped
- Tables in schema
- Lines of code (verify before publishing)

Omit this section if the numbers do not hold up to scrutiny.
The existing landing page has stats — check they still match the codebase.

### Section: Quick start (code block)

Minimal: create instance → configure → add group → run.

```bash
arizuko create foo
vim /srv/data/arizuko_foo/.env
arizuko group foo add tg:-123456789
arizuko run foo
```

One comment per line max. No explanation prose in this block.

### Section: Navigation / links

Two cards: **Docs** (how it works) and **Cookbooks** (how to use it).
Brief one-sentence description of each.

Link to GitHub repo at footer.

---

## 3. Deep Dive Docs Spec

### Purpose

Each docs page answers: "how does this subsystem work and how does it
connect to the rest of the system?" Not a tutorial. Not a getting-started
guide. Not design rationale. Just the mechanics.

Writers rule: if a sentence explains why a choice was made, delete it
or move it to a cookbook. Docs are what-and-how. Cookbooks are why.

### Format rules

- First sentence: one-line description of the subsystem's role
- Package(s) that implement it
- Tables it owns (if any)
- Key interfaces or types
- How it connects to adjacent subsystems
- Edge cases and invariants worth knowing
- Cross-references to relevant specs and cookbooks

No marketing language. No history ("this was previously...").
No future speculation ("this will eventually...").
Present tense throughout.

### /docs/overview

Role: entry point for the technical reader. Explains the shape of the
whole system before any subsystem page.

Content:

- ASCII package dependency graph (from ARCHITECTURE.md, kept current)
- Full message flow diagram (from ARCHITECTURE.md §Message Flow)
- One-paragraph description of each daemon in table form:

  | Daemon | Role                                       | Language   |
  | ------ | ------------------------------------------ | ---------- |
  | gated  | gateway, message loop, routing, containers | Go         |
  | timed  | cron scheduler                             | Go         |
  | onbod  | onboarding state machine                   | Go         |
  | dashd  | operator dashboard                         | Go         |
  | proxyd | web proxy, auth perimeter                  | Go         |
  | teled  | Telegram adapter                           | Go         |
  | discd  | Discord adapter                            | Go         |
  | mastd  | Mastodon adapter                           | Go         |
  | bskyd  | Bluesky adapter                            | Go         |
  | reditd | Reddit adapter                             | Go         |
  | emaid  | Email (IMAP/SMTP) adapter                  | Go         |
  | whapd  | WhatsApp adapter                           | TypeScript |

- SQLite schema summary (table names + one-line purpose each)
- Directory layout (from ARCHITECTURE.md §Repository Layout)
- Data directory layout (from ARCHITECTURE.md §Data Directory)

Links to each subsystem page.

### /docs/gateway

Package: `gateway/`, `queue/`, `router/`
Entrypoint daemon: `gated/`
Tables owned: `routes`, `groups`, `router_state`,
`sessions`, `session_log`, `system_messages`

Content:

- The message loop: poll interval, SQL query, what "processed" means
- Route resolution: longest-prefix JID matching, 7 rule types in order,
  service routes vs group routes, template routing with `{sender}`
- impulseGate: weight-based batching, social verbs, hold timeout
- Commands intercepted before dispatch: /new, /ping, /chatid, /stop, /status
- Job queue: per-group serialization, global concurrency cap,
  circuit breaker (3 failures), pending queue
- Output processing: ARIZUKO_OUTPUT_START/END markers, `<think>` stripping,
  `<status>` extraction, FormatOutbound
- Session management: session ID in DB, error eviction, cursor rollback
- Prefix dispatch: `@name` → named group, `#topic` → topic session
- Topic sessions: composite PK (group_folder, topic), /new behavior
- Web JIDs: activeWebJIDs poll, processWebTopics

### /docs/store

Package: `store/`
Tables owned: all (store is the shared DB layer)

Content:

- WAL mode, 5s busy timeout, PRAGMA user_version migrations
- Full schema table (all 13 tables, key columns, purpose)
- Migration pattern: each daemon owns a service name, runs its own
  migrations at startup
- Outbound audit trail: source/group_folder columns in messages
  populated by `PutMessage` (unified inbound/outbound, v0.25)
- Outbound writes: agents, MCP, scheduler, control all flow through
  `PutMessage` with `is_bot_message=1`

Note: link to ARCHITECTURE.md §SQLite Schema for column details.

### /docs/container

Package: `container/`, `groupfolder/`, `mountsec/`

Content:

- Full lifecycle: 12 steps from EnsureRunning to StopSidecars
- Volume mount table: all 8 mount types (host path → container path → rw)
- Input JSON schema: all fields of ContainerInput/Input struct
- Output protocol: marker lines, JSON shape, streaming across turns,
  IPC poll loop inside container, `_close` sentinel
- seedSettings: what goes into settings.json (env block, MCP servers)
- seedSkills: one-time copy, non-destructive, migration path
- Sidecars: docker run -d lifecycle, socket path, settings.json wiring
- Security flags: --cap-drop ALL, --security-opt no-new-privileges,
  --memory 1g, --cpus 2
- mountsec: allowlist JSON, blocked patterns, path resolution rules
- Docker-in-docker path translation: HOST_DATA_DIR, hp() function
- Container naming convention: arizuko-<folder>-<timestamp_ms>
- Skills migration: MIGRATION_VERSION file, /migrate skill, NNN-desc.md
- Error handling: with output vs without output cursor behavior

### /docs/ipc

Package: `ipc/`

Content:

- Role: MCP server on unix socket per group, started before docker run
- Identity resolution: socket path → folder, folder depth → tier
- Tier table: 0=root, 1=world, 2=agent, 3+=worker with example folders
- The socat bridge: exact command in settings.json, what it does
- Full tool table: all 20 tools, domain, gating (grants / tier / both)
- Request flow: walk through a single tool call end-to-end
- Stateless: ipc owns no tables, identity from filesystem
- Lifecycle: server starts before docker run, socket cleaned up after

### /docs/auth

Packages: `auth/`, `grants/`

Content:

- Two separate systems in one section:

  **MCP authorization (tier + grants)**
  - Tier-based tool gating: only get_grants/set_grants (tier ≤ 1),
    refresh_groups (tier ≤ 2)
  - Grants: rule syntax `[!]action[(param=glob,...)]`, last-match-wins
  - Default grant derivation by tier (0: `*`, 1: all management + platform
    send, 2: send only, 3+: send_reply only)
  - NarrowRules: delegation can only restrict; child cannot widen parent
  - Grant injection into start.json, manifest filtering
  - Ownership checks: task pause/resume/cancel, set_routes, delegate_group

  **Web authentication**
  - local username/password, argon2, JWT (1h), refresh rotation
  - OAuth: GitHub, Discord, Telegram Login Widget
  - Route table: all /auth/\* endpoints
  - Cookie behavior: HttpOnly, SameSite, Secure flag on HTTPS
  - Rate limiting: 5 POST /auth/login per IP per 15-min window
  - User management CLI: arizuko config <instance> user ...

### /docs/channels

Packages: `chanreg/`, `api/`

Content:

- Channel protocol: 3-endpoint contract (register, inbound, outbound)
- Registration payload: name, callback URL, JID prefixes, capabilities
- Inbound flow: POST /v1/messages → store.PutMessage
- Outbound flow: HTTPChannel.Send → POST /send to adapter
- Health: 30s ping to /health, 3 failures = auto-deregister, outbox queuing
- Auth: CHANNEL_SECRET for registration, session token for subsequent calls,
  shared secret for router→adapter calls
- chanlib: shared Go library for all adapters (RouterClient, InboundMsg,
  auth middleware)
- receive_only capability: onbod uses this (receives commands, no inbound)
- Channel deregistration and reconnection behavior
- Service routes: targets without `/` look up channels table by name

### /docs/adapters

One page covering all adapters. Each adapter gets a short block:

For each adapter: transport mechanism, auth model, what JID prefixes
it handles, any notable behavior.

| Adapter | Transport                              | Platform API       |
| ------- | -------------------------------------- | ------------------ |
| teled   | Telegram Bot API (go-telegram-bot-api) | polling            |
| discd   | Discord Gateway WebSocket              | gateway intents    |
| mastd   | Mastodon WebSocket streaming + REST    |                    |
| bskyd   | AT Protocol polling                    |                    |
| reditd  | Reddit OAuth2 inbox + subreddit poll   |                    |
| emaid   | IMAP TLS polling + SMTP STARTTLS       | thread-id tracking |
| whapd   | TypeScript, platform-specific          |                    |

Note: all Go adapters share chanlib for auth middleware and router client.

### /docs/memory

Packages: `diary/`, `container/` (episodes.go, runner.go)

Content:

- Two-layer model: MEMORY.md (knowledge, <200 lines) + diary (work log)
- Diary: path `diary/YYYYMMDD.md`, YAML frontmatter with `summary:`,
  HH:MM entries ≤250 chars, max 5 bullets in summary
- Gateway diary injection: diary.Read() → up to 14 entries → XML block
- Episodes: day/week/month hierarchy, summary: and type: in frontmatter,
  ReadRecentEpisodes() → up to 3 per type level
- Facts: `facts/<slug>.md`, researched via agent skill, 14-day staleness
- Users: `users/<platform-id>.md`, name + first_seen + Recent section
- User context injection: UserContextXml() per sender
- PreCompact hook: transcript → conversations/ archive, diary update nudge
- Skills as plain files: SKILL.md in ~/.claude/skills/<name>/, loaded
  automatically by Claude Code at session start
- Migration system: MIGRATION_VERSION file, /migrate skill (root only)
- Skill map table: all skills, what files they write, what they read,
  when they trigger
- Session continuity: agent self-reads diary on new session per CLAUDE.md

Note on unimplemented features:

- get_history IPC action referenced by recall-messages skill: not implemented
- recall binary (v2 FTS5+vector) referenced in skill: not present in image,
  skill falls back to grep (v1)

### /docs/scheduler

Package: `timed/` (daemon), `ipc/` (tool handlers)

Content:

- Loop: 60s poll, due tasks → INSERT into messages table
- Task schema: id, owner, chat_jid, prompt, cron, next_run, status
- Cron expressions: robfig/cron 5-field format
- Interval mode: integer cron field = milliseconds interval
- One-shot: NULL cron, next_run set directly, goes NULL after firing
- context_mode: "group" (resumes session) vs "isolated" (fresh context,
  sender prefix "scheduler-isolated")
- MCP tools: schedule_task, list_tasks, pause_task, resume_task, cancel_task
- task_run_logs: execution history table (task_id, run_at, duration_ms,
  status, result, error)
- Authorization: owner field, auth.Authorize for pause/resume/cancel

### /docs/onboarding

Package: `onbod/` (daemon)

Content:

- When it activates: ONBOARDING_ENABLED=true in .env
- Gateway hook: unrouted JID → INSERT OR IGNORE into onboarding table
- State machine: awaiting_name → pending → approved | rejected
- Poll loop: 10s interval, what each state does
- Name validation: lowercase, a-z0-9-, no collision with existing folders
- /approve and /reject: how they're routed (prefix routes in routes table
  pointing to "onbod" service), tier-0 enforcement
- What /approve creates: group dir, prototype copy, DB row, default routes,
  welcome system message (XML format)
- Prototype copy: CLAUDE.md and SOUL.md only, session/memory excluded
- Outbound: onbod POSTs to channel adapter /send endpoint via chanlib

### /docs/dashboard

Package: `dashd/` (daemon)

Content:

- Six views: portal (tile grid 30s refresh), status (channels/groups/
  containers/queue/errors), tasks (scheduled + run history), activity
  (message flow + routing table), groups (hierarchy tree), memory
  (per-group knowledge browser)
- Opens SQLite read-only (?mode=ro)
- URL conventions: /dash/ portal, /dash/<name>/, /dash/<name>/x/<fragment>
  HTMX partials, /dash/<name>/api/<path> JSON
- Auth: JWT cookie (imports auth library)
- /status command routing: dashd registers receive_only, /status routes to it
- Port: DASH_PORT default 8090

### /docs/web

Package: `proxyd/` (daemon), `webd/` (channel adapter)

Content:

- proxyd role: auth perimeter, routes /auth/_ locally, /dash/_ to dashd,
  /\* to webd or vited
- Auth planes: JWT plane (X-User-Sub, X-User-Groups) and slink plane
  (X-Folder, X-Group-Name, X-Slink-Token)
- Web JIDs: `web:<folder>` format, processWebTopics per-topic agent runs
- Slink tokens: groups.slink_token, rate-limited 10 req/min per IP
- user_groups table: restricts web user to specific folder list, embedded in JWT
- WebDAV: WEBDAV_ENABLED=true, /dav/ → dufs container, read-only groups/ mount
- vited: Vite dev server fallback when WEBD_ADDR not set
- OAuth providers enabled by which env vars are present (GOOGLE_CLIENT_ID,
  GITHUB_CLIENT_ID, DISCORD_CLIENT_ID)

### /docs/compose

Package: `compose/`

Content:

- compose.Generate(): derives project name from data dir basename
- Container naming: arizuko*<daemon>*<flavor> convention, why it matters
  for multi-instance hosts
- services/\*.toml: user-defined service entries, how they become containers
- Auto-included: onbod when ONBOARDING_ENABLED=true
- Port conventions: dashd :8090, onbod :8092 (compose) vs :8091 (standalone)
- HOST_DATA_DIR and HOST_APP_DIR for docker-in-docker path translation

### /docs/cli

Binary: `cmd/arizuko/`

Content:

- arizuko create <name>: seeds data dir, .env, default group
- arizuko run <instance>: generate compose + docker compose up
- arizuko generate <instance>: write docker-compose.yml only
- arizuko group <instance> list|add|rm: group management
- arizuko status <instance>: show compose services and channels
- arizuko config <instance> user list|add|rm|passwd: user management
- arizuko chat [group] [flags]: CLI chat mode (see cli-chat.md spec)
  - --new, --instance, --data, --no-ipc flags
  - session resume from cli-session.json
  - ant as short alias

---

## 4. Cookbooks Spec

### Purpose

Each cookbook is a self-contained walkthrough of a specific task.
This is where design rationale lives. The "why" of architecture
is shown through examples, not explained abstractly.

### Format rules

Each cookbook:

- Opening: one sentence stating what this accomplishes
- Prerequisites: what must be in place
- The task: numbered steps
- Code/config blocks where relevant
- "How it fits the system": 1-3 paragraphs explaining the design
  forces that make this work the way it does. This is the shill.
- Links to relevant docs pages

Cookbooks are not tutorials for beginners. They assume the reader
can read Go and understand HTTP. They skip explaining `curl`.

### Cookbook list

---

**add-channel-adapter**
What: build a minimal adapter that connects a new platform to the gateway.
Demonstrates: the 3-endpoint channel protocol, chanlib usage, registration
flow, JID prefix design.
Design angle: show that the gateway never imports adapter code — the
contract is pure HTTP. A working adapter is ~200 lines in any language.
Show the Go version using chanlib. Note that the TypeScript version
(whapd) follows the same 3-endpoint protocol.

---

**custom-mcp-tool**
What: add a new MCP tool that agents in one group can call.
Demonstrates: ipc.go tool registration, GatedFns injection, grants gating,
how the tool appears in the agent's manifest.
Design angle: agents call tools. Tools are gateway callbacks. The
boundary is clean: ipc.go is the only place tool definitions live.
The unix socket is the transport. Socat is the bridge. No HTTP, no
public API.

---

**two-telegram-instances**
What: run two separate arizuko instances on the same host, each with
its own Telegram bot.
Demonstrates: compose project naming, container naming convention,
data dir isolation, how multiple instances avoid conflicts.
Design angle: the flavor suffix in container names (`arizuko_gated_foo`
vs `arizuko_gated_bar`) is what prevents collisions. The data dir is
the instance. Everything else derives from it.

---

**per-user-agents**
What: configure an atlas-style routing hub that spawns a separate
agent per user, auto-creating from a prototype.
Demonstrates: template routing with `{sender}`, prototype/ directory,
max_children limit, spawnFromPrototype, how the world hierarchy
(tier 0 → tier 1 hub → tier 2 per-user) gives each user isolation.
Design angle: the routing table is the multiplexer. One rule
(`target=atlas/{sender}`) causes the gateway to create and route to
hundreds of distinct agents without any custom code.

---

**memory-skills-standalone**
What: use arizuko's memory skills (diary, facts, recall) with plain
Claude Code CLI, no gateway.
Demonstrates: which skills are portable (diary, facts, recall-memories,
compact-memories, users), which require the gateway (recall-messages,
schedule_task), what the CLAUDE.md snippets look like.
Design angle: the memory system is markdown files and agent instructions.
The gateway injection is an optimization (avoids extra turns). Without
it, the agent reads diary itself on session start. The skills work
because Claude Code loads SKILL.md files from ~/.claude/skills/.
Reference: specs/memory-skills-standalone.md for full detail.

---

**cli-chat-mode**
What: run an agent group interactively from the terminal using
`arizuko chat`.
Demonstrates: arizuko chat command, session resume, how container.Run()
is reused, what gets stripped (DB, gateway loop, channel adapters).
Design angle: the container is self-contained. The gateway is not
required to run an agent. The IPC poll loop inside the container handles
multi-turn without a message queue — the host writes JSON files to the
IPC input directory. Same container image, different plumbing around it.
Reference: specs/cli-chat.md for full detail.

---

**scheduled-tasks**
What: create a recurring task that runs a prompt against an agent on a
cron schedule.
Demonstrates: schedule_task MCP tool call, cron expression format,
context_mode options (group vs isolated), how timed inserts into
messages table, how gated picks it up.
Design angle: the scheduler is just another producer on the messages
table. Scheduled tasks arrive at the gateway exactly like user messages.
No special handling required — the message loop is the single intake.

---

**onboarding-flow**
What: enable self-serve onboarding so new users can request their own
agent world without operator involvement in each step.
Demonstrates: ONBOARDING_ENABLED flag, onbod state machine, /approve
and /reject commands, prototype copy, welcome system message.
Design angle: onbod is a channel adapter, not a gateway plugin. It
registers itself, receives commands via the same route table any
adapter uses, and speaks the same outbound protocol. The gateway does
not know onboarding exists — it just routes unmatched JIDs to the
onboarding table when the flag is set.

---

**grant-rules**
What: restrict what MCP tools a child group can call, and verify
the restriction holds.
Demonstrates: default tier-based grants, NarrowRules behavior,
set_grants MCP tool (tier 0-1 only), how grants appear in start.json,
how the agent manifest is filtered.
Design angle: grants are capabilities, not permissions. A group receives
rules at spawn time injected into start.json. The agent sees only tools
it is allowed to call. The rules are last-match-wins: composing
restrictions is appending deny rules.

---

**crackbox-backend** (mark as planned / not yet shipped)
What: use QEMU/KVM VMs (via crackbox) instead of Docker containers
for agent isolation.
Demonstrates: the Backend interface abstraction, QEMU vs Docker
tradeoffs, VM lifecycle, 9p/virtio-fs mounts, MCP socat TCP bridge.
Design angle: the container package is the only place Docker is touched.
Abstracting it to a Backend interface lets you swap the execution
substrate without changing anything above it. The input/output protocol
(JSON on stdin, markers on stdout) stays the same regardless of backend.
Reference: specs/crackbox-sandboxing.md for full detail.

---

## 5. Style Guide

### Tone

- Address the reader as an engineer, not a user.
- Describe what code does, not what it "provides" or "enables".
- Prefer specific over general: "socat STDIO UNIX-CONNECT:/workspace/ipc/router.sock"
  beats "a bridge to the socket".
- Active voice. Short sentences.
- No hedging: "might", "could be", "may want to". Say what it does.
- Numbers are better than adjectives: "14 days of diary", not "recent diary".

### What each section type includes/excludes

| Section   | Include                                         | Exclude                                           |
| --------- | ----------------------------------------------- | ------------------------------------------------- |
| Landing   | system model, selling points, quick start       | tutorials, design rationale, future roadmap items |
| Docs      | mechanics, types, flows, edge cases, cross-refs | why choices were made, history, future plans      |
| Cookbooks | step-by-step, design rationale, system fit      | reference tables, schema detail (link to docs)    |

### Formatting rules

- Code blocks for all commands, JSON, SQL, HTTP endpoints.
- Tables for: tool lists, schema, option lists with 3+ items.
- No numbered sections in landing (prose flows). Numbered steps only
  in cookbook walkthrough sections.
- Heading depth: landing uses H2 max. Docs uses H2 (section) + H3
  (subsection). Cookbooks: H2 for each cookbook, H3 inside.
- Links: use relative paths. Every docs page links to relevant cookbooks
  at the bottom. Every cookbook links to the docs pages it touches.

### What to include in docs pages

Always:

- Package name(s)
- Tables owned (if any)
- How it receives input / produces output
- What invariants hold (e.g. "session eviction always rolls back cursor")

Often:

- Key types or structs (by name, not full definition)
- Sequence: numbered steps when order matters
- Edge cases that would surprise a reader

Never:

- Why the design was chosen (→ cookbook)
- History of changes (→ .diary/)
- Future plans (→ specs/)
- Comments on what would make it better

### On unimplemented features

If a feature is referenced in code or skills but not implemented,
note it explicitly: "not yet implemented", "skill falls back to X".
Do not hide gaps. Builders need accurate information.

Specifically:

- get_history IPC action: called by recall-messages skill, not in ipc.go
- recall binary (v2): referenced in skill, not in container image

---

## 6. Open Questions

**OQ-1: Stat strip numbers**
The existing landing page shows stats that may not match the current
codebase (it references 16 tools; the current IPC spec lists 20).
Before publishing, verify: package count, LOC, test count, tool count,
table count, adapter count. Use the actual codebase, not the old page.

**OQ-2: Is `arizuko chat` implemented?**
The cli-chat.md spec is written but it is a design spec, not a
confirmation that the `chat` subcommand exists in `cmd/arizuko/main.go`.
Check before including it in docs as a shipped feature vs planned.

**OQ-3: Is `emaid` shipped?**
ARCHITECTURE.md includes emaid in the daemon list. The CLAUDE.md layout
also includes it. But no spec page was found for it in `specs/`.
Verify it exists as a binary and its implementation status.

**OQ-4: Is `whapd` shipped?**
Listed as TypeScript daemon. Verify it is operational and that the
3-endpoint protocol is fully implemented.

**OQ-5: Is `discd` shipped?**
The old landing page's LLM context block marks discd as "planned". The
current ARCHITECTURE.md lists it without a status note. Verify.

**OQ-6: AGPL vs MIT**
README says MIT. The memory-skills-standalone spec notes the codebase
may be AGPL (citing .diary/20260323.md). Resolve before publishing
the standalone memory skills cookbook — the license statement matters
for adopters who want to extract the skills.

**OQ-7: GitHub repo URL**
The existing landing page links to `github.com/REDACTED/arizuko`. The
README says `github.com/onvos/arizuko`. Verify canonical URL.

**OQ-8: Web chat (webd) status**
The web layer (proxyd + webd) is listed as "partial" in specs/index.md.
The docs site should clearly mark the web channel as partial/in-progress
rather than implying it is fully operational.

**OQ-9: Crackbox cookbook scope**
The crackbox sandboxing spec is detailed but the backend abstraction
(the Backend interface) does not yet exist in the codebase. Decide
whether the cookbook should be published as a design document labeled
"planned" or held until the interface exists.

**OQ-10: proxyd vs webd naming**
specs/6/2-proxyd.md notes the rename from webd to proxyd, and that a
separate webd channel adapter exists. The docs page /docs/web covers
both. Verify the actual binary names in the codebase match what the
docs will call them.

---

## Shill Angles — Landing Page

### 1. The Core Insight Angle

**One-liner**: The schema is the contract. Not the API, not the framework — the table.

Every daemon in arizuko shares one SQLite database in WAL mode. `timed` inserts
a row into `messages`. `gated` polls `messages`. That is the entire scheduler
integration. There is no scheduler API. There is no event bus. There is no
publish/subscribe. There is a table, and every process knows how to read it.

This sounds obvious until you realize what it eliminates: version negotiation,
serialization formats between services, service discovery, message broker ops.
When you want to add a new producer (say, a webhook ingestion daemon), you write
to `messages`. The gateway picks it up in the next 2-second poll. Zero
coordination surface.

The channel protocol is the same insight applied to the boundary: three HTTP
endpoints and a shared secret. That is the entire contract between an adapter
and the gateway. You can write a working Telegram adapter in 200 lines of shell.

---

### 2. The "Why Not X" Angles

**Why not LangChain / LlamaIndex / CrewAI?**

Those frameworks assume you are building an application in Python. You import
them. They own the runtime. When you want multi-channel delivery, you write glue
code around their primitives. When you want persistent memory, you configure
their vector store. When you want to run agents concurrently for different users,
you manage that yourself.

Arizuko is not a Python library. It is a message router. You wire it to whatever
channels exist. The agents run as Docker containers — they are isolated processes
with their own filesystem, not objects instantiated inside your application.
The memory is plain markdown files, not a vector database you pay for per query.

**Why not a Discord bot framework (discord.js, hikari, etc.)?**

A Discord bot framework assumes Discord. You get one channel, one platform,
one deployment topology. Adding Telegram means a second codebase. Adding
email means a third. The routing logic lives in each adapter separately.

Arizuko separates the routing from the delivery. Add a Telegram adapter and
a Discord adapter and a Mastodon adapter — the routing table, the agent memory,
the scheduling, the grant rules — all of it is shared. The agents do not know
which channel a message came from. They just answer.

**Why not a SaaS chatbot platform (Botpress, Voiceflow, Intercom)?**

SaaS platforms own your data, your agent logic, and your deployment. When you
want to run agents that can spawn subagents, call custom MCP tools, or access
your internal systems via a unix socket, you are outside the platform's sandbox.
You are writing webhooks into someone else's infrastructure.

Arizuko runs on a $5 VPS. Your agent memory is a folder on disk. Your MCP tools
are Go functions you register in one file. Your data is a SQLite database you
can open with any SQL client. Nothing is behind an API you do not control.

---

### 3. The Composability Angle

Arizuko is not monolithic. You can use pieces without the whole.

**The memory skills**: Five SKILL.md files that work with plain Claude Code CLI,
no gateway required. Drop them in `~/.claude/skills/`. They give any Claude Code
session diary, facts, user memory, and episode compression. No Docker, no router,
no database. The gateway is an optimization that injects summaries so the agent
does not spend turns reading files — but the files work without it.

**The channel protocol**: Three HTTP endpoints. If you have an existing system
and just want to route inbound messages to a Claude agent and get responses back,
implement `POST /send` and call `POST /v1/messages`. You do not need the
Telegram adapter, the scheduler, or the dashboard.

**The container package**: `container.Run()` is a function. It takes an input
struct, fires a Docker container, and returns a result. The CLI chat mode
(`arizuko chat`) uses it directly, skipping the gateway, the DB, and the
adapter stack entirely. Same container image. Different plumbing around it.

This is unlike monolithic agent frameworks where everything is entangled:
memory, execution, tooling, and delivery are all coupled through the framework's
object model. In arizuko, each concern is a daemon, a package, or a file. You
pull out the part you want.

---

### 4. The "Phone as a Deployment Tool" Angle

Arizuko was designed to be fully operated from a Telegram conversation.

This is not a convenience feature. It is a design constraint that enforces
real discipline. Every operation that matters must be expressable as a message.
Check which containers are running: message. Approve a new user: `/approve jid`.
Trigger a one-shot task: message. Read the dashboard: `/status`. Restart an
agent session: `/new`. All of it works from a phone with no SSH, no terminal,
no VPN.

What this says about the design: there is no "ops console" that only works on
a desktop. There is no configuration UI that assumes you have a keyboard. The
routing table, the group hierarchy, the scheduled tasks — all of it is readable
and writable through the same channel the agents use.

For builders: this means every administrative action you build into the system
is also a programmable action. The operator dashboard is useful, but it is not
the control plane. The message interface is the control plane. Agents can use
it too.

---

### 5. The SQLite Angle

SQLite is not a limitation. It is the correct choice, and choosing it says
something about every other architectural decision.

Most people would reach for Redis for a message queue, Kafka for multi-producer
ingestion, and Postgres for the relational data. That is three moving parts,
three things to backup, three things to monitor, three schemas to keep in sync.

Arizuko has one file: `messages.db`. WAL mode means multiple readers can query
while the writer is active — the gateway and the scheduler read the same DB
concurrently without locking problems. Migration is `PRAGMA user_version`. The
entire schema is readable in one table in ARCHITECTURE.md.

The tradeoffs are real: SQLite will not scale to millions of messages per second.
But an agent system is not a metrics pipeline. It is a low-volume, high-latency
system where the bottleneck is the agent container runtime, not the database.
At arizuko's actual throughput (dozens to hundreds of messages per minute per
instance), SQLite has headroom that most deployments will never exhaust.

The bold claim: if your agent platform needs Kafka, something is wrong with your
architecture, not your queue.

---

### 6. The "Agent Is Just a Process" Angle

Most AI agent frameworks require you to import an SDK. The SDK owns the event
loop. The SDK manages the tool calls. The SDK talks to the API. You write your
logic inside the SDK's callbacks.

Arizuko treats Claude as a black-box process: spawn it, write JSON to stdin,
read output from stdout, kill it when done. The gateway has zero imports from
the Claude SDK. It does not know what model is running. It does not know what
tools the agent called. It knows: input went in, output came out, here is the
session ID for next time.

This has a concrete consequence: the agent container is swappable. The current
image uses Claude Code CLI via the Agent SDK. Tomorrow you could swap in a
container that runs a different model, a different CLI, or a different execution
pattern — as long as it speaks the same marker protocol on stdout. The gateway
does not care.

It also means the gateway is not on the critical path of the agent's reasoning.
The agent calls MCP tools by connecting to a unix socket that the gateway
started before the container launched. The gateway responds to those calls. But
the agent's context window, its tool use sequence, its multi-step reasoning —
none of that touches the gateway. The gateway is infrastructure, not a
co-reasoner.

---

### 7. The Multitenant Angle

One server. Multiple instances. Multiple bots. Multiple channels. Zero
namespace conflicts.

Each arizuko instance is a directory: `/srv/data/arizuko_<name>/`. The directory
contains the config, the database, the group folders, and the session state.
Running two instances means two directories and two `docker compose up` calls.
Container names are `arizuko_gated_<flavor>`, `arizuko_timed_<flavor>` — the
flavor suffix prevents Docker name collisions on the host.

Compare this to running N separate bots: N separate codebases, N separate
databases, N separate routing configurations, N separate memory systems. When
you want to add a new channel to all of them, you touch N deployments. When you
want a shared memory pool across a group hierarchy, you are building that
yourself.

In arizuko, a group hierarchy is just folder depth. `groups/root/` is the root.
`groups/root/alice/` is a child agent for Alice. They share the same database,
the same channel delivery infrastructure, and the same MCP tools. Alice's agent
is isolated (it has its own folder, its own session, its own grants) but it
lives inside the same operational footprint as the root. One instance, many
tenants, one deploy to maintain.

---

### 8. Hero Statement Candidates

1. **The schema is the contract. Each group is a directory. Agents are
   containers. That's it.**

2. **A Claude agent router built from Go, SQLite, and Docker. No framework.
   No broker. No magic.**

3. **Routes messages to containerized Claude agents. Memory is a folder.
   Channels register themselves. The database is one file.**

4. **Seven channel adapters, twenty MCP tools, one SQLite database. Everything
   else is your code.**

5. **Self-hosted Claude agents that remember things, answer messages from
   anywhere, and run on whatever you have.**

---

## Shill Angles — Cookbooks

For each cookbook: the architectural insight it demonstrates, the
before/after story, and one memorable line.

---

### add-channel-adapter

**Insight**: The channel boundary is the only place where platform
specifics exist. The gateway never imports adapter code. The contract
is three HTTP endpoints: `POST /v1/channels/register` on startup, then
`POST /v1/messages` inbound, then `POST /send` outbound. `chanreg.Registry`
is a name→URL map keyed on the adapter's self-reported name.
`HTTPChannel.Owns(jid)` does a prefix scan over the JID prefixes the
adapter declared at registration. Nothing else crosses the boundary.

**Before/after**: Before this design, adding a new platform means
modifying the router — new imports, new config keys, new message
parsing, deployment restart. After: write a standalone daemon in any
language, POST one registration payload, and the gateway discovers it
at runtime without restarting. A working Go adapter using `chanlib` is
~200 lines: `rc.Register(...)`, poll platform, call
`rc.PostMessage(...)`, serve `/send` and `/health`. That is the whole
adapter. The TypeScript WhatsApp adapter (`whapd`) follows the same
three-endpoint contract despite being a completely different language
and runtime.

**Memorable line**: The gateway never imports adapter code. If it speaks
three endpoints, it is a channel.

---

### two-telegram-bots-one-gateway

**Insight**: Two `teled` instances with different `CHANNEL_NAME` values
register as distinct channels. The JID prefixes are per-registration:
one adapter declares `["telegram-personal:"]`, the other declares
`["telegram-work:"]`. The gateway routes by longest-prefix JID match.
The adapter owns the prefix — it declares it at registration, not in
the gateway config. The compose flavor suffix (`arizuko_teled_foo` vs
`arizuko_teled_bar`) prevents container naming collisions on the host.

**Before/after**: Before: running two bots on one host requires two
gateway deployments, or a custom multiplexer, or platform dispatch
logic inside the core. After: spin up a second `teled` container with a
different name and JID prefix. Zero gateway changes. The routing table
resolves them independently because their message JIDs differ by prefix.
Each bot routes to a different group via the normal rule engine.

**Memorable line**: The JID prefix is the routing contract. The adapter
declares it; the gateway follows it. Two adapters never collide because
they declare different prefixes.

---

### memory-skills-standalone

**Insight**: The memory system is five SKILL.md files and two sections
of CLAUDE.md. Claude Code loads `~/.claude/skills/*/SKILL.md`
automatically at session start. The diary skill writes
`diary/YYYYMMDD.md`. The facts skill spawns a research subagent, writes
`facts/<topic>.md`. Recall greps `summary:` frontmatter across all
stores. None of this requires arizuko. The gateway adds one
optimization: before the container runs it calls `diary.Read()` and
`ReadRecentEpisodes()`, reads the same frontmatter files, and prepends
summaries to the prompt. Without the gateway, the agent spends one extra
turn reading diary itself — the CLAUDE.md instructs it to.

**Before/after**: Before: persistent agent memory requires a vector
store with an API, an embedding pipeline, and custom retrieval logic.
After: copy five markdown files to `~/.claude/skills/`, add two sections
to CLAUDE.md. The session has diary, facts, user profiles, and episode
compression. No server, no npm install. Backup is `cp -r`. The most
powerful feature is plain text.

**Memorable line**: The gateway injection is an optimization, not a
requirement. The agent reads its own diary — the CLAUDE.md just tells
it to.

---

### cli-chat-mode

**Insight**: Strip away the gateway, the store, the channel adapters,
the queue, and the routing rules. What remains is one call:
`container.Run()`. CLI chat mode reuses this function unchanged. The
`OnOutput` callback — which in gateway mode routes text to a channel
adapter — is replaced with a function that prints to stdout. The
DB-backed session store is replaced with a single JSON file. Multi-turn
works because the container's IPC poll loop already handles follow-up
messages: after each query completes it polls
`/workspace/ipc/input/*.json`. The host writes JSON files there. The
container picks them up at the next 500ms poll. No new protocol, no new
container image.

**Before/after**: Before: using the arizuko container requires a running
gateway, a SQLite store, and a registered channel adapter just to get a
response. After: `arizuko chat root` (or `ant`) launches an agent
directly from the terminal with full memory, MCP tools, and session
resume. The agent does not know it is running in CLI mode.

**Memorable line**: The container is self-contained. The gateway is
optional plumbing.

---

### custom-mcp-tool

**Insight**: `ipc.go` is the single file where MCP tool definitions
live. `ServeMCP()` takes two structs of function pointers — `GatedFns`
and `StoreFns` — and builds the tool manifest from them. Adding a tool
means adding a field to `GatedFns` and a registration call in
`buildMCPServer()`. The tool is then available to every agent running in
any group, filtered by the grant rules injected into `start.json`. The
agent calls the tool, the unix socket carries it to the gateway process,
the gateway executes the function. No HTTP, no public API. The transport
is `socat STDIO UNIX-CONNECT:/workspace/ipc/router.sock`.

**Before/after**: Before: extending what an agent can do requires either
prompt engineering (unreliable) or deploying a separate MCP server and
manually wiring it into each agent's settings. After: add a field to
`GatedFns`, register it in `ipc.go`, rebuild. Every agent sees the new
tool in its manifest immediately. Tier and grant rules gate access
without any additional logic in the tool handler.

**Memorable line**: The unix socket is the gateway↔agent contract. The
only place to add tools is `ipc.go`.

---

### multi-tenant-deploy

**Insight**: `compose.Generate()` derives the project name from the
data directory basename and produces container names of the form
`arizuko_<daemon>_<flavor>`. The flavor is the part of the directory
name after the first underscore — so `/srv/data/REDACTED` produces
`arizuko_gated_REDACTED`, `arizuko_timed_REDACTED`, and so on. Two instances
on the same host produce non-overlapping names. There is no central
registry. Each instance is fully described by its data directory: one
`.env`, one `store/messages.db`, one `groups/` tree. Backup is `tar`.
Migration is `cp -r`. Teardown is `docker compose down` then `rm -rf`.

**Before/after**: Before: running multiple isolated agent deployments on
one host requires manual docker-compose editing, explicit port
assignment, and careful namespace management to avoid container name
conflicts. After: `arizuko create bar`, edit `.env`, run `arizuko run
bar`. The naming convention handles isolation. Every configuration
variable flows from `.env` into the compose file at generation time.

**Memorable line**: The data directory IS the instance. Everything else
derives from it.

---

### scheduled-tasks-as-messages

**Insight**: `timed` opens the same SQLite database as `gated` in WAL
mode. When a task fires, it executes one query:
`INSERT INTO messages (chat_jid, sender, content, ...)`. That is the
entire scheduler output. The gateway's 2-second poll loop picks it up
exactly as it would pick up a Telegram message — same `store.NewMessages`
query, same routing, same queue, same container spawn. The sender field
is `"scheduler"`. The routing table resolves the `chat_jid`. Nothing in
the gateway is scheduler-aware.

**Before/after**: Before: scheduled tasks need a special dispatch path —
the scheduler calls the gateway directly, manages its own concurrency,
handles session state separately. After: the scheduler is a 60-line poll
loop that writes rows. Every guarantee the gateway gives to user
messages — queuing, concurrency cap, circuit breaker, session resume,
grant enforcement — applies automatically to scheduled messages because
they are structurally identical.

**Memorable line**: The scheduler does not talk to the gateway. It talks
to the database, and the gateway's poll loop does the rest.

---

### onboarding-flow

**Insight**: `onbod` is a channel adapter with a state machine. It
starts by calling `POST /v1/channels/register` — the same call every
other adapter makes. It then writes `/approve` and `/reject` command
routes directly into the routes table. When `ONBOARDING_ENABLED=true`
and an unrouted JID sends a message, the gateway inserts a row into the
`onboarding` table and stops — it does not route, does not spawn a
container. `onbod`'s 10-second poll loop finds those rows, sends
prompts via its own outbound `/send` endpoint, and runs the state
machine: `awaiting_name` → `pending` → `approved`. Approval creates the
group directory, inserts the `groups` row, and seeds the
routes. The gateway code was not changed.

**Before/after**: Before: onboarding logic lives inside the router.
Changing the onboarding flow — different prompts, different validation,
different approval criteria — means touching core gateway code, testing
gateway behavior, redeploying. After: `ONBOARDING_ENABLED=true` in
`.env`, `onbod` is a compose service. The entire flow lives in one
standalone binary. Swap it out without touching `gated`.

**Memorable line**: The gateway does not know what onboarding IS. It
just inserts a row when a JID has no route.

---

### grants-and-auth

**Insight**: `DeriveRules()` computes the default grant list from folder
depth alone — tier 0 gets `["*"]`, tier 1 gets platform send actions
plus management tools, tier 2 gets send only, tier 3+ gets
`["send_reply"]`. These defaults are computed at container spawn time
and injected into `start.json`. The MCP manifest sent to the agent is
filtered to show only the tools permitted by those rules. The agent
cannot call `set_routes` if it is not in the manifest. `NarrowRules`
enforces one invariant: deny rules always pass through; allow rules only
pass through if the parent already permits them. A child cannot grant
itself what its parent cannot do.

**Before/after**: Before: per-group tool restrictions require an ACL
table, a permission check in every tool handler, and admin UI. After:
the folder depth sets the default. An agent with `set_grants` access
appends deny rules to narrow its children further. The child agent sees
a smaller manifest — no failed calls, no 403 errors, just fewer tools.
Rules are strings in a list, not code in the handlers.

**Memorable line**: The agent does not see tools it cannot call. The
manifest is the policy.

---

### memory-skills-in-production

**Insight**: The full memory loop has four steps that compose without
coordination. (1) User says something notable — agent calls `/users` to
append to `users/<id>.md`. (2) Something is decided — agent calls
`/diary`, appends a timestamped entry, rewrites the `summary:` YAML
frontmatter to ~5 bullets. (3) Next container run — gateway calls
`diary.Read(groupDir, 14)`, extracts `summary:` fields, prepends up to
14 days of context as `<knowledge layer="diary">` before the agent
ever sees the user prompt. (4) Over weeks — `compact-memories` (on
cron via `schedule_task`) compresses daily sessions into weekly
episodes, then monthly; `recall-memories` greps `summary:` fields
across all levels. The PreCompact hook fires before context compaction
and nudges the agent to write a diary entry first, ensuring continuity
survives context window resets.

**Before/after**: Before: persistent agent memory across sessions
requires a vector database, an embedding pipeline, and retrieval logic
wired into the prompt. After: files in a mounted directory, a `summary:`
YAML field, and the 30-line `diary.Read()` function. The recall system
is grep on frontmatter. Compression is the agent summarizing its own
files. Backup is `cp -r`. The whole memory layer survives the container
being destroyed and recreated because it lives on the mounted volume,
not inside the container.

**Memorable line**: Memory is a skill, not a feature. The skill is
markdown. The storage is a directory.
