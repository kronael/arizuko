# groupfolder

Group-folder path resolution and validation.

## Purpose

Translates a `folder` string into filesystem paths (`groups/<folder>`,
`ipc/<folder>/gated.sock`, `groups/<folder>/media/<YYYYMMDD>`). Rejects
traversal attempts (`..`, absolute paths, symlink escapes). `IsRoot`
distinguishes tier-0 (root) from sub-worlds.

## Public API

- `Resolver` — constructed with base group + ipc dirs
- `(*Resolver).GroupPath(folder)`, `IpcPath(folder)`, `EnsureWithinBase(path)`
- `IsRoot(folder string) bool`
- `IpcInputDir(ipcDir)`, `IpcSocket(ipcDir)`, `GroupMediaDir(groupDir, day)`

## Dependencies

None (stdlib only).

## Files

- `folder.go`
