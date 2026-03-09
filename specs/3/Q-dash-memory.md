# Dashboard: Memory View & Edit

**Status**: open

Web UI for viewing and editing agent memory files (MEMORY.md,
diary entries, facts). Operators can inspect what the agent
knows, correct mistakes, and prune stale entries.

## Scope

Per-group memory browser. Each group has:

- `MEMORY.md` — long-term knowledge (editable)
- `diary/YYYYMMDD.md` — daily work log (editable)
- `facts/*.md` — knowledge base (when atlas ships, editable)

## Architecture

```
/dash/memory/:folder/              → memory browser for group
/dash/memory/:folder/api/files     → list memory files
/dash/memory/:folder/api/file      → read/write single file
```

Auth: JWT + group access check (tier 0-1 all groups, tier 2
own group only).

## API

### `GET /api/files`

Returns tree of memory-related files:

```json
{
  "memory": { "path": "MEMORY.md", "size": 1024, "modified": "..." },
  "diary": [{ "path": "diary/20260309.md", "size": 512, "modified": "..." }],
  "facts": []
}
```

### `GET /api/file?path=MEMORY.md`

Returns file content as plain text.

### `PUT /api/file?path=MEMORY.md`

Writes file content. Body is plain text. Creates parent dirs
if needed. Validates path is within allowed set (MEMORY.md,
diary/_, facts/_) — rejects traversal attempts.

## Frontend

Single HTML page with three panels:

1. **File tree** (left) — expandable list of memory/diary/facts
2. **Editor** (center) — textarea with markdown content, save button
3. **Preview** (right, optional) — rendered markdown preview

No framework. Vanilla HTML + fetch. Textarea with monospace
font. Save triggers PUT, shows success/error toast.

## Path safety

Only allow reads/writes to:

- `MEMORY.md`
- `diary/*.md`
- `facts/*.md`
- `.claude/CLAUDE.md`

Reject paths with `..`, absolute paths, or outside the allow
list. Use `group-folder.ts:resolveGroupFolderPath()` for base
path resolution.

## Conflict handling

No locking for v1. Last write wins. Agent and operator can
both edit MEMORY.md — acceptable because edits are infrequent
and the agent re-reads on each session start.

## Out of scope

- Diff view / version history — use git log on group folder
- Collaborative editing — single operator at a time
- Semantic search over facts — depends on atlas embeddings
