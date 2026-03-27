---
status: planned
---

# Agent-Managed Services

Agents can define and run long-running services within their group, similar
to how they schedule tasks. A new `servd` daemon manages these containers
inside the compose stack — no root required.

---

## Problem

Agents can currently run one-shot containers (per-message via `gated`,
scheduled via `timed`). There is no way for an agent to run a persistent
service — a web server, a bot, an API — that outlives a single agent run.

---

## Permission Model

`servd` manages containers within each group's allocation using the Docker
socket — same as `gated`. No root required per operation.

Agents control their own group's services only. `servd` enforces the group
boundary. An agent cannot start, stop, or inspect another group's services.

Root-level concerns (VPS, systemd, firewall, host networking) are out of
scope — handled separately.

---

## Service Model

Services are declared declaratively. The agent writes a desired state;
`servd` reconciles actual state to match.

### Schema

```sql
CREATE TABLE services (
    id          TEXT PRIMARY KEY,
    folder      TEXT NOT NULL,          -- group folder (ownership boundary)
    name        TEXT NOT NULL,          -- stable identifier within group
    image       TEXT NOT NULL,          -- docker image:tag
    args        TEXT,                   -- JSON array of extra docker args
    env         TEXT,                   -- JSON object of env vars
    ports       TEXT,                   -- JSON array of {host, container, proto}
    restart     TEXT DEFAULT 'always',  -- always | on-failure | no
    desired     TEXT DEFAULT 'running', -- running | stopped
    actual      TEXT DEFAULT 'stopped',
    container   TEXT,                   -- docker container ID when running
    error       TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE (folder, name)
);
```

### Lifecycle

`servd` polls the `services` table on a short interval (~5s) and reconciles:

- `desired=running, actual=stopped` → start container
- `desired=stopped, actual=running` → stop container
- `desired=running, actual=running` → check container still alive; restart if dead
- `desired=stopped, actual=stopped` → nothing

Container names: `arizuko-svc-<sanitized_folder>-<name>` to avoid collisions.

### Deploy / Image Update

Agents do not trigger builds. The build pipeline (CI or operator) pushes
images to the registry and updates the `image` field in the services table
(or agents do it via `service_update`). `servd` detects the image tag change
on next reconcile and does a rolling restart (stop old → start new).

This keeps deployment declarative: change the image tag, `servd` converges.

---

## MCP Tools (IPC)

Six new tools exposed via the MCP server to agents:

| Tool              | Description                            |
| ----------------- | -------------------------------------- |
| `service_define`  | Create or update a service definition  |
| `service_start`   | Set desired=running (idempotent)       |
| `service_stop`    | Set desired=stopped                    |
| `service_restart` | Stop then start (rotate container)     |
| `service_logs`    | Fetch last N lines from container logs |
| `service_list`    | List services in the group with status |

All tools are scoped to the calling agent's group folder. Attempting to
operate on a different group's service returns an error.

`service_define` parameters:

```json
{
  "name": "myapi",
  "image": "my-image:latest",
  "env": { "PORT": "8080", "DB_URL": "..." },
  "ports": [{ "host": 8080, "container": 8080, "proto": "tcp" }],
  "restart": "always"
}
```

Port allocation: agents cannot bind to privileged ports (<1024). `servd`
validates port ranges and checks for conflicts across all groups.

---

## Crons: No Change

`timed` stays as-is. Agent-defined cron jobs continue to run as one-shot
containers managed by `timed`. No migration to systemd timers — that would
require root per-timer and adds complexity for no clear gain.

Systemd timers remain the domain of the operator (ansible-managed).

---

## Relation to Existing Daemons

| Daemon  | Manages                       | Triggered by     |
| ------- | ----------------------------- | ---------------- |
| `gated` | per-message containers        | incoming message |
| `timed` | cron one-shot containers      | schedule         |
| `servd` | persistent service containers | desired state    |

`servd` is a standalone binary like `timed` — separate process, shared SQLite
DB (WAL mode), registered in the compose stack.

---

## Sandbox Future

When Docker is replaced by OS-level sandboxes (namespace-per-group):

- Each group runs in an isolated network namespace
- Agents request port exposure via `service_define` (ports field) — same API
- `servd` translates that into network plumbing for the namespace
- The agent-facing MCP interface does not change

Gateway/firewall wiring at the host level is handled by `minsrv` (root skill),
not by `servd` or agents.

---

## Out of Scope

- **Virtual host management** (nginx reverse proxy entries for agent services):
  needs a privileged broker to reload nginx. Separate spec.
- **Inter-service discovery** (DNS/service mesh between group services):
  future. For now, agents use fixed ports.
- **Resource limits** (CPU/memory quotas per group): future. `servd` can
  pass `--cpus` / `--memory` to docker run once quotas are tracked.
