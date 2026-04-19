---
status: superseded
superseded_by: ARCHITECTURE.md
---

# Service Architecture

Superseded by `/ARCHITECTURE.md` and CLAUDE.md "Service Architecture"
table. That is the live source for daemon/library inventory and
message flow.

Items unique to this doc (kept for reference):

## Path translation (docker-in-docker)

gated runs in Docker and spawns child containers via the host daemon.
Gateway-internal paths must be translated to host-side paths for child
volume mounts.

| Var             | Purpose                        |
| --------------- | ------------------------------ |
| `HOST_DATA_DIR` | Host path to instance data dir |
| `HOST_APP_DIR`  | Host path to application dir   |

Computed once in `core.LoadConfig`. Each mount path is constructed
explicitly from these constants. When unset, defaults to the gateway's
own paths (native mode, no translation). Used by `container/` volume
mounts, `compose/` generation, `ipc/` socket paths.

## Shared migrations table

```sql
CREATE TABLE migrations (
  service TEXT NOT NULL,
  version INTEGER NOT NULL,
  applied_at TEXT NOT NULL,
  PRIMARY KEY (service, version)
);
```

Per-daemon version rows. `gated` owns `messages.db` schema via
`store/migrations/`; other daemons connect read/write but never run
migrations.
