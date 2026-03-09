# SPEC: Three New Specs for arizuko

Write three new spec files under `specs/1/`. Each spec should follow
the existing style: markdown, concise, concrete, no fluff. Reference
existing specs and code where relevant. Use XML tags sparingly for
structured examples.

## Deliverable 1: `specs/1/F-group-routing.md`

**Title**: Hierarchical Group Routing

The main group today is special (isRoot). Extend this so ANY group can
set up a router and routing rules for groups beneath it. A group that
owns sub-groups becomes a "parent" — it defines how messages flow to
its children.

Key design points:

- A parent group defines routing rules for its children (pattern match,
  command prefix, keyword, or delegation via IPC)
- Children inherit the parent's channel bindings but can override
- The main (root) group is just the top of the tree — not special-cased
- JID hierarchy from `specs/1/e-worlds.md` (Phase 1 shipped: `/` separator,
  glob routing with minimatch) is the foundation
- Parent group's agent can delegate to child groups via IPC
  (`type:'delegate'` with `{group, prompt}`)
- Child groups have their own session, CLAUDE.md, skills, memory
- Router config stored in `registered_groups` table (new columns or
  JSON config column)
- Routing evaluation order: exact command → pattern match → parent default

Reference patterns:

- **takopi**: ThreadScheduler with per-thread FIFO queues, thread-aware
  routing. Projects + threads model.
- **brainpro/muaddib**: Session-per-conversation. Subagents with restricted
  tool sets defined in `.brainpro/agents/<name>.toml`. Permission modes
  (Default/AcceptEdits/BypassPermissions). One session = one agent.
- **eliza-atlas**: Room-per-workspace. Plugin lifecycle with topological
  dependency resolution. RoomType.THREAD for thread isolation.

Existing arizuko specs to build on:

- `specs/1/e-worlds.md` — JID hierarchy, glob routing
- `specs/1/1-agent-routing.md` — worker agents per group (command/keyword/delegate)
- `specs/1/Z-systems.md` — topics → agents decomposition
- `specs/1/0-actions.md` — action registry for IPC delegation

Write concrete examples showing:

1. Root group routes `/code` commands to a `code` child group
2. A "team" parent group routes by sender pattern to per-person child groups
3. A parent group's agent delegates dynamically via IPC

Include a section on what changes in gateway code (router.ts, group-queue.ts,
registered_groups schema).

## Deliverable 2: `specs/1/h-isolation.md`

**Title**: MCP Tool Isolation

How to isolate MCP tool servers in their own containers (docker now,
sandboxes later). Today MCP servers run inside the agent container
(nanoclaw, agent-registered servers). This spec describes running them
in separate containers with controlled communication.

Key design points:

- Each MCP server runs in its own docker container (sidecar pattern)
- Communication: unix socket mounted into both agent and sidecar containers
- Gateway manages sidecar lifecycle (start before agent, stop after)
- Sidecar images configurable per group (or globally)
- Agent sees MCP servers via socket paths in settings.json
- Permission model: which tools each sidecar exposes, which groups can use them
- Resource limits per sidecar (memory, CPU, network access)
- Later: Firecracker/gVisor for stronger isolation (virtio-vsock transport)

Reference patterns:

- **brainpro**: MCP servers configured in `.brainpro/config.toml` with
  tool filtering per server. Permission system: rules evaluated in order
  (allow → ask → deny → mode default). Custom slash commands.
- **eliza-atlas**: Services (state management) → Actions (user commands) →
  Providers (read-only context). Plugin lifecycle with topological
  dependency resolution.
- **arizuko sidecar/whisper**: Existing sidecar pattern — separate docker
  image, mounted socket, gateway manages lifecycle.

Existing arizuko specs to build on:

- `specs/3/A-ipc-mcp-proxy.md` — MCP over socket, three MCP layers
- `specs/1/9-extend-agent.md` — agent-registered MCP servers
- `specs/1/A-extend-gateway.md` — gateway registries
- `sidecar/whisper/` — existing sidecar implementation

Write concrete examples showing:

1. A web-search MCP sidecar with network access but no filesystem
2. A code-execution MCP sidecar with filesystem but no network
3. Per-group sidecar configuration in `.env` or registered_groups

Include a section on the sidecar lifecycle (start, health check, stop)
and how this relates to the existing whisper sidecar.

## Deliverable 3: `specs/1/S-reference-systems.md`

**Title**: Reference Systems Analysis

Document what we've learned from studying openclaw (brainpro CLI),
ironclaw (brainpro gateway), muaddib (brainpro agent daemon), takopi
(telegram bridge), and eliza-atlas (ElizaOS fork). This is a concrete
analysis of their architectures and what arizuko should adopt.

For each system, document:

1. **Architecture**: How it's structured, key components
2. **Routing/Groups**: How messages flow, how groups/rooms/sessions work
3. **Tool Isolation**: How tools/MCP servers are sandboxed or controlled
4. **Memory**: How state persists across sessions
5. **What arizuko should adopt**: Concrete patterns to port

