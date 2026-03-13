---
status: planned
---

# Dashboard: Status & Health

Operator dashboard showing gateway health, active containers,
message queues, and recent errors. Read-only — no mutations.

## What it shows

| Section       | Data source                        |
| ------------- | ---------------------------------- |
| Uptime        | `process.uptime()`                 |
| Channels      | `channels[]` connected status      |
| Containers    | `docker ps --filter name=nanoclaw` |
| Group queues  | `GroupQueue` internal state        |
| Recent errors | Last N error log entries           |
| Sessions      | `sessions` map (folder → id)       |

## Architecture

Static HTML page served from `/dash/status/`. No framework —
vanilla HTML + fetch + SSE for live updates.

```
/dash/status/           → static HTML (bundled or inline)
/dash/status/api/state  → JSON snapshot of gateway state
/dash/status/api/stream → SSE for real-time updates
```

Auth: same as gateway web (JWT from `auth.ts`). Not public.

## Gateway changes

### `web-proxy.ts`

New routes behind auth middleware:

```typescript
app.get('/dash/status/api/state', authMiddleware, (req, res) => {
  res.json({
    uptime: process.uptime(),
    channels: channels.map((c) => ({ name: c.name, connected: true })),
    groups: Object.entries(registeredGroups).map(([jid, g]) => ({
      jid,
      folder: g.folder,
      name: g.name,
      hasSession: !!sessions[g.folder],
    })),
    containers: getActiveContainers(),
  });
});
```

### Container list

`docker ps --filter name=nanoclaw --format json` parsed and
returned. Cached 5s to avoid hammering docker.

### SSE stream

Reuse existing SSE infrastructure from `specs/3/J-sse.md` once
auth is added. Status dashboard subscribes to a `status` channel
that emits container start/stop, queue depth changes, errors.

## Frontend

Single HTML file. Auto-refreshes state every 10s, SSE for
real-time container events. No build step — served as static.

Table layout: channels row, groups table with session/queue
status, containers table with uptime and resource usage.

Color coding: green (healthy), yellow (idle >5min), red (error).

## Out of scope

- Mutations (kill container, clear queue) — future
- Historical metrics / graphs — future
- Per-message inspection — use conversation logs
