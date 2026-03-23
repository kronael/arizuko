# Service Architecture

## Naming convention

Daemons end in `d` (4+d): `gated`, `timed`, `onbod`, `dashd`.
Libraries don't: `auth`, `ipc`, `grants`, `notify`.

## Daemons

| Daemon  | Status  | Role                         | Spec                             |
| ------- | ------- | ---------------------------- | -------------------------------- |
| `gated` | running | Gateway, routing, containers | `specs/4/9-gated.md`             |
| `timed` | running | Cron poll, writes messages   | `specs/4/8-scheduler-service.md` |
| `onbod` | running | Onboarding state machine     | `specs/4/21-onboarding.md`       |
| `dashd` | running | Operator dashboards (HTMX)   | `specs/4/25-dashboards.md`       |
| `teled` | running | Telegram adapter             |                                  |
| `discd` | running | Discord adapter              |                                  |
| `whapd` | running | WhatsApp adapter             |                                  |
| `emaid` | planned | Email adapter                |                                  |

## Libraries

| Package  | Spec                          | Role                          |
| -------- | ----------------------------- | ----------------------------- |
| `auth`   | `specs/4/11-auth.md`          | Identity, authorization, JWT  |
| `ipc`    | `specs/4/10-ipc.md`           | MCP server, per-group sockets |
| `grants` | `specs/4/19-action-grants.md` | Grant rule engine             |
| `notify` | `specs/4/20-control-chat.md`  | Operator notifications        |

Channel adapters are external — they use HTTP because they
may run on remote hosts. See `specs/4/1-channel-protocol.md`.

## Communication

```
                      SQLite (messages.db)
                   /    |    \     \      \
              gated  timed  onbod  dashd  ipc/auth
                |
           HTTP (:8080)          channels table
                |              /    |    \    \    \
         docker network     teled discd whapd onbod dashd
        /    |    \    \       (+ agent: implicit, docker run)
     teled discd whapd emaid
```

- **Co-located services** (gated, timed, onbod, dashd, ipc,
  auth) share one SQLite file. No IPC between them — each
  reads/writes the DB independently.
- **Shared libraries**: auth (auth/policy), grants (rule
  engine), notify (operator notifications). Imported by any
  service that needs them.
- **All services register in the channels table** — external
  adapters (teled, discd, whapd) and internal services
  (onbod, dashd) use the same registration mechanism.
  See `specs/4/1-channel-protocol.md`.
- **Route targets** are either a group folder path (contains
  `/`) or a service name (no `/`). Folder paths → write to
  messages table. Service names → channels table lookup →
  HTTP POST to registered URL.
- **Agent is an implicit channel** — currently hardcoded as
  `docker run` in gated. Conceptually a channel named
  `agent`. Future: registers as `http://agentd:8092`.
- **Agent containers** connect to ipc via MCP unix socket.
  ipc stamps identity and calls auth.Authorize for
  runtime authorization before executing tool calls.

## Shared database

One `messages.db`, WAL mode, busy timeout. Each daemon opens
the same file, runs its own migration runner on startup.

```sql
CREATE TABLE migrations (
  service TEXT NOT NULL,
  version INTEGER NOT NULL,
  applied_at TEXT NOT NULL,
  PRIMARY KEY (service, version)
);
```

Shared tables (any daemon can read/write): `messages`, `chats`.
All other tables are owned by a single daemon — see individual
daemon specs for schema.

## Path translation (docker-in-docker)

gated runs in Docker and spawns child containers via the
host Docker daemon. Gateway-internal paths must be
translated to host-side paths for child volume mounts.

Two env vars, computed once at startup in `core.LoadConfig`:

| Var             | Purpose                        |
| --------------- | ------------------------------ |
| `HOST_DATA_DIR` | Host path to instance data dir |
| `HOST_APP_DIR`  | Host path to application dir   |

Each mount path is constructed explicitly from these
constants. No string replacement or path rewriting at
runtime. When unset, defaults to the gateway's own
filesystem paths (native mode, no translation needed).

Used by: `container/` (volume mounts), `compose/`
(docker-compose.yml generation), `ipc/` (socket paths).
