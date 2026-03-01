# Arizuka Spec

NanoClaw fork. Wire (routing) + tap (tool middleware) + bus (IPC/MCP).
3,916 LOC TypeScript. 20 source files. Compiles clean.

NanoClaw is the chosen base because it is the most flexible and useful
architecture. It provides real container isolation without senseless
overhead (no QEMU VMs, no 4 Zig daemons, no FUSE filesystem). Every
host file has zero SDK imports -- the container is a plugin slot that
any engine fills. The essential complexity is ~2,000 LOC; the rest of
the field wraps the same problem in 430,000+ lines of config and
accidental complexity.

## Why NanoClaw, Not Alternatives

**OpenClaw** has the routing brain (24+ channels, binding cascades, 6
queue strategies) but zero compute isolation. The gateway IS the agent
(`runEmbeddedPiAgent()` is a function call). 430,000+ lines, 53 config
files, 70+ dependencies. Multi-agent safety is prompt-only. The routing
logic is ~800 LOC worth extracting; the rest is accidental complexity.

**Muaddib** has the strongest isolation (QEMU micro-VMs via Gondolin)
but it's overbuilt for agent workloads. Four Zig daemons, FUSE
filesystem with 6-8 context switches per read, max 8 concurrent VMs.
The host-side tool executor pattern (~50 LOC/integration) and credential
isolation are worth extracting. The VM infrastructure is not.

**ElizaOS** has a plugin marketplace but no isolation at all. Agent runs
in-process with full permissions. PostgreSQL dependency. Horizontal
scaling is the one unique property; irrelevant at current scale.

**NanoClaw** is ~24 files, agent-agnostic (zero SDK imports on host),
has container isolation by default (not opt-in), transparent IPC proxy
bus, and the container contract is JSON stdin/stdout + MCP bus. Swapping
engines is a container image change; the host never changes.

## Architecture

```
Channel (WhatsApp / Telegram / Discord / Slack)
  |
  wire  -- message routing (4-tier binding cascade)
  |
  tap   -- tool middleware (credential inject, reject, audit)
  |
  box   -- container isolation (Docker per agent)
  |
  agent -- any engine: Claude SDK, Pi SDK, Codex, raw CLI
```

Three orthogonal components, separated not tangled:

- **wire** routes messages to agents. Does not execute agents.
- **tap** intercepts tool calls with middleware. Does not route messages.
- **box** isolates the agent. Does not intercept tools or route messages.

## Source Map

| File                   | LOC | Role                                                              |
| ---------------------- | --- | ----------------------------------------------------------------- |
| `index.ts`             | 479 | Main orchestrator. Polls SQLite, deduplicates, routes to queue    |
| `container-runner.ts`  | 447 | Spawns Docker containers. Builds mounts. Passes secrets via stdin |
| `mount-security.ts`    | 419 | Mount validation. 17-pattern blocklist. Path traversal prevention |
| `db.ts`                | 414 | SQLite: messages, groups, tasks, state                            |
| `group-queue.ts`       | 299 | Concurrency control (max N containers). Exponential backoff       |
| `channels/whatsapp.ts` | 298 | WhatsApp via baileys                                              |
| `ipc.ts`               | 270 | Filesystem IPC. 1s polling. Message/task routing                  |
| `task-scheduler.ts`    | 256 | Cron/interval/one-shot. 60s poll                                  |
| `config.ts`            | 199 | YAML config with ${ENV} interpolation. Singleton                  |
| `channels/telegram.ts` | 152 | Telegram bot API long-polling                                     |
| `types.ts`             | 151 | All interfaces                                                    |
| `tap.ts`               | 141 | Tool middleware pipeline. Glob matching. Credential injection     |
| `container-runtime.ts` | 87  | Docker abstraction. Health checks. Orphan cleanup                 |
| `wire.ts`              | 87  | 4-tier binding cascade: peer > account > channel > default        |
| `channels/index.ts`    | 49  | Channel registry and dispatch                                     |
| `group-folder.ts`      | 44  | Folder name validation and path resolution                        |
| `env.ts`               | 42  | Environment detection                                             |
| `router.ts`            | 38  | XML message formatting, injection escaping                        |
| `credentials.ts`       | 28  | Credential store. Env-sourced. Lookup by name                     |
| `logger.ts`            | 16  | Pino logger                                                       |

## Config Format

Single `config.yaml` with `${VAR}` env interpolation:

