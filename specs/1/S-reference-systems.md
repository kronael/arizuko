---
status: reference
---

# Reference Systems Analysis

Concrete findings from studying brainpro, takopi, and eliza-atlas.
Source code read, not summaries. Informs kanipi v2 design.

Sources:

- `/home/onvos/app/refs/brainpro` — Rust CLI + gateway + agent daemon
- `/home/onvos/app/refs/takopi` — Python Telegram bridge
- `/home/onvos/app/eliza-atlas` + `/home/onvos/app/eliza-plugin-evangelist`
  — ElizaOS fork with YAML facts + research pipeline

---

## brainpro (openclaw + ironclaw + muaddib)

Two execution paths: direct CLI (`yo` → MrCode) and gateway
(client → WebSocket → Unix socket → MrBot agent daemon).

### Architecture

```
Client ──WebSocket──▶ brainpro-gateway
                              │
                         Unix socket
                              │
                       brainpro-agent ──▶ LLM API
```

Agent loop (`agent_impl.rs`): prompt → LLM → tool calls →
policy check (allow/ask/deny) → execute → repeat.
Max iterations: 12. Yield/resume for approval flows in
gateway mode. NDJSON events on unix socket: `Thinking`,
`ToolCall`, `ToolResult`, `Content`, `Done`, `Yield`, `Error`.

Session storage: `~/.brainpro/sessions/<uuid>.json` (JSONL
transcript of messages, tool calls, permission decisions).

### Routing / Groups

No group concept. `ChannelSessionMap` maps channel target
(chat/room id) → session UUID. One session per target.
No routing within a channel — one active agent per target.

### Tool Isolation

Subagents defined in `.brainpro/agents/<name>.toml`:

```toml
name = "scout"
allowed_tools = ["Read", "Grep", "Glob"]
permission_mode = "default"
max_turns = 8
system_prompt = "..."
```

Permission modes: `default` (reads ok, writes ask), `acceptEdits`
(mutations ok, Bash asks), `bypassPermissions` (all allowed).

Policy rules evaluated in order: `allow` → `ask` → `deny` →
mode default. Pattern syntax: `"Bash(git:*)"`, `"Write"`,
`"mcp.server.*"`. Built-in: `curl`/`wget` blocked.

External MCP servers via `config.toml`:

```toml
[mcp.servers.calc]
command = "/path/to/mcp-calc"
transport = "stdio"
```

### Memory

Workspace context loaded from `.brainpro/` each session:

| File                   | Loaded                 | Size limit |
| ---------------------- | ---------------------- | ---------- |
| `BOOTSTRAP.md`         | Always (main sessions) | 20k chars  |
| `MEMORY.md`            | Always (main sessions) | 20k chars  |
| `WORKING.md`           | Always (main sessions) | 20k chars  |
| `memory/YYYY-MM-DD.md` | Today + yesterday only | 20k chars  |

Subagents receive no workspace context (clean slate).
Truncation: 70% head, 20% tail.

### Resilience

**Circuit breaker** (`src/circuit_breaker.rs`):

```
Closed → Open → HalfOpen → Closed
```

Defaults: 5 consecutive failures → Open; 30s recovery →
HalfOpen; 3 successful probes → Closed. Per-backend registry
so one unhealthy backend doesn't affect others.

**Doom loop detection** (`src/agent_impl.rs`):

```rust
const DOOM_LOOP_THRESHOLD: usize = 3;

// Hash name + args, ring buffer of last N hashes
// If last N are identical → abort turn
fn record(&mut self, hash: u64) -> bool {
    self.recent_calls.push(hash);
    let start = self.recent_calls.len() - DOOM_LOOP_THRESHOLD;
    let recent = &self.recent_calls[start..];
    recent.iter().all(|h| *h == hash)
}
```

**Retry with jitter**: exponential backoff, 1s initial, 60s max,
±30% jitter. Respects `Retry-After` (429).

**Provider health**: Healthy/Degraded/Unhealthy states with
fallback chains per task category.

### Modular persona assembly

Persona defined in `config/persona/<name>/`:

```
manifest.md    # tool list, assembly order
identity.md    # who the agent is
soul.md        # personality (MrBot only)
agents.md      # operating instructions
tooling.md     # tool usage
plan-mode.md   # conditional: planning mode active
optimize.md    # conditional: optimize mode active
```

Assembly order from manifest frontmatter. Conditional sections
only injected when that mode is active. Allows specialization
without forking the full prompt.

### What kanipi should adopt

- Doom loop detection: container runner or agent-runner
- Circuit breaker: around `docker run` / LLM calls
- Subagent toml → worker groups with tool allow-lists
- Modular persona assembly for per-group identity files

---

## takopi

Python Telegram bridge. Wraps any agent CLI (claude, codex,
opencode, pi) as a subprocess runner. No gateway daemon —
each message spawns a CLI process.

### Architecture

```
Telegram poll
  → ThreadScheduler (per-thread FIFO queue)
  → runner_bridge.handle_message()
  → Runner.run(prompt, resume_token)
  → spawn subprocess, stream JSONL
  → TakopiEvent stream → Telegram render
```

