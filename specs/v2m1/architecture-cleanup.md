# Architecture Cleanup

**Status**: partial

v2 Go port carries over v1 patterns. This spec documents cleanup needed
to restore minimality and proper separation.

## actions/ Package

~~Previously marked for deletion~~ — KEEP.

The `actions/` package is an intentional extension point. The duplication
between `actions/` and `ipc/watcher.go:handleAction()` is a bug to fix,
not dead code to delete.

**Fix**: Wire `handleAction()` to dispatch through `actions.Registry`.
See `specs/v2m3/action-registry.md` for design.

## IPC Deps Bloat

`ipc.Deps` struct has 13 fields mixing paths, callbacks, and complex functions.
Gateway injection (gateway.go:72-99) is 27 lines of wiring.

Current:

```go
type Deps struct {
    SendMessage      func(jid, text string) error
    SendDocument     func(jid, path, filename string) error
    ClearSession     func(folder string)
    GroupsDir        string
    HostGroupsDir    string
    IsRoot           func(folder string) bool
    RegisteredJids   func() map[string]bool
    InjectMessage    func(jid, content, sender, senderName string) (string, error)
    RegisterGroup    func(jid string, group core.Group) error
    GetGroups        func() map[string]core.Group
    DelegateToChild  func(childFolder, prompt, originJid string, depth int) error
    DelegateToParent func(parentFolder, prompt, originJid string, depth int) error
    CreateTask       func(t core.Task) error
    GetTask          func(id string) (core.Task, bool)
    UpdateTaskStatus func(id, status string) error
    DeleteTask       func(id string) error
}
```

**Proposed**: Group into focused interfaces:

```go
type Deps struct {
    Channel  ChannelDeps   // SendMessage, SendDocument
    Groups   GroupDeps     // RegisterGroup, GetGroups, DelegateToChild/Parent
    Tasks    TaskDeps      // CreateTask, GetTask, UpdateTaskStatus, DeleteTask
    Session  SessionDeps   // ClearSession, InjectMessage
    Paths    PathDeps      // GroupsDir, HostGroupsDir
    Auth     AuthDeps      // IsRoot, RegisteredJids
}
```

## Queue Leaks IPC Protocol

`queue/queue.go:187-222` writes IPC files directly:

- Knows `ipc/<folder>/input/` path structure
- Knows `<ts>-<rand>.json` naming convention
- Sends SIGUSR1 to container

**Proposed**: Move to ipc package:

```go
// ipc/queue.go
func QueueMessage(dataDir, folder, text string) error
func SignalContainer(name string) error
```

Queue calls these instead of manipulating files.

## Queue Uses Container (Not Dead)

`queue/queue.go:14` imports `container` — audit incorrectly flagged this.
Queue uses `container.Bin` and `container.StopContainerArgs()` for signaling
and stopping containers. This coupling is intentional.

## Gateway Monolith

gateway.go is 734 lines, imports 8+ packages, does:

- Message polling and routing
- Command handling (/new, /ping, /chatid)
- Session management
- IPC coordination
- Agent container orchestration
- Web server

**Proposed**: Keep as-is for v2m1. Revisit if it grows past 1000 LOC.

## Hierarchy Logic Duplication

`getTier()`, `worldOf()`, `isInWorld()`, `isDirectChild()` are defined in
ipc/watcher.go but the same concepts appear in:

- container/runner.go (isRoot checks)
- router/router.go (IsAuthorizedRoutingTarget)

**Proposed**: Move to groupfolder package:

```go
// groupfolder/hierarchy.go
func Tier(folder string) int
func WorldOf(folder string) string
func IsInWorld(source, target string) bool
func IsDirectChild(parent, child string) bool
```

## Task Action Boilerplate

pause_task, resume_task, cancel_task (lines 587-645) repeat identical auth
logic. Could consolidate to single handler with status parameter.

**Proposed**: Keep as-is. Explicit is clearer than clever.

## Priority Order

1. ~~Delete actions/ (immediate, -505 LOC)~~ — done
2. Extract hierarchy helpers to groupfolder (v2m2)
3. Refactor Deps struct (v2m2)
4. Move IPC file writing from queue to ipc (v2m2)

## Non-Issues

These were flagged but are acceptable:

- **Logging in IPC actions**: Useful for debugging production issues
- **Tier system complexity**: Matches kanipi semantics, needed for compat
- **lastMessageDate state**: Cheap, avoids repeated date parsing
- **Callback injection**: Standard Go pattern, no better alternative