```yaml
assistant:
  name: 'Arizuka'
  trigger: '@Arizuka'
  defaultAgent: 'main'

container:
  image: 'arizuka-agent:latest'
  timeout: 1800000
  maxConcurrent: 5
  idleTimeout: 1800000

channels:
  whatsapp:
    enabled: false
  telegram:
    enabled: false
    token: '${TELEGRAM_BOT_TOKEN}'

autoRegister: true

agents:
  main:
    personality: 'You are a helpful assistant.'
    # network: true
    # image: "custom:latest"
    # timeout: 3600000
    # mounts:
    #   - hostPath: "~/datasets"
    #     containerPath: "datasets"
    #     readonly: true

bindings:
  - match: { channel: 'telegram', peer: 'tg:-1001234567' }
    agent: 'research'
  - match: { channel: 'telegram' }
    agent: 'main'

routes:
  - match: { tool: 'web_search' }
    middleware:
      - type: 'inject-header'
        header: 'Authorization'
        credential: 'serpapi-key'

credentials:
  serpapi-key:
    source: 'env'
    envVar: 'SERPAPI_API_KEY'
```

## Wire (Message Routing)

4-tier binding cascade, most specific wins:

1. **peer** -- exact chat/DM match
2. **account** -- specific bot account on that channel
3. **channel** -- channel-wide wildcard
4. **default** -- `assistant.defaultAgent`

Session key: `agent:{agentId}:{channel}:{peerId}`

Interface:

```typescript
interface RouteInput {
  channel: string;
  peerId: string;
  accountId?: string;
}

interface RouteResult {
  agentId: string;
  channel: string;
  peerId: string;
  sessionKey: string;
  matchedBy: 'peer' | 'account' | 'channel' | 'default';
}
```

Auto-registration: unknown Telegram/WhatsApp chats auto-register with
`defaultAgent` when `autoRegister: true`.

## Tap (Tool Middleware)

Pipeline intercepts tool calls before execution. Glob matching on tool
names. Per-agent scoping optional.

Middleware types:

- **inject-credential** -- adds credential as env var or HTTP header
- **inject-header** -- shorthand for header injection
- **reject** -- blocks the tool call with reason

Pipeline execution: all matching routes applied in order. Any middleware
returning null rejects the call. Otherwise the (potentially mutated)
tool call proceeds.

```typescript
type Middleware = (call: ToolCall, ctx: TapContext) => ToolCall | null;

interface TapContext {
  agentId: string;
  channel: string;
  peerId: string;
  groupFolder: string;
}
```

Glob patterns: `*` matches all, `prefix*` matches prefix, `*suffix`
matches suffix, exact string matches exact.

## Box (Container Isolation)

Docker container per agent invocation. Mounts explicitly controlled.

### Mount structure (main agent)

| Mount                                | Mode | Purpose                         |
| ------------------------------------ | ---- | ------------------------------- |
| Project root -> `/workspace/project` | ro   | Agent can't modify its own code |
| Group folder -> `/workspace/group`   | rw   | Agent's persistent workspace    |
| Session dir -> `/home/node/.claude`  | rw   | Claude session persistence      |
| IPC dir -> `/workspace/ipc`          | rw   | Agent <-> host communication    |

### Mount security

17-pattern blocklist: `.ssh`, `.gnupg`, `.gpg`, `.aws`, `.azure`,
`.gcloud`, `.kube`, `.docker`, `credentials`, `.env`, `.netrc`,
`.npmrc`, `.pypirc`, `id_rsa`, `id_ed25519`, `private_key`, `.secret`.

Allowlist at `~/.config/arizuka/mount-allowlist.json` -- outside project
root so containers can't tamper.

Folder name validation: `/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/` with path
traversal prevention.

### Container flags

Required: `--cap-drop ALL`, `--security-opt no-new-privileges`, `--memory 1g`, `--cpus 2`.

Network: on by default (agent needs Anthropic API). Per-agent toggle via
`agents.{id}.network: false`.

### Container contract

- **INPUT**: JSON on stdin `{prompt, history, config, tools[]}`
- **OUTPUT**: JSON on stdout `{result, session_id}`
- **BUS**: MCP tools at `/workspace/ipc/` (filesystem) or MCP stdio

Any engine fills the slot: Claude SDK, Pi SDK, Codex, raw CLI.

## IPC Bus

File-based IPC with 1s directory polling per group.

### Protocol

