---
status: shipped
---

# Dashboards

Tile-based operator portal. Each subsystem has a dashboard; portal shows
summary tiles with health indicators. All read-only.

`dashd` HTTP server with auth middleware and six dashboards: Portal,
Status, Tasks, Activity, Groups, Memory. Inline Go templates, HTMX for
live updates (no frontend build).

## dashd daemon

Standalone, reads shared SQLite read-only (WAL). Own HTTP port.

- Binary: `dashd/main.go`
- Config: `DASH_PORT` (default 8090), `DATA_DIR`, `DB_PATH`
- DB: `?mode=ro`
- Templates: embedded `html/template`
- HTMX from CDN
- Included in generated docker-compose.yml

Registers in the channels table on startup:

```
name:         "dashd"
url:          "http://dashd:8090"
capabilities: {receive_only: true}
```

`/status` is routed by gated via channels table (HTTP POST to dashd's
`/send`). Routing entry: `match=/status → dashd`. dashd replies via
`notify/` or HTTP POST back to requesting JID's adapter.

## Auth

All `/dash/*` routes use auth middleware for JWT cookie verification.
dashd imports `auth` as a library.

## URL convention

```
/dash/                       portal (tile grid)
/dash/<name>/                dashboard page
/dash/<name>/x/<fragment>    HTMX partial
/dash/<name>/api/<path>      JSON API
```

## Portal

Tile grid. Each tile: title, one-line status, health dot
(green/yellow/red). Max-width 900px, monospace, 2-col grid. Auto-refresh 30s.

## Status

| Section    | Content                                                       | Refresh |
| ---------- | ------------------------------------------------------------- | ------- |
| Banner     | version, uptime, channel/container count, green/yellow/red bg | 5s      |
| Channels   | name, status, msg count 24h. Disconnected = red               | 30s     |
| Groups     | name, folder, tier, active dot, queue depth. Expandable       | 10s     |
| Containers | name, group, status, uptime, idle                             | 10s     |
| Queue      | JID, group, pending, failures, circuit breaker state          | 5s      |
| Errors     | last 20 from task_run_logs + queue failures. Expandable       | 5s      |
| Onboarding | pending: JID, name, age, approve command                      | 30s     |

Onboarding only when `ONBOARDING_ENABLED=1`.

Fragments: `banner`, `channels`, `groups`, `containers`, `queue`,
`errors`, `group-detail?folder=<f>`, `onboarding`.
API: `api/state`, `api/errors`, `api/onboarding`.
Health: ok = 0 failures + all channels connected; warn = failures > 0
or max containers; error = channel down or circuit breaker tripped.

## Tasks

Scheduled tasks, run history, failure details.

| Section         | Content                                           | Refresh  |
| --------------- | ------------------------------------------------- | -------- |
| Summary         | total/active/paused/failed(24h) counts            | 10s      |
| Task list       | ID, group, cron, next run, status, last run       | 10s      |
| Detail (expand) | full config, run history (20), next 5 run times   | on-click |
| Filters         | group dropdown, status (all/active/paused/failed) | -        |

Fragments: `summary`, `list?group=<f>&status=<s>`, `detail?id=<id>`,
`runs?id=<id>`. API: `api/tasks`, `api/tasks/:id`,
`api/runs?task_id=<id>&limit=20`.
Health: ok = no failed runs 24h; warn = 1+ failed; error = 3+ consecutive
failures on any task.

## Activity

Message flow and routing. Text truncated to 80 chars (privacy).

| Section      | Content                                                         | Refresh |
| ------------ | --------------------------------------------------------------- | ------- |
| Summary      | 24h: total, chats, senders, per-channel breakdown               | 30s     |
| Recent msgs  | last 50: time, channel, chat, sender, group, 80-char preview    | 10s     |
| Active chats | JID, channel, group, msg count, last msg time. Clickable        | 30s     |
| Flow         | per-group volume bars (24h), Unicode block chars                | 60s     |
| Routes       | read-only routing table grouped by JID. Template targets marked | 60s     |

Fragments: `summary`, `recent?chat=<jid>`, `chats`, `flow`, `routes`.
API: `api/summary`, `api/recent?limit=50&chat=<jid>`, `api/chats`, `api/routes`.
Health: ok = messages in last 1h; warn = none in 1h; error = none in 24h.

## Groups

Group hierarchy, routing, world/tier structure.

| Section         | Content                                                                                | Refresh  |
| --------------- | -------------------------------------------------------------------------------------- | -------- |
| Summary         | total groups, worlds (tier 0), active count                                            | 30s      |
| Tree            | hierarchical indented view, tier + active badges                                       | 30s      |
| Detail (expand) | config, routes, queue, container, knowledge counts, task count                         | on-click |
| Routes          | table grouped by JID. Color: command=blue, pattern=purple, sender=orange, default=grey | 60s      |
| World map       | text visualization of tier hierarchy per world                                         | 60s      |

Fragments: `summary`, `tree`, `detail?folder=<f>`, `routes`, `worlds`.
API: `api/groups`, `api/group?folder=<f>`, `api/routes`, `api/worlds`.
Health: always ok.

## Memory

Per-group knowledge browser. Read-only file viewer. No auto-refresh.

| Section   | Content                                            |
| --------- | -------------------------------------------------- |
| Selector  | group dropdown, reloads sections                   |
| MEMORY.md | full content in `<pre>`, size + mtime              |
| CLAUDE.md | collapsible `<details>`                            |
| Diary     | last 30 entries, date + first line. Expandable     |
| Episodes  | grouped by type (daily/weekly/monthly). Expandable |
| Users     | `users/*.md`, filename + first line                |
| Facts     | `facts/*.md`, filename + summary frontmatter       |
| Search    | substring across all stores for selected group     |

Fragments: `selector`, `memory?group=<f>`, `claude-md?group=<f>`,
`diary?group=<f>`, `diary-entry?group=<f>&file=<n>`,
`episodes?group=<f>`, `users?group=<f>`, `facts?group=<f>`,
`file?group=<f>&path=<p>`, `search?group=<f>&q=<q>`.
API: `api/groups` (with file counts), `api/files?group=<f>`,
`api/file?group=<f>&path=<p>`, `api/search?group=<f>&q=<q>`.
Path safety: reject `..`, absolute paths, outside allowlist. Use
`groupfolder.Resolve()`.

## Not in scope

- Mutations (kill, restart, clear, edit, pause)
- WebSocket / SSE (HTMX polling sufficient)
- Frontend build tooling
- Mobile layout
- Full message content viewing
- Session transcript browsing