Plugin architecture: Python entrypoints for engine/transport/
command backends. Lazy-loaded on first use.

### Routing / Groups

Per-thread FIFO queues (ThreadScheduler). Same thread
serialized; different threads run in parallel. Session lock
prevents concurrent resume of the same session.

Engine selection:

1. Parse directive prefix (`/<engine-id>`, `/<project-alias>`,
   `@branch`) from first non-empty message line
2. Try to extract resume token from message or reply text
3. If resume token found → route to matching engine
4. Otherwise → default engine

Multi-project: `[projects.alias]` in config maps to a
worktree path + default engine. `@branch` directive checkouts
a specific worktree.

### Resume tokens

First-class objects: `ResumeToken(engine, value)`.

```python
@dataclass(frozen=True, slots=True)
class ResumeToken:
    engine: EngineId
    value: str
```

CLI embeds resume token in reply footer:
`` `claude resume abc123` ``. On next message, bridge
calls `extract_resume(reply_text)` across all runners.
Matched token locks the session and passes `--resume abc123`
to the CLI. Session lock (`anyio.Semaphore`) prevents
concurrent resumes of the same value.

### JSONL event protocol

Subprocess emits JSONL; runner translates to `TakopiEvent`:

```python
type TakopiEvent = StartedEvent | ActionEvent | CompletedEvent
type ActionKind = Literal[
    "command", "tool", "file_change", "web_search",
    "subagent", "note", "turn", "warning", "telemetry"
]
```

`StartedEvent` carries the resume token. `CompletedEvent`
carries the final answer + resume token for threading.
Bridge renders progress to Telegram, edits in place.

### Tool Isolation

None — tool restriction is delegated to the CLI (`claude
--disallowedTools`, brainpro permission mode, etc).
Takopi treats each engine as a black box.

### Memory

N/A — stateless bridge. Memory lives inside the agent CLI
(session files, CLAUDE.md, etc). Takopi only passes
`--resume` to reattach.

### What kanipi should adopt

- Resume token pattern: embed token in reply, extract on
  next message, `--resume` to agent CLI
- Per-thread FIFO with session lock (kanipi already has
  GroupQueue; resume locking is missing)
- ActionKind taxonomy for richer streaming events

---

## eliza-atlas + eliza-plugin-evangelist

ElizaOS fork with YAML facts repository and two-phase
research pipeline. Components: Services (state) → Actions
(user commands) → Providers (read-only context injection).

### Architecture

```
Inbound message
  → Providers inject context (XML bundles)
  → LLM generates response
  → Actions evaluate + execute (researchNeeded, helpSession)
  → Services handle background work (research queue, facts)
```

Plugin lifecycle: topological dependency resolution so
services start in dependency order.

### Routing / Groups

Room-per-workspace. `HelpSessionManager` scopes sessions
to `channelId:userId` with 1h TTL. Session guard provider
blocks non-session messages in help mode. Research requests
carry `ResearchContext` with `roomId`, `entityId`, `threadId`
for reply routing back to origin thread.

### Tool Isolation

Providers are read-only by design. Actions execute with
restricted tool lists. Research subprocess uses:

```typescript
const ALLOWED_TOOLS = [
  'Read', 'Glob', 'Grep', 'WebSearch', 'WebFetch',
  'Bash(ls:*)', 'Bash(find:*)', 'Bash(git:*)',
  'Bash(curl:*)', 'Bash(wget:*)', ...
];
const DISALLOWED_TOOLS = ['Edit', 'NotebookEdit'];
```

No container isolation — restriction is CLI flag only.

### Memory

**YAML facts repo** (`facts/` directory of `.md` files):

```yaml
---
path: topic-slug
category: architecture
verified_at: 2025-01-01
confidence: high
---
One verifiable finding per paragraph...
```

`FactsService` loads all `.md` files, generates embeddings,
exposes `search(query, k)` via cosine similarity + keyword
fallback. `knowledgeContextProvider` formats results as XML:

```xml
<fact path="topic-slug" confidence="85%">
  header: ...
  verification: verified (high)
  summary: ...
  read_full: facts/topic-slug.md
</fact>
```

### Two-phase fact verification

**Phase 1** (Opus): research prompt → XML `<research>` output
with `<factset>` / `<fact source="file:line">` / `<summary>`.

**Phase 2** (Sonnet): for each factset, verification prompt:

> "For each finding, try to REFUTE it using the codebase.
> If you cannot find evidence to disprove it, accept it."

Rejected findings are dropped. Verified facts written to
`facts/<slug>.md`. This asymmetric model (Opus explores,
Sonnet challenges) catches hallucinated findings before
they persist.

### Session management

`HelpSessionManager`: 1h TTL, per-room scoping, persisted to
runtime cache. Session guards block unrelated messages.
`originalMessageId` → `platformMessageId` lookup enables
threaded research delivery back to origin message.

### What kanipi should adopt

