# Dashboards

**Status**: design

Tile-based operator portal for monitoring instance state.
Each subsystem has a dedicated dashboard; portal shows summary
tiles with health indicators. All read-only.

## Implementation: dashd

Standalone daemon like timed, teled. Reads shared SQLite DB
(read-only, WAL mode). Serves HTMX pages on its own HTTP port.

- Binary: `services/dashd/main.go`
- Config: `DASH_PORT` (default 8090), `DATA_DIR`, `DB_PATH`
- DB: opens store read-only (`?mode=ro`)
- File access: reads group folders (memory dashboard only)
- Templates: embedded Go `html/template`, no build step
- HTMX from CDN, no frontend tooling
- Included in generated docker-compose.yml

## Auth

All `/dash/*` routes use authd middleware for JWT cookie
verification. dashd imports `authd` as a library.

## URL Convention

```
/dash/                       portal (tile grid)
/dash/<name>/                dashboard page
/dash/<name>/x/<fragment>    HTMX partial
/dash/<name>/api/<path>      JSON API
```

## Portal

`GET /dash/` -- tile grid. Each tile: title, one-line status,
health dot (green/yellow/red). Max-width 900px, monospace,
2-column grid. Auto-refresh 30s.

---

## Status

Health, channels, containers, queues, errors.

| Section    | Content                                                       | Refresh |
| ---------- | ------------------------------------------------------------- | ------- |
| Banner     | version, uptime, channel/container count, green/yellow/red bg | 5s      |
| Channels   | name, status, msg count 24h. Disconnected = red               | 30s     |
| Groups     | name, folder, tier, active dot, queue depth. Expandable       | 10s     |
| Containers | name, group, status, uptime, idle                             | 10s     |
| Queue      | JID, group, pending, failures, circuit breaker state          | 5s      |
| Errors     | last 20 from task_run_logs + queue failures. Expandable       | 5s      |

**Fragments**: `banner`, `channels`, `groups`, `containers`, `queue`, `errors`, `group-detail?folder=<f>`
**API**: `api/state` (full snapshot), `api/errors` (recent errors)
**Health**: ok = 0 failures + all channels connected. Warn = failures > 0 or at max containers. Error = channel down or circuit breaker tripped.

---

## Tasks

Scheduled tasks, run history, failure details.

| Section         | Content                                                   | Refresh  |
| --------------- | --------------------------------------------------------- | -------- |
| Summary         | total/active/paused/failed(24h) counts                    | 10s      |
| Task list       | ID, group, cron + human gloss, next run, status, last run | 10s      |
| Detail (expand) | full config, run history (last 20), next 5 run times      | on-click |
| Filters         | group dropdown, status filter (all/active/paused/failed)  | -        |

**Fragments**: `summary`, `list?group=<f>&status=<s>`, `detail?id=<id>`, `runs?id=<id>`
**API**: `api/tasks`, `api/tasks/:id`, `api/runs?task_id=<id>&limit=20`
**Health**: ok = no failed runs 24h. Warn = 1+ failed. Error = 3+ consecutive failures on any task.

---

## Activity

Message flow and routing. Text truncated to 80 chars (privacy).

| Section      | Content                                                                    | Refresh |
| ------------ | -------------------------------------------------------------------------- | ------- |
| Summary      | 24h: total msgs, chats, senders, per-channel breakdown                     | 30s     |
| Recent msgs  | last 50: time(ago), channel, chat, sender, group, 80-char preview          | 10s     |
| Active chats | JID, channel, group, msg count, last msg time. Clickable -> filters recent | 30s     |
| Flow         | per-group volume bars (24h), Unicode block chars                           | 60s     |
| Routes       | read-only routing table grouped by JID. Template targets marked            | 60s     |

**Fragments**: `summary`, `recent?chat=<jid>`, `chats`, `flow`, `routes`
**API**: `api/summary`, `api/recent?limit=50&chat=<jid>`, `api/chats`, `api/routes`
**Health**: ok = messages in last 1h. Warn = none in 1h. Error = none in 24h.

---

## Groups

Group hierarchy, routing config, world/tier structure.

| Section         | Content                                                                                     | Refresh  |
| --------------- | ------------------------------------------------------------------------------------------- | -------- |
| Summary         | total groups, worlds (tier 0), active count                                                 | 30s      |
| Tree            | hierarchical indented view, tier + active badges. Expandable                                | 30s      |
| Detail (expand) | config, routes, queue state, container, knowledge file counts, task count                   | on-click |
| Routes          | full table grouped by JID. Color: command=blue, pattern=purple, sender=orange, default=grey | 60s      |
| World map       | text visualization of tier hierarchy per world                                              | 60s      |

**Fragments**: `summary`, `tree`, `detail?folder=<f>`, `routes`, `worlds`
**API**: `api/groups`, `api/group?folder=<f>`, `api/routes`, `api/worlds`
**Health**: always ok (groups are static config).

---

## Memory

Per-group knowledge browser. Read-only file viewer. No auto-refresh
(content changes infrequently).

| Section   | Content                                                     |
| --------- | ----------------------------------------------------------- |
| Selector  | group dropdown, reloads all sections                        |
| MEMORY.md | full content in `<pre>`, size + mtime                       |
| CLAUDE.md | collapsible `<details>`                                     |
| Diary     | last 30 entries newest-first, date + first line. Expandable |
| Episodes  | grouped by type (daily/weekly/monthly). Expandable          |
| Users     | `users/*.md`, filename + first line. Expandable             |
| Facts     | `facts/*.md`, filename + summary frontmatter. Expandable    |
| Search    | substring across all stores for selected group              |

**Fragments**: `selector`, `memory?group=<f>`, `claude-md?group=<f>`, `diary?group=<f>`, `diary-entry?group=<f>&file=<n>`, `episodes?group=<f>`, `users?group=<f>`, `facts?group=<f>`, `file?group=<f>&path=<p>`, `search?group=<f>&q=<q>`
**API**: `api/groups` (with file counts), `api/files?group=<f>`, `api/file?group=<f>&path=<p>`, `api/search?group=<f>&q=<q>`
**Path safety**: reject `..`, absolute paths, outside allowlist. Use `groupfolder.Resolve()`.
**Health**: ok = all groups have MEMORY.md. Warn = some missing.

---

## Not in scope

- Mutations (kill, restart, clear, edit, pause)
- WebSocket / SSE (HTMX polling sufficient)
- Frontend build tooling
- Mobile layout
- Full message content viewing
- Session transcript browsing
