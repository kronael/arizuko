# Systems

Kanipi decomposes into orthogonal systems.

## Infrastructure

```
channels --> ROUTER --> spawner --> container
             |    ^                   |
             |    +-- actions <-------+ (IPC / MCP)
             |
             +-- mime (inbound enrichment)
             +-- web (separate process)
```

### Router

Core. Inbound pipeline, action dispatch, message formatting,
channel management.
Code: `index.ts`, `router.ts`, `ipc.ts`, `commands/`

### Spawner

Container lifecycle. Create, mount, run, cleanup.
Code: `container-runner.ts`, `container-runtime.ts`

### Web

Vite dev server (bash entrypoint). Gateway proxies via
`web-proxy.ts`. Already separate process.

### MCP Proxy

Replaces file IPC with MCP over unix socket.
See `ipc-mcp-proxy.md`.

### MIME

Media enrichment in inbound pipeline. Could move
agent-side as MCP servers (`workflows.md`).
Code: `mime.ts`, `mime-handlers/`

## Application systems (v2 -- open)

```
#topic -> @agent -> workflow (enrich -> prompt -> call)
```

### Topics (#topics)

Route by topic/thread to specific agents. Topic ID as
JID segment (builds on worlds router). Glob patterns.

Session threading lives here: resume tokens (takopi pattern)
embed session ID in reply footer, next message extracts and
passes `--resume <id>` for explicit continuation. Thread-level
granularity within groups (same group, different conversations).

### Agents (@agents)

Different personas/configs. Currently group = agent (1:1).
Patterns: topic-based, skill-based, model-based.

### Workflows

Multi-step pipelines: enrich, prompt, LLM call, transform,
route. Agent orchestrates or gateway pre-defines.
See `workflows.md`.

## Interfaces

| From     | To      | Interface                         |
| -------- | ------- | --------------------------------- |
| Channels | Router  | `NewMessage` events               |
| Router   | Spawner | `runContainerAgent(group, input)` |
| Spawner  | Router  | `ContainerOutput` (stdout JSON)   |
| Agent    | Router  | IPC files (v1) / MCP (v2)         |
| Router   | Channel | `sendMessage()`, `sendDocument()` |
| Router   | MIME    | `enrichMessage(msg)`              |
| Router   | Topics  | JID pattern -> agent selection    |
