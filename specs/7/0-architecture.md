# Service Architecture

## Daemons

All daemons follow 4+d naming.

| Daemon  | Status  | Role                         | Spec                             |
| ------- | ------- | ---------------------------- | -------------------------------- |
| `gated` | running | Gateway, routing, containers | `specs/7/9-gated.md`             |
| `timed` | running | Cron poll, writes messages   | `specs/7/8-scheduler-service.md` |
| `icmcd` | running | MCP server, identity+auth    | `specs/7/10-icmcd.md`            |
| `authd` | running | Authorization policy         | `specs/7/11-authd.md`            |
| `teled` | running | Telegram adapter             |                                  |
| `discd` | planned | Discord adapter              |                                  |
| `whapd` | planned | WhatsApp adapter             |                                  |
| `emaid` | planned | Email adapter                |                                  |

Channel adapters are external — they use HTTP because they
may run on remote hosts. See `specs/7/1-channel-protocol.md`.

## Communication

```
                     SQLite (messages.db)
                    /        |         \
               gated       timed      icmcd/authd
                 |
            HTTP (:8080)
                 |
         docker network
        /    |    \    \
     teled discd whapd emaid
```

- **Co-located daemons** (gated, timed, icmcd, authd) share
  one SQLite file. No IPC between them — each reads/writes
  the DB independently.
- **Channel adapters** connect to gated via HTTP. They
  self-register, deliver inbound messages, receive outbound.
- **Agent containers** connect to icmcd via MCP unix socket.
  icmcd stamps identity and calls authd.Authorize for
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
(docker-compose.yml generation), `icmcd/` (socket paths).
