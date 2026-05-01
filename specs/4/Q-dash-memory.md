---
status: shipped
---

# Dashboard: Memory View & Edit

Per-group memory browser in dashd: view and edit `MEMORY.md`,
`.claude/CLAUDE.md`, and flat `*.md` entries under `diary/`,
`facts/`, `users/`, `episodes/`.

## Routes

```
/dash/memory/                       HTML browser (group select via ?group=...)
PUT    /dash/memory/:folder/:rel    write file content (body = bytes)
DELETE /dash/memory/:folder/:rel    remove file
```

Auth: JWT enforced upstream by `proxyd`. dashd has no per-tier
group-scoping today — any authorized caller may read or edit any
group's memory. Tighten if/when needed.

## Path safety

Allow only `MEMORY.md`, `.claude/CLAUDE.md`, and `*.md` directly under
`diary/`, `facts/`, `users/`, `episodes/` (no nesting). Reject `..`,
absolute paths, symlink escapes from the group root. Implementation:
`memoryPathAllowed` + `resolveMemoryFile` in `dashd/main.go`.

## Conflicts

No locking. Last write wins. Agent re-reads on session start.

## Out of scope

Diff view, collaborative editing, semantic search.
