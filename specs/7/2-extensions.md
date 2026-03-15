# Extensions System

**Status**: planning (updated 2026-03-10)

Extension points and plugin architecture for arizuko.
Goal: make the system extensible without modifying core code.

## Extension Points Summary

| Point         | Location            | Extensible By | Mechanism        |
| ------------- | ------------------- | ------------- | ---------------- |
| Channels      | external containers | Developer     | HTTP protocol    |
| Actions       | MCP tools           | Agent/Plugin  | Registry + MCP   |
| Routing Rules | router/             | Agent         | MCP tools        |
| Sidecars      | container/          | Agent         | Container config |
| Mounts        | container/          | Agent         | Container config |
| Skills        | container/skills/   | Agent         | File-based       |
| Tasks         | services/timed/     | Agent         | IPC actions      |
| Diary         | diary/              | Agent         | File-based       |

## 1. Action Registry

Shipped as `icmcd/` package. All 16 MCP tools registered in
a single handler with gateway callbacks injected at creation
time. Agent discovers tools via MCP `tools/list`. Authorization
via `authd.Authorize` at runtime.

See `specs/7/10-icmcd.md` for tool list and architecture,
`specs/7/11-authd.md` for tier assignments.

## 2. Channel Interface

Channels are now external HTTP processes, not Go interfaces.
See `specs/7/1-channel-protocol.md` for the full protocol
spec including registration, capabilities, auth, and
transport options.

## 3. Sidecar System

**Current**: Per-group sidecar config in `GroupConfig.Sidecars`.
Launched as separate containers with Unix socket IPC.

### Open Questions

1. **Sidecar protocol**: Currently assumes MCP over Unix socket.
   - Is MCP required?
   - Can sidecars use other protocols (HTTP, gRPC)?
   - How to specify protocol?

2. **Sidecar lifecycle**: Currently started/stopped with agent container.
   - Should sidecars persist across agent runs?
   - Shared sidecars between groups?
   - Health checks?

3. **Sidecar discovery**: How do agents know what sidecars are available?
   - Written to manifest at spawn?
   - Query gateway via IPC?

4. **Sidecar auth**: Can sidecars access agent credentials?
   - Env var passthrough (current)
   - Scoped tokens?
   - No auth?

### Proposed Design

```yaml
# groups/<folder>/container-config.yaml
sidecars:
  whisper:
    image: arizuko-whisper:latest
    protocol: http # NEW: http|mcp|grpc
    port: 8080 # NEW: if http/grpc
    persist: false # NEW: keep running?
    env:
      WHISPER_MODEL: large-v3
    resources:
      memory: 4G
      cpus: 2.0
    tools: ['*'] # MCP tool filter
```

## 4. Skills System

**Current**: `container/skills/` seeded into agent session.
Each skill has `SKILL.md` with prompt injection.

### Open Questions

1. **Skill loading**: When are skills loaded?
   - At container spawn (current)
   - On demand?
   - Cached in session?

2. **Skill dependencies**: Can skills depend on other skills?
   - Dependency resolution?
   - Version constraints?

3. **Skill scope**: Are skills global or per-group?
   - Gateway skills (container/skills/) are global
   - Agent skills (.claude/skills/) are per-session
   - Group skills (groups/<folder>/.claude/skills/) are per-group

4. **Skill updates**: How do skills get updated?
   - Migration system (current: `MIGRATION_VERSION`)
   - Hot reload?
   - Agent self-update?

5. **Skill marketplace**: Can agents install skills from external sources?
   - Security implications?
   - Verification/signing?

### Proposed Design

```
container/skills/         # Gateway-provided, read-only
groups/<folder>/skills/   # Group-specific, persistent
.claude/skills/           # Session-specific, ephemeral

Skill Format:
  <name>/
    SKILL.md              # Required: prompt injection
    CLAUDE.md             # Optional: additional context
    schema.json           # Optional: tool definitions
    migrations/           # Optional: upgrade scripts
```

## 5. Routing Rules

Flat routes table (shipped). Keyed by `jid` + `seq`.
7 rule types: command, verb, pattern, keyword, sender,
trigger, default. First match wins.

Agents modify routing via MCP `set_routing_rules` tool
(tier 0-2). Dynamic — no restart needed.

## 6. Permission Tiers

**Current**: 4 tiers based on folder depth (0=root, 1=world,
2=agent, 3=worker). See `specs/7/11-authd.md` for tier
definitions and MCP tool tier assignments.

### Open Questions

1. **Tier semantics**: Are tiers the right model?
   - Alternative: explicit capability grants
   - Alternative: ACLs per action

2. **Tier inheritance**: Do children inherit parent permissions?
   - Current: no, tier computed from depth
   - Proposal: explicit inheritance?

3. **Tier escalation**: Can an agent temporarily escalate?
   - Current: no
   - Proposal: escalate_group action for delegation

4. **Custom tiers**: Can users define new permission levels?
   - Named roles instead of numbers?

### Proposed Design

```go
// Option A: Keep tier system, add explicit caps
type ActionContext struct {
    SourceGroup string
    Tier        int
    Caps        []string // ["inject_message", "register_group", ...]
}

// Option B: Replace tiers with capabilities
type ActionContext struct {
    SourceGroup string
    CanInject   bool
    CanRegister bool
    CanDelegate bool
    CanSchedule bool
}
```

## 7. Plugin Architecture

Plugins are docker containers with `.toml` configs in the
services/ directory. See `specs/7/0-architecture.md` for
the services directory format.

### Open Questions

1. **Discovery**: how to find third-party extensions
2. **Install**: `arizuko install <repo>` workflow
3. **Versioning**: how to pin and upgrade versions
4. **Security**: network access to router
5. **Dependencies**: extension A needs extension B

Not designed yet. Built-in model works. Extensions deferred
until built-in channels are validated.

## Non-goals (for now)

- External plugin loading (deferred)
- Plugin marketplace (deferred indefinitely)
- Hot reload (deferred)
- WebAssembly support (deferred)
