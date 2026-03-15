# Service Architecture

## Daemons

All daemons follow 4+d naming.

| Daemon  | Role                         | Spec                             |
| ------- | ---------------------------- | -------------------------------- |
| `gated` | Gateway, routing, containers | `specs/7/9-gated.md`             |
| `timed` | Cron poll, writes messages   | `specs/7/8-scheduler-service.md` |
| `actid` | MCP identity stamping        | `specs/7/10-actid.md`            |
| `authd` | Authorization policy         | `specs/7/11-authd.md`            |
| `teled` | Telegram adapter             |                                  |
| `discd` | Discord adapter              |                                  |
| `whapd` | WhatsApp adapter             |                                  |
| `emaid` | Email adapter                |                                  |

Channel adapters are external — they use HTTP because they
may run on remote hosts. See `specs/7/1-channel-protocol.md`.

## Communication

```
                     SQLite (messages.db)
                    /        |         \
               gated       timed      authd
                 |
            HTTP (:8080)
                 |
         docker network
        /    |    \    \
     teled discd whapd emaid
```

- **Co-located daemons** (gated, timed, actid, authd) share
  one SQLite file. No IPC between them — each reads/writes
  the DB independently.
- **Channel adapters** connect to gated via HTTP. They
  self-register, deliver inbound messages, receive outbound.
- **Agent containers** connect to actid via MCP unix socket.
  actid stamps identity and routes tool calls to the
  appropriate consumer (gated or timed). Consumers call
  authd to authorize.

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
