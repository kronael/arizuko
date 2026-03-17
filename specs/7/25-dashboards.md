# Dashboards

**Status**: design

Tile-based operator portal for monitoring and inspecting gateway
state. Each subsystem has a dedicated dashboard; the portal shows
summary tiles with health indicators.

## URL Convention

```
/dash/                       -> portal (tile grid)
/dash/<name>/                -> dashboard page
/dash/<name>/x/<fragment>    -> HTML fragment (HTMX partial)
/dash/<name>/api/<path>      -> JSON API
```

## Portal

`GET /dash/` renders tile grid. Each dashboard gets a tile:
title, one-line status, health indicator (green/yellow/red dot).
Tiles link to full dashboard.

### Health

Each dashboard provides `health()` returning
`{status: ok|warn|error, summary: string}`.

### Layout

Max-width 900px, centered. Monospace font. 2-column grid.
Auto-refresh every 30s.

## Dashboards

| Dashboard | Route           | Description                          |
| --------- | --------------- | ------------------------------------ |
| status    | /dash/status/   | Uptime, channels, containers, errors |
| tasks     | /dash/tasks/    | Scheduled tasks, run history         |
| memory    | /dash/memory/   | Knowledge stores, diary, episodes    |
| activity  | /dash/activity/ | Message flow, recent activity        |
| groups    | /dash/groups/   | Group tree, routing, config          |

## Status Dashboard

Health banner (version, uptime, memory), channels table,
groups table (expandable), containers, queue state, recent
errors (last 20). Most time-sensitive — errors refresh 5s.

Health: ok = 0 failures + all channels connected.
Warn = failures > 0 or containers at max.
Error = channel disconnected or circuit breaker tripped.

## Tasks Dashboard

Summary bar (total/active/paused/failed), task list table
(schedule, next run, status, last run), expandable detail
(run history, cron visualization, full config).

Health: ok = no failed runs 24h. Warn = 1+ failed.
Error = 3+ consecutive failures on any task.

## Activity Dashboard

24h summary (messages, chats, senders, per-channel breakdown),
recent messages (last 50, truncated 80 chars for privacy),
active chats, message flow (per-group volume bars), routing
table (read-only).

Health: ok = messages in last 1h. Warn = none in 1h.
Error = none in 24h.

## Groups Dashboard

Summary bar (total groups, worlds, active), group tree
(hierarchical, expandable to show routes/queue/containers),
routing table (full, grouped by JID), world map (tier
visualization).

Health: always ok (groups are static config).

## Memory Dashboard

Per-group knowledge browser. Stores: facts, diary, users,
episodes. Shows file count per store, recent entries,
summary fields. Read-only.

Health: ok = all groups have MEMORY.md. Warn = some missing.

## Go Implementation

Register dashboards in `api/` package. Each dashboard is a
handler function receiving `http.Request` and gateway context.
HTMX served from CDN (no build step). JSON API alongside
HTML fragments.

```go
type Dashboard struct {
    Name        string
    Title       string
    Handler     http.Handler
    Health      func() HealthStatus
}
```

## Auth

All `/dash/*` routes require auth (JWT cookie). Same middleware
as web proxy. See `specs/7/11-authd.md`.

## Not in scope

- Mutations (kill, restart, clear, edit)
- WebSocket (HTMX polling sufficient)
- Build tooling for dashboard frontends
- Mobile-optimized layout
