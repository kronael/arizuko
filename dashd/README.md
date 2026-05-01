# dashd

Operator dashboard daemon: HTMX views over `messages.db` plus
allow-listed memory editing on disk.

## Purpose

Standalone HTMX portal for operators. Opens SQLite read-only. Six views:
portal, status, tasks, activity, groups, memory. Auth is enforced upstream
by `proxyd`'s `requireAuth` middleware; dashd itself assumes the caller
is authorized and does not further scope responses by group.

## Responsibilities

- Serve `/dash/` portal and `/dash/<name>/` pages.
- Serve HTMX partials at `/dash/<name>/x/<frag>`.
- Read diary and MEMORY.md via `diary` package; cap file reads to 1 MiB.
- Memory edit: `PUT`/`DELETE /dash/memory/<folder>/<rel>` against an
  allow-list of `MEMORY.md`, `.claude/CLAUDE.md`, and flat `*.md`
  under `diary/`, `facts/`, `users/`, `episodes/`.
- Symlink-safe path resolution (`safeJoin`, `resolveMemoryFile`)
  against the groups dir sandbox.

## Entry points

- Binary: `dashd/main.go`
- Listen: `$DASH_PORT` (default `:8080`, also `PORT`)

## Dependencies

- `chanlib` (env helpers), `diary`, `theme`

## Configuration

- `DATA_DIR` or `DB_PATH` — resolves `<DATA_DIR>/store/messages.db`
- `DASH_PORT` — listen port
- `INSTANCE_NAME` — shown in portal header

## Health signal

`GET /health` returns 200 when DB is reachable. Typical deploy reaches
dashd through `proxyd` at `/dash/`; direct exposure requires `DASH_PORT`
mapped on the host.

## Files

- `main.go` — HTMX handlers, safe path join, capped file reads

## Related docs

- `specs/4/Q-dash-memory.md` — memory view & edit
- `specs/7/25-dashboards.md`
- `ARCHITECTURE.md` (Operator Dashboard section)
