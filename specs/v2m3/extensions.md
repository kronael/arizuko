# Extensions System

**Status**: planning

v2m3 defines extension points and plugin architecture for arizuko.
Goal: make the system extensible without modifying core code.

## Extension Points Summary

| Point         | Location            | Extensible By | Mechanism        |
| ------------- | ------------------- | ------------- | ---------------- |
| Channels      | core/types.go:76    | Developer     | Interface impl   |
| Actions       | actions/registry.go | Agent/Plugin  | Registry pattern |
| Routing Rules | core/types.go:56    | Agent         | IPC config       |
| Sidecars      | core/types.go:47    | Agent         | Container config |
| Mounts        | core/types.go:41    | Agent         | Container config |
| Skills        | container/skills/   | Agent         | File-based       |
| Tasks         | scheduler/          | Agent         | IPC actions      |
| Diary         | diary/              | Agent         | File-based       |

## 1. Action Registry

**Current**: `actions/` package with `Register()`, `Get()`, `All()`.

**Problem**: Actions in `actions/` are NOT wired to IPC. The `ipc/watcher.go`
has a parallel `handleAction()` switch that duplicates the logic.

### Open Questions

1. **Single source of truth**: Should `handleAction()` dispatch through
   `actions.Get(type)` or keep the switch statement?

2. **Schema validation**: Actions package has JSON schema definitions.
   Should IPC validate input against schema before dispatch?

3. **Action discovery**: How do agents discover available actions?
   - Write manifest file at container spawn?
   - MCP tool registration?
   - Both?

4. **Custom actions**: Can agents register new actions at runtime?
   - If yes: how does gateway learn about them?
   - If no: all actions must be compiled into gateway

5. **Action versioning**: What happens when action schema changes?
   - Backwards compatibility requirements?
   - Version field in request?

### Proposed Design

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│ IPC Request │────▶│ Action       │────▶│ Handler     │
│ {type, ...} │     │ Registry     │     │ Function    │
└─────────────┘     └──────────────┘     └─────────────┘
                           │
                    ┌──────┴──────┐
                    │ Schema      │
                    │ Validation  │
                    └─────────────┘
```

**Option A**: Registry dispatches all actions

- `handleAction()` becomes: `return actions.Dispatch(typ, data, ctx)`
- Single code path, DRY
- All actions must be registered

**Option B**: Keep switch, registry for metadata only

- Switch handles core actions
- Registry provides schema/docs for agent discovery
- Hybrid approach

## 2. Channel Interface

**Current**: 5 implementations (telegram, discord, email, whatsapp, web).
Instantiated in `cmd/arizuko/main.go` based on env vars.

### Open Questions

1. **Plugin channels**: Can channels be loaded from external binaries?
   - Go plugin system (`.so` files)?
   - Subprocess with stdio protocol?
   - Neither (compile-time only)?

2. **Channel discovery**: How does gateway know which channels exist?
   - Hardcoded in main.go (current)
   - Scan plugin directory
   - Configuration file

3. **Channel capabilities**: Different channels have different features.
   - Reactions, threads, file types, formatting
   - How to express capabilities?
   - Should actions check channel capabilities?

4. **Channel middleware**: Can channels have pre/post processing?
   - Rate limiting, logging, transformation
   - Decorator pattern?
   - Hooks?

### Proposed Design

```go
// Option A: Keep simple interface, add capabilities
type Channel interface {
    Name() string
    Capabilities() ChannelCaps  // NEW
    Connect(ctx context.Context) error
    Send(jid, text string) error
    SendFile(jid, path, name string) error
    Owns(jid string) bool
    Typing(jid string, on bool) error
    Disconnect() error
}

type ChannelCaps struct {
    Reactions  bool
    Threads    bool
    FileTypes  []string
    MaxMsgLen  int
    Formatting string // "markdown"|"html"|"plain"
}
```

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

**Current**: 5 rule types (command, pattern, keyword, sender, default).
Stored in `registered_groups.routing_rules` JSON.

### Open Questions

1. **Rule composition**: Can rules be combined (AND/OR)?
   - Current: first match wins
   - Proposal: rule chains?

2. **Rule priority**: How to order rules?
   - Current: array order
   - Explicit priority field?

3. **Dynamic routing**: Can routing change based on context?
   - Time-based rules?
   - Load-based rules?
   - State-based rules?

4. **Routing actions**: What can a rule do besides delegate?
   - Transform message?
   - Enrich with context?
   - Split to multiple targets?

### Proposed Design

```go
type RoutingRule struct {
    Kind     string   // command|pattern|keyword|sender|default
    Match    string   // pattern/keyword/etc
    Target   string   // group folder
    Priority int      // NEW: explicit ordering
    Enabled  bool     // NEW: toggle without delete
    Transform string  // NEW: optional message transform
}
```

## 6. Permission Tiers

**Current**: 4 tiers based on folder depth (0=root, 1=world, 2=agent, 3=worker).

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

## 7. Plugin Architecture (Future)

### Open Questions

1. **Plugin format**: How are plugins packaged?
   - Go plugins (`.so` files, Linux only)
   - Subprocesses with stdio/socket protocol
   - WebAssembly modules
   - Container images

2. **Plugin discovery**: How does gateway find plugins?
   - `plugins/` directory
   - Configuration file
   - Registry service

3. **Plugin lifecycle**: How are plugins managed?
   - Start/stop with gateway
   - On-demand loading
   - Hot reload

4. **Plugin isolation**: How are plugins sandboxed?
   - Same process (Go plugins) - no isolation
   - Subprocess - OS isolation
   - Container - full isolation
   - WASM - language-level isolation

5. **Plugin API**: What can plugins access?
   - Full gateway state?
   - Scoped interface?
   - Event streams only?

### Proposed Design

```
Phase 1 (v2m3): Document extension points, clean interfaces
Phase 2 (v3m1): Subprocess plugins with stdio protocol
Phase 3 (v3m2): Container-based plugins with IPC

Plugin Protocol (stdio):
  Gateway -> Plugin: JSON-RPC requests
  Plugin -> Gateway: JSON-RPC responses + events

Plugin Manifest:
  name: my-plugin
  version: 1.0.0
  capabilities:
    - channel    # implements Channel interface
    - action     # registers actions
    - middleware # pre/post processing
```

## Immediate Actions (v2m3)

1. **Wire actions/registry to IPC**
   - Replace `handleAction()` switch with registry dispatch
   - Add schema validation
   - Write manifest at container spawn

2. **Document extension points**
   - ARCHITECTURE.md section on extensions
   - Example: adding a new channel
   - Example: adding a new action

3. **Clean interfaces**
   - Add `Capabilities()` to Channel
   - Add `Validate()` to Action
   - Add `Tier` to ActionContext

4. **Test extension points**
   - Mock channel implementation
   - Mock action implementation
   - Extension point test suite

## Non-Goals (v2m3)

- External plugin loading (defer to v3)
- Plugin marketplace (defer indefinitely)
- Hot reload (defer to v3)
- WebAssembly support (evaluate later)
