# Security

arizuko's security model, threat boundaries, and hardening notes. Each
daemon with non-trivial security surface has its own `SECURITY.md` next
to its source — this file is the index and the cross-cutting model.

## Model

arizuko is a **multi-tenant agent router**. Every security boundary
maps to one of three axes:

1. **Group isolation** — agents in one group must not see another
   group's files, sockets, DB rows, or messages.
2. **User isolation** — users reach groups only via ACL entries in
   `user_groups`. OAuth + JWT verify identity at the proxy edge.
3. **Daemon isolation** — daemons share SQLite but own disjoint
   schemas; adapters reach gated only via the internal docker
   network + a shared `CHANNEL_SECRET`.

The boundaries and where they live:

| Boundary                | Mechanism                                                                              | Location                                                   |
| ----------------------- | -------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| Group filesystem        | Per-group bind mount; `folders.GroupPath` validates folder, refuses `..`, reserved     | `groupfolder/folder.go`, `container/runner.go buildMounts` |
| Group MCP socket        | Per-group bind mount of `ipcDir` → `/workspace/ipc`; `folders.IpcPath` validates       | `container/runner.go buildMounts`                          |
| MCP peer identity       | `SO_PEERCRED` peer-uid check on each accepted conn                                     | `ipc/ipc.go ServeMCP`, see `ipc/SECURITY.md`               |
| Container resource      | Docker `--memory`, `--cpus`, `--pids-limit`, `--read-only` flags                       | `container/runner.go buildArgs`                            |
| Agent exec capability   | `bypassPermissions` in-container but mounts are scoped to the group                    | `ant/src/index.ts`, `container/runner.go`                  |
| Web routes              | Proxyd path table; `/pub/*` anon, `/slink/*` token, everything else JWT                | `proxyd/`                                                  |
| Authn                   | GitHub / Google / Discord OAuth → JWT (1h) + refresh cookie (30d)                      | `auth/`                                                    |
| Authz                   | `user_groups` ACL, `auth.MatchGroups`, grants engine                                   | `auth/`, `grants/`                                         |
| Channel ingress         | Shared `CHANNEL_SECRET` HMAC; adapter → gated over internal docker network only        | `chanlib/`, `api/`                                         |
| Rate limits             | Onboarding gates (per-day), slink (per-token), webd MCP (per-user)                     | `onbod/`, `webd/`                                          |
| Sender identity in chat | Ant stamps `userID` = sha256(folder) in MCP calls; gateway verifies tool-group mapping | `ant/src/index.ts`, `ipc/ipc.go`                           |

Anything not in this table is **not** a security boundary. In
particular: socket filesystem permissions alone do not separate
containers, and host root always wins — arizuko does not try to defend
against the host operator.

## Trust zones

```
┌─ host (trusted) ─────────────────────────────────────────┐
│                                                          │
│  ┌─ arizuko_<instance> (docker-compose, trusted) ──────┐ │
│  │                                                     │ │
│  │  gated   onbod   dashd   webd   proxyd   timed      │ │
│  │  teled   discd   mastd   whapd  ...                 │ │
│  │                                                     │ │
│  │  ┌─ agent container (per-group, partially trusted) ┐│ │
│  │  │  claude code + ant/ + skills                    ││ │
│  │  │  can run arbitrary Bash, Edit, WebFetch         ││ │
│  │  │  scoped to /workspace/ipc + group mounts        ││ │
│  │  └─────────────────────────────────────────────────┘│ │
│  └─────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ external channels (untrusted) ─────────────────────┐ │
│  │  telegram, discord, whatsapp, web slink, email      │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

## Per-daemon security docs

Daemons with security-relevant surface ship their own `SECURITY.md`:

- [`ipc/SECURITY.md`](ipc/SECURITY.md) — MCP channel, peer-uid check,
  per-group mount isolation, incident history

Other daemons do not have a dedicated file yet; their guarantees are
summarized in the table above. Add a `SECURITY.md` next to the daemon
when its threat model grows past a table row.

## Incident log

### 2026-04-17 → 2026-04-19 — MCP token preamble outage

A per-connection token preamble added to `ipc.ServeMCP` in commit
`2774394` was enforced on the server side but never implemented on the
client side (ant's socat bridge writes no preamble). Every MCP tool
call was silently rejected for ~48h. Agents appeared to lose memory
because `get_history` and every other gateway tool was unreachable.

Fixed in v0.29.3 (disable preamble) and replaced in v0.29.4 with
kernel-attested `SO_PEERCRED` peer-uid check. Full writeup:
[`ipc/SECURITY.md`](ipc/SECURITY.md).

## Reporting

Internal issue — log to `bugs.md` with reproducer and severity. No
public security channel yet; arizuko is single-operator per instance.
