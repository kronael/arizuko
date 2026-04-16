---
status: draft
---

# Documentation Site Spec

Three-part site: Landing (one-pager), Docs (subsystem reference),
Cookbooks (walkthroughs with rationale). IA, outlines, style rules below.

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
- Audit trail: `messages.source` is the canonical adapter-of-record
  per inbound message (stamped by `api.handleMessage`); outbound rows
  carry `is_from_me=1 is_bot_message=1` and an empty source.
  `LatestSource(jid)` drives outbound adapter resolution.
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