### brainpro (openclaw + ironclaw + muaddib)

Source: `/home/onvos/app/refs/brainpro`

Key findings:

- **Persona system**: Modular prompts — identity.md + soul.md + tooling.md
  - conditional sections (plan-mode.md, optimize.md). Assembly order matters.
- **Subagents**: Restricted tool sets in `.brainpro/agents/<name>.toml`.
  Permission modes: Default, AcceptEdits, BypassPermissions.
- **Resilience**: Circuit breaker (Closed → Open → HalfOpen), provider
  health tracking (Healthy/Degraded/Unhealthy), fallback chains with
  jittered backoff retries.
- **Doom loop detection**: Ring buffer of recent tool calls with hash-based
  identity checking (DOOM_LOOP_THRESHOLD = 3).
- **Memory**: BOOTSTRAP.md + MEMORY.md + WORKING.md + memory/YYYY-MM-DD.md.
  Only today + yesterday loaded. Truncated at 20k chars.
- **Protocol**: Gateway ↔ Agent via Unix socket with NDJSON streaming.
  Client ↔ Gateway via WebSocket (port 18789).
- **MCP**: External MCP servers in config.toml with per-server tool filtering.

### takopi

Source: `/home/onvos/app/refs/takopi`

Key findings:

- **Plugin architecture**: Entrypoint discovery for engine/transport/command
  backends. Lazy loading.
- **Threading model**: Per-thread FIFO queues (ThreadScheduler). Same thread
  serialized, different threads parallel. Session locks prevent concurrent
  execution.
- **Resume tokens**: `{engine, value}` as first-class objects. Extracted from
  reply text, passed to agent CLI for session continuation.
- **Progress streaming**: JSONL events from subprocess → TakopiEvent →
  Telegram rendering. Action types: command, tool, file_change, web_search,
  subagent, note, turn, warning, telemetry.
- **Multi-project**: Worktrees, multiple repos, parallel runs per thread.

### eliza-atlas

Source: `/home/onvos/app/eliza-atlas` + `/home/onvos/app/eliza-plugin-evangelist`

Key findings:

- **Component model**: Services → Actions → Providers → Database → LLM.
  Topological dependency resolution for plugins.
- **XML context bundles**: `<context_bundle>` with orientation, current_message,
  historical_messages, world_activity, similar_context, search_context.
  Key attributes: `ago` (relative time), `stale="true"`, `<thought>`,
  `<actions>`.
- **Research pipeline**: Two-phase fact verification (Opus researches,
  Sonnet cross-checks with disproval evidence). Rejected findings dropped.
- **Facts system**: YAML files with topics, verification metadata,
  three-tier confidence (primary >=90%, context 70-90%, weak 40-70%).
- **Session management**: HelpSessionManager with 1h TTL, session guards,
  per-room scoping.
- **Reply threading**: originalMessageId → platformMessageId lookup for
  threaded research delivery.

### Cross-cutting analysis

Include a comparison table:

| Pattern        | brainpro                       | takopi              | eliza-atlas           | arizuko status            |
| -------------- | ------------------------------ | ------------------- | --------------------- | ------------------------- |
| Routing        | Session-per-CLI                | Thread-per-message  | Room-per-workspace    | Group-per-JID             |
| Resume         | Session UUID                   | Resume tokens       | Session+message ID    | Session folder            |
| Memory         | BOOTSTRAP+MEMORY+WORKING+daily | N/A                 | YAML facts+embeddings | Skills+CLAUDE.md          |
| Tool isolation | Subagent toml+permissions      | Per-runner CLI      | Service+action model  | Skills (CLAUDE.md rules)  |
| MCP            | config.toml per-server         | N/A (CLI delegates) | N/A (native tools)    | nanoclaw+agent-registered |
| Streaming      | NDJSON unix socket             | JSONL subprocess    | Progress events       | IPC messages              |
| Resilience     | Circuit breaker+fallback       | Thread-safe queuing | Session lifecycle     | Not yet                   |

### Concrete adoptions for arizuko

List specific patterns arizuko should adopt, with priority:

1. **P0**: Hierarchical group routing (this sprint)
2. **P0**: MCP sidecar isolation (this sprint)
3. **P1**: Doom loop detection from brainpro
4. **P1**: Resume token pattern from takopi
5. **P2**: Circuit breaker for container spawns
6. **P2**: XML context bundles from eliza-atlas
7. **P2**: Two-phase fact verification from eliza-atlas
8. **P3**: Modular persona assembly from brainpro

## Style Guidelines

- Match existing spec style in `specs/1/`
- Concise, no marketing language
- Code examples in TypeScript (gateway is TS)
- Reference existing code paths where relevant
- Mark open questions explicitly
- 80 char line width preferred, 120 max