- XML context bundles for facts/knowledge injection
  (matches Anthropic's recommendation; see `xml-vs-json-llm.md`)
- Two-phase verification: Opus researches, Sonnet refutes
- YAML facts repo pattern for kanipi's long-term memory
  (see `specs/2/3-memory-facts.md`)
- HelpSessionManager TTL pattern for scoped agent sessions

---

## Cross-cutting comparison

| Pattern        | brainpro                            | takopi                        | eliza-atlas           | kanipi                    |
| -------------- | ----------------------------------- | ----------------------------- | --------------------- | ------------------------- |
| Routing        | Session-per-channel-target          | Thread-per-message            | Room-per-workspace    | Group-per-JID             |
| Resume         | Session UUID (uuid.json)            | Resume token (`engine:value`) | Session+message ID    | Session folder            |
| Memory         | BOOTSTRAP+MEMORY+WORKING+daily      | None (stateless bridge)       | YAML facts+embeddings | Skills+CLAUDE.md          |
| Tool isolation | Subagent toml+permission rules      | CLI flags (delegated)         | Restricted tool list  | Skills (CLAUDE.md rules)  |
| MCP            | config.toml per-server, tool filter | N/A (CLI delegates)           | N/A (native tools)    | nanoclaw+agent-registered |
| Streaming      | NDJSON unix socket                  | JSONL subprocess→TakopiEvent  | Progress events       | IPC messages              |
| Resilience     | Circuit breaker+doom loop+fallback  | Thread-safe queuing           | Session lifecycle     | Not yet                   |

---

## Concrete adoptions for kanipi

### P0 — this sprint

**Hierarchical group routing** (see `specs/1/F-group-routing.md`)
Builds on: takopi ThreadScheduler (FIFO per thread), brainpro
ChannelSessionMap (session per target). Groups form a tree;
parent routes to children by command/pattern/delegation.

**MCP server isolation** (see `specs/1/isolation.md`)
Builds on: brainpro MCP config.toml, eliza-atlas service model.
Each MCP server in its own container with unix socket transport.
Gateway manages lifecycle.

### P1

**Doom loop detection** — from brainpro `agent_impl.rs`.
Ring buffer of last N tool call hashes. If last 3 identical →
abort turn, emit error IPC message. Implement in
`container/agent-runner/` or gateway's container monitor.

```typescript
// gateway: detect stuck container, kill + report
const DOOM_THRESHOLD = 3;
// agent-runner: ring buffer of (tool_name + args hash)
// if ring buffer saturated with identical hash → exit 1
```

**Resume token pattern** — from takopi.
Embed session ID in agent reply footer. On next message,
extract token, pass `--resume <id>` to agent CLI. Enables
thread-continuation without losing context.

```typescript
// Footer format (agent appends):
// `session:abc123`
// Gateway extracts → ContainerInput.resumeSession
```

### P2

**Circuit breaker for container spawns** — from brainpro.
`docker run` failures (image not found, OOM, timeout) open
the circuit per group. Prevents cascading spawns on broken
container images.

```typescript
// src/container-runtime.ts
class SpawnCircuitBreaker {
  // Closed → Open (5 failures) → HalfOpen (30s) → Closed
  check(group: string): 'allow' | 'reject' | 'probe';
  recordSuccess(group: string): void;
  recordFailure(group: string): void;
}
```

**XML context bundles** — from eliza-atlas.
Wrap memory/facts injection in XML tags for system prompt.
`<context_bundle>` with `<facts>`, `<session>`, `<history>`.
Research shows Claude parses XML structure better than plain
text for prompt sections (see `specs/res/xml-vs-json-llm.md`).

**Two-phase fact verification** — from eliza-atlas.
When agent writes to `facts/`, gateway or a verifier runs
Sonnet to attempt refutation. Rejects findings without
source evidence. Prevents fact file pollution.

### P3

**Modular persona assembly** — from brainpro.
Per-group `persona/` directory: `identity.md`, `soul.md`,
`tooling.md`, `plan-mode.md`. Gateway assembles system
prompt from parts based on mode flags. Enables per-group
identity without duplicating entire CLAUDE.md.

```typescript
// container-runner.ts
function buildSystemPrompt(group: RegisteredGroup): string {
  const parts = [identity, tooling];
  if (group.persona?.soul) parts.splice(1, 0, soul);
  if (flags.planMode) parts.push(planMode);
  return parts.join('\n\n---\n\n');
}
```

---

## Open

- Doom loop threshold: 3 identical calls (brainpro default).
  Adjust for agents that legitimately repeat reads.
- Resume token in reply footer vs. IPC: footer is visible to
  user; IPC reply would be cleaner but requires agent support.
- Circuit breaker scope: per-group or per-image? Per-group
  is safer (isolates one bad group from others).
- YAML facts repo: flat directory or slugged hierarchy?
  Evangelist uses flat (one file per slug). Sufficient for v1.
- Two-phase verification: requires second Claude invocation
  per factset. Cost model needs evaluation before enabling
  by default.
- Modular persona: conflicts with current CLAUDE.md approach
  where agent self-edits its own instructions. Define
  boundary: gateway-managed persona vs. agent-managed rules.
