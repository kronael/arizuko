# groupfolder

Group-folder path resolution and validation.

## Purpose

Translates a `folder` string into filesystem paths (`groups/<folder>`,
`ipc/<folder>/gated.sock`, `groups/<folder>/media/<YYYYMMDD>`). Rejects
traversal attempts (`..`, absolute paths, symlink escapes). `IsTopLevel`
reports whether a folder is a world (no `/`). It is a shape test, not a
privilege check — capability comes from `acl` rows.

## Containment is a string prefix

A folder contains another when the child equals it or starts with the folder
plus a separator: `/` before a child folder, `#` before a topic
(`Contains`, `ContainsSep`). The separator suffix is the rule, so `acme`
never contains `acmecorp`. A database `LIKE` is never the test: `_` and `%`
are wildcards in it, and case folding differs by engine. An empty parent
contains nothing, so a missing folder cannot read as the root.

The path IS the hierarchy: there is no closure table, adjacency list or nested
set. A folder-keyed entity keeps the path in its sort key inside one
partition, so one prefix query covers a whole subtree (`objstore/README.md`
§ The shape of an item).

## Public API

- `Resolver` — constructed with base group + ipc dirs
- `(*Resolver).GroupPath(folder)`, `IpcPath(folder)`, `EnsureWithinBase(path)`
- `IsTopLevel(folder string) bool`
- `Contains(parent, child) bool`, `ContainsSep(parent, child, sep) bool`
- `IpcInputDir(ipcDir)`, `IpcSocket(ipcDir)`, `GroupMediaDir(groupDir, day)`

## Dependencies

None (stdlib only).

## Files

- `folder.go`
