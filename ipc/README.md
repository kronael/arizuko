# ipc

MCP host — the in-container agent's only way to the host. Runs as a
subsystem inside the `gated` process; per `specs/6/R-platform-api.md`
this is the **MCP host** issuer in the platform-token model.

## Purpose

One MCP server per group at `ipc/<folder>/gated.sock`, JSON-RPC over a
unix socket, one connection per agent container. Action tools
(send/reply/post/like/…, `schedule_task`, `register_group`,
`set_routes`, …) mutate state; the `inspect_*` family is read-only
introspection. Tools are filtered per-caller by `auth.Authorize`
against derived grant rules. Identity (folder, tier) is resolved from
the socket path; the kernel-attested peer uid (`SO_PEERCRED`) gates
every connection.

## Capability token (planned, per `specs/6/R-platform-api.md` §"Issuance sites")

At agent socket bind, the MCP host mints a capability token via
`auth.Mint(...)` carrying:

- `sub: "agent:<folder>"`
- `folder: <group folder>`
- `tier: <resolved tier>`
- `scope: <snapshot of grants for this folder/tier>`
- `iss: "mcp-host"`

The token is handed to the agent at handshake time (env var or first
JSON-RPC reply — exact carrier TBD) and used as
`Authorization: Bearer <jwt>` on every cross-daemon HTTP call the
agent makes. Today the host does not mint tokens; tools execute
in-process against the local store.

## Tool federation (today vs planned)

**Today (shipped):** every tool runs in-process inside gated. Tools
calling tables gated owns (groups, routes, sessions, channels,
messages, grants) hit `store.*` directly. Tools touching
foreign-owned tables (`schedule_task` → `scheduled_tasks` in timed,
invite ops → `invites` in onbod) **also** hit the shared SQLite
directly — that's the cross-boundary leak `specs/6/7` closes.

**Planned (per spec Phase 4):** when a tool touches gated-owned tables
it stays an in-process Go call. When it touches a foreign daemon's
tables it becomes an HTTP forward to that daemon's `/v1/*` with the
agent's capability token:

```
agent → ipc.tools/call(pause_task, ...)
       → ipc.HasScope(token, "tasks", "write")          → 403 if no
       → ipc HTTP-PATCH timed/v1/tasks/{id} {paused}
              Authorization: Bearer <agent-token>
       → timed.VerifyHTTP + scope/folder check + execute
       → JSON-RPC result back to agent
```

The MCP host becomes a thin per-agent API gateway: local for own
domain, HTTP forward for cross-daemon. Single socket per agent stays
the model. Agent-tailored tool names (`send_voice`, `escalate_group`,
`schedule_task`) are preserved — each maps to one or a small
composition of `/v1/*` calls.

Resource ownership map (drives which tools forward):

| Tool family                                       | Backing tables                                               | After Phase 4    |
| ------------------------------------------------- | ------------------------------------------------------------ | ---------------- |
| social verbs, `*_routes`, `*_grants`, `inspect_*` | groups, routes, sessions, channels, messages, grants (gated) | local in-process |
| `schedule_task`, task ops, `inspect_tasks`        | scheduled_tasks, task_run_logs (timed)                       | HTTP → timed     |
| invite/admission ops                              | invites, admissions, auth_users (onbod)                      | HTTP → onbod     |

## Tool surface

Social verbs (chanreg-backed, `*UnsupportedError`-aware):

- `send`, `reply`, `post`, `like`, `dislike`, `delete`, `forward`,
  `quote`, `repost`, `edit`, `send_file`, `send_voice`

(Names are post-rename: `send_message`→`send`, `send_reply`→`reply`,
`react`→`like`, `score_down`→`dislike`, `delete_post`→`delete`. No
aliases.)

Routing / groups: `register_group`, `escalate_group`, `delegate_group`,
`refresh_groups`, `list_routes`, `set_routes`, `add_route`,
`delete_route`, `list_acl`, `fetch_history`,
`get_history` (deprecated alias), `get_thread`, `inject_message`.

Per-turn agent output flows back over the same socket via the
`submit_turn` JSON-RPC method (hidden from `tools/list`); stdout from
the container is discarded.

Read-only introspection: `inspect_messages`, `inspect_routing`,
`inspect_tasks`, `inspect_session`, `inspect_identity`. Tier 0 sees
all instances;
tier ≥1 is scoped to its folder subtree. Replaces ad-hoc
`Bash sqlite3 …` audits.

## Public API

- `ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, rules []string, expectedUID int) (stop func(), err error)`
  — `expectedUID` is the kernel-attested uid required on every accept
  (1000 = ant image's `node` user in prod; ≤0 disables the check for
  tests).
- `GatedFns` — callbacks into gated (enqueue, register channel, run container, social verbs)
- `StoreFns` — callbacks into store (typed subset, not the full `*store.Store`)
- `PlatformHistory`, `ErroredChat`, `TaskRunLog` — DTO types

## Dependencies

- `core`, `auth`, `grants`, `router`, `mark3labs/mcp-go`

## Files

- `ipc.go` — server wiring, action tools, `SO_PEERCRED` accept gate
- `inspect.go` — read-only introspection (`inspect_routing`, `inspect_tasks`, `inspect_session`, `inspect_identity`)
- `SECURITY.md` — threat model (peer UID, path resolution, grants)

## Related docs

- `ARCHITECTURE.md` (IPC section)
- `specs/5/30-inspect-tools.md`
- `specs/6/R-platform-api.md` — MCP host's role as token issuer +
  HTTP-federation pattern for foreign-domain tools
- `../auth/README.md` — `Mint`/`VerifyHTTP`/`HasScope`/`MatchesFolder` contract
- `../gated/README.md` — host process; gated-owned tables stay local
- `SECURITY.md` — root threat model