1. Agent calls MCP tool (thinks it's local) -> writes JSON to `/workspace/ipc/tasks/`
2. Host polls, reads JSON, checks authorization
3. Host injects credentials, routes to endpoint
4. Host writes result to `/workspace/ipc/x_results/{requestId}.json`
5. Agent reads result, returns to LLM

### Authorization

Main group can IPC to any group. Non-main groups restricted to own
`chatJid`. Sub-agent containers report to parent's channel only.

### Message types

`messages`, `tasks`, `register_group`, `refresh_groups`.

## Secret Handling

5-layer model from NanoClaw:

1. **Host-side isolation** -- `.env` read into memory, never `process.env`
2. **Stdin injection** -- secrets via container stdin pipe, not env/args
3. **Ephemeral disk** -- `/tmp/input.json` deleted immediately after read
4. **SDK-only access** -- secrets in `sdkEnv` object, not `process.env`
5. **Bash scrubbing** -- `unset ANTHROPIC_API_KEY` on every command

Secrets never in: Docker CLI args, environment, mounted files, image layers.

Residual risk: secret in agent-runner process memory. Prompt injection
can introspect SDK state. Acceptable tradeoff -- SDK must run inside to
keep all tools isolated.

## Concurrency Control

GroupQueue with FIFO ordering:

- Max N concurrent containers (configurable, default 5)
- Per-agent keying: one container per agent per group at a time
- Follow-up messages piped to active containers via IPC (no new container)
- Idle timeout: 30 minutes with activity detection
- Exponential backoff retry: 5s-80s, max 5 retries
- Drain order: (1) pending tasks for finished group, (2) pending messages, (3) next from FIFO

## Two-Cursor Dedup

```
lastTimestamp          -- "seen" cursor, advances for every polled message
lastAgentTimestamp[id] -- "processed" cursor, advances when agent finishes
```

Prevents duplicate dispatch during container runtime (30s container / 2s
poll = 15 potential duplicates without it). Crash recovery via
`recoverPendingMessages()`.

## Task Scheduler

Three modes: `cron`, `interval`, `once`. 60s poll cycle. Context modes:
`group` (shared context) or `isolated` (clean context per run).

## Channels

| Channel  | Status  | Implementation                      |
| -------- | ------- | ----------------------------------- |
| WhatsApp | Built   | baileys (`@whiskeysockets/baileys`) |
| Telegram | Built   | Bot API long-polling                |
| Discord  | Phase 2 | Channel interface ready             |
| Web      | Phase 2 | Channel interface ready             |

Channel interface:

```typescript
interface Channel {
  name: string;
  connect(): Promise<void>;
  sendMessage(jid: string, text: string): Promise<void>;
  isConnected(): boolean;
  ownsJid(jid: string): boolean;
  disconnect(): Promise<void>;
  setTyping?(jid: string, isTyping: boolean): Promise<void>;
}
```

New channels: implement interface, register in `channels/index.ts`.

## Per-Agent Configuration

Each agent inherits container defaults, can override:

```typescript
interface AgentConfig {
  personality?: string; // CLAUDE.md content
  image?: string; // container image override
  mounts?: AdditionalMount[]; // extra host dirs
  network?: boolean; // network access (default: true)
  timeout?: number; // per-agent timeout
  maxConcurrent?: number; // per-agent concurrency limit
}
```

## Host Architecture

Zero SDK imports on host side. Every file (`index.ts`,
`container-runner.ts`, `group-queue.ts`, `ipc.ts`, `mount-security.ts`,
`task-scheduler.ts`, `db.ts`) has zero `@anthropic-ai` imports.

SDK lives entirely in two container-side files:

- `container/agent-runner/src/index.ts` (agent entry point)
- `ipc-mcp-stdio.ts` (MCP tool proxy)

Host startup:

1. `loadConfig()` -> `initDatabase()` -> `connectChannels()` -> `startSchedulerLoop(60s)` -> `startIpcWatcher()` -> `startMessageLoop(2s)`
2. Message -> `resolveRoute()` -> `queue.enqueue()` -> `runContainerAgent()` -> `docker run -i --rm`
3. Container: compile TS, read stdin JSON, run agent, write stdout JSON

## What This Is Not

This is not a framework, not a platform, not a marketplace. It is:

- A host process that routes messages to containers
- A middleware pipeline that intercepts tool calls
- A container runner that isolates agents

~2,000 LOC of essential complexity. Everything else is channel adapters
and database glue.
