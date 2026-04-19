---
status: shipped
---

# Dashboard: Memory View & Edit

Per-group memory browser in dashd: view and edit `MEMORY.md`,
`diary/YYYYMMDD.md`, `facts/*.md`, `.claude/CLAUDE.md`.

## Routes

```
/dash/memory/              HTML browser (group select via ?group=...)
PUT    /dash/memory/:folder/:rel    write file content (body = bytes)
DELETE /dash/memory/:folder/:rel    remove file
```

Auth: JWT + group access (tier 0–1 all groups, tier 2 own group).

## Path safety

Allow only `MEMORY.md`, `diary/*.md`, `facts/*.md`, `.claude/CLAUDE.md`.
Reject `..`, absolute paths. Resolve via
`groupfolder.resolveGroupFolderPath`.

## Conflicts

No locking. Last write wins. Agent re-reads on session start.

## Out of scope

Diff view, collaborative editing, semantic search.
