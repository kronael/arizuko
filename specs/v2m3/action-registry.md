# Action Registry

**Status**: planning

Unify action handling between `actions/` package and `ipc/watcher.go`.

## Current State

Two parallel implementations:

```
actions/registry.go     ipc/watcher.go
├── Register()          ├── handleAction() switch
├── Get()               │   case "send_message":
├── All()               │   case "send_file":
├── Manifest()          │   case "reset_session":
│                       │   case "inject_message":
actions/messaging.go    │   case "register_group":
├── send_message        │   case "escalate_group":
├── send_file           │   case "delegate_group":
│                       │   case "set_routing_rules":
actions/groups.go       │   case "schedule_task":
├── register_group      │   case "pause_task":
├── delegate_group      │   case "resume_task":
├── set_routing_rules   │   case "cancel_task":
│                       │
actions/tasks.go        └── (direct implementation)
├── schedule_task
├── pause_task
├── resume_task
├── cancel_task
```

**Problem**: actions/ defines handlers but they're never called.
IPC watcher reimplements all logic inline.

## Proposed Design

### Option A: Registry Dispatch (Recommended)

```go
// ipc/watcher.go
func (w *Watcher) handleAction(typ string, data []byte, group string, tier int) (...) {
    action := actions.Get(typ)
    if action == nil {
        return false, nil, "unknown action: " + typ
    }

    ctx := &actions.Context{
        SourceGroup:  group,
        Tier:         tier,
        SendMessage:  w.deps.SendMessage,
        SendDocument: w.deps.SendDocument,
        // ... other deps
    }

    if err := action.Validate(data); err != nil {
        return false, nil, err.Error()
    }

    result, err := action.Handler(data, ctx)
    if err != nil {
        return false, nil, err.Error()
    }
    return true, result, ""
}
```

**Pros**:

- Single source of truth
- Schema validation built-in
- Actions self-document via Manifest()
- Easy to add new actions

**Cons**:

- Requires dependency injection refactor
- Context struct becomes large

### Option B: Keep Switch, Registry for Metadata

```go
// ipc/watcher.go - keep switch statement
func (w *Watcher) handleAction(...) (...) {
    switch typ {
    case "send_message":
        // inline implementation
    // ...
    }
}

// actions/ - metadata only
func init() {
    Register(&Action{
        Name:   "send_message",
        Desc:   "Send message to channel",
        Schema: map[string]any{"chatJid": "string", "text": "string"},
        // NO Handler - just metadata
    })
}
```

**Pros**:

- Minimal refactor
- Switch is explicit and debuggable

**Cons**:

- Two places to update for new actions
- Schema and implementation can drift

## Context Structure

Actions need access to gateway capabilities:

```go
type Context struct {
    // Identity
    SourceGroup string
    Tier        int

    // Channel operations
    SendMessage  func(jid, text string) error
    SendDocument func(jid, path, name string) error

    // Session operations
    ClearSession  func(folder string)
    InjectMessage func(jid, content, sender, senderName string) (string, error)

    // Group operations
    RegisterGroup    func(jid string, group core.Group) error
    GetGroups        func() map[string]core.Group
    DelegateToChild  func(folder, prompt, jid string, depth int) error
    DelegateToParent func(folder, prompt, jid string, depth int) error

    // Task operations
    CreateTask       func(t core.Task) error
    GetTask          func(id string) (core.Task, bool)
    UpdateTaskStatus func(id, status string) error
    DeleteTask       func(id string) error

    // Paths
    GroupsDir     string
    HostGroupsDir string
}
```

**Open Question**: Is this too large? Should it be split?

```go
// Alternative: scoped contexts
type ChannelContext interface {
    SendMessage(jid, text string) error
    SendDocument(jid, path, name string) error
}

type GroupContext interface {
    RegisterGroup(jid string, group core.Group) error
    GetGroups() map[string]core.Group
    DelegateToChild(folder, prompt, jid string, depth int) error
}

type Context struct {
    SourceGroup string
    Tier        int
    Channel     ChannelContext
    Groups      GroupContext
    Tasks       TaskContext
    Session     SessionContext
}
```

## Action Manifest

Written to container at spawn for agent discovery:

```json
{
  "actions": [
    {
      "name": "send_message",
      "description": "Send message to channel",
      "schema": {
        "type": "object",
        "properties": {
          "chatJid": { "type": "string" },
          "text": { "type": "string" }
        },
        "required": ["chatJid", "text"]
      },
      "tier": 3
    }
  ]
}
```

**Location**: `/workspace/ipc/actions.json`

**Open Question**: Should manifest include tier requirements?

- Tier 0-1 only: inject_message, register_group
- Tier 0-2: delegate_group, schedule_task
- All tiers: send_message, send_file

## Migration Plan

1. **Phase 1**: Add Context type to actions/
2. **Phase 2**: Update action handlers to use Context
3. **Phase 3**: Wire handleAction() to registry dispatch
4. **Phase 4**: Delete inline switch cases
5. **Phase 5**: Write manifest at container spawn

## Test Strategy

```go
// actions/registry_test.go
func TestDispatch(t *testing.T) {
    ctx := &Context{
        SourceGroup: "main",
        Tier:        0,
        SendMessage: func(jid, text string) error { return nil },
        // ... mock deps
    }

    result, err := Dispatch("send_message", []byte(`{
        "chatJid": "tg:123",
        "text": "hello"
    }`), ctx)

    // assert result
}

func TestTierEnforcement(t *testing.T) {
    ctx := &Context{Tier: 2}
    _, err := Dispatch("inject_message", []byte(`{}`), ctx)
    // assert unauthorized
}
```

## Open Questions

1. **Error types**: Should actions return typed errors?
   - `ErrUnauthorized`, `ErrValidation`, `ErrNotFound`?

2. **Async actions**: Some actions enqueue work (delegate, schedule).
   Should they return immediately or wait?

3. **Action hooks**: Pre/post processing for all actions?
   - Logging, metrics, audit trail

4. **Custom actions**: Can agents define new actions?
   - Register via IPC at startup?
   - WASM handlers?
