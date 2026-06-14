# ipc

MCP host — the in-container agent's only way to the host. Runs as a
subsystem inside the `routd` process; per `specs/5/5-uniform-mcp-rest.md`
this is the **MCP host** issuer in the platform-token model.

## Purpose

One MCP server per group at `ipc/<folder>/gated.sock`, JSON-RPC over a
unix socket, one connection per agent container. Concurrent connections
bounded to 8 per socket. Action tools (send/reply/post/like/…,
`schedule_task`, `register_group`, `set_routes`, …) mutate state; the
`inspect_*` family is read-only introspection. Tools are filtered by
grant rules; identity (folder, tier) is resolved from the socket path;
the kernel-attested peer uid (`SO_PEERCRED`) gates every connection.

## Capability token (planned, per `specs/5/5-uniform-mcp-rest.md` §"Issuance sites")

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

**Today (shipped):** every tool runs in-process inside routd. Tools
calling tables routd owns (groups, routes, sessions, channels,
messages, grants) hit `store.*` directly. Tools touching
foreign-owned tables (invite ops → `invites` in onbod) **also** hit
the shared SQLite directly — that's the cross-boundary leak
`specs/6/7` closes.

**Planned (per spec Phase 4):** when a tool touches routd-owned tables
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
| social verbs, `*_routes`, `*_grants`, `inspect_*` | groups, routes, sessions, channels, messages, grants (routd) | local in-process |
| `schedule_task`, task ops, `inspect_tasks`        | scheduled_tasks, task_run_logs (timed)                       | HTTP → timed     |
| invite/admission ops                              | invites, admissions, auth_users (onbod)                      | HTTP → onbod     |

## Tool surface

Social verbs (chanreg-backed, `*UnsupportedError`-aware):

- `send`, `reply`, `post`, `like`, `dislike`, `delete`, `forward`,
  `quote`, `repost`, `edit`, `pin_message`, `unpin_message`,
  `unpin_all`, `send_file`, `send_voice`

Routing / groups: `register_group`, `escalate_group`, `delegate_group`,
`list_routes`, `set_routes`, `add_route`, `delete_route`, `add_acl`,
`remove_acl`, `inject_message`, `fork_topic`, `reset_session`,
`observe_group`, `unobserve_group`, `set_observe_window`,
`set_group_open`, `engage`, `disengage`.

Tasks: `schedule_task`, `list_tasks` (plus `inspect_tasks` in read-only).

Invites: `invite_create`, `invite_list`, `invite_revoke`.

Route tokens: `issue_chat_link`, `issue_webhook`, `list_tokens`,
`revoke_token`.

Network: `network_allow`, `network_deny`, `network_list`.

Web routes: `set_web_route`, `del_web_route`, `list_web_routes`,
`get_web_presence`.

Slack pane controls: `pane_set_prompts`, `pane_set_title`.

Cost tracking: `log_external_cost`.

MCP-subprocess connectors: dynamically registered from catalog (spec 7/Y).

Per-turn agent output flows back over the same socket via the
`submit_turn` JSON-RPC method (hidden from `tools/list`);
`submit_status` delivers mid-turn progress notices. Stdout from the
container is discarded.

Read-only introspection: `inspect_messages`, `inspect_routing`,
`inspect_tasks`, `inspect_session`, `inspect_identity`, `find_messages`.
Tier 0 sees all instances; tier ≥1 is scoped to its folder subtree.
Replaces ad-hoc `Bash sqlite3 …` audits.

`find_messages` is FTS5-backed full-text search (spec 5/C): bare
tokens, `"exact phrase"`, `a OR b`, `a NOT b`, `prefix*`,
`NEAR(a b, 5)`. Optional `scope` (chat_jid or folder subtree), `sender`,
`since` (RFC3339), `limit` (default 20, max 200). ACL gate is
post-fetch `JIDRoutedToFolder` per row.

## Public API

- `ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, rules []string, expectedUID int, callerSub string) (stop func(), err error)`
  — `expectedUID` is the kernel-attested uid required on every accept
  (1000 = ant image's `node` user in prod; ≤0 disables the check for
  tests). `callerSub` is the agent's auth subject, stamped into audit
  rows as the actor. Returns a stop func that tears down the listener.
- `GatedFns` — callbacks into the host daemon, routd (enqueue, register
  channel, run container, social verbs).
- `StoreFns` — callbacks into store (typed subset, not the full `*store.Store`)
- `TurnResult`, `ModelUsage` — turn submission payloads
- `RouteTokenInfo` — route token metadata
- `PlatformHistory`, `ErroredChat`, `TaskRunLog`, `NetworkRule` — DTO types

## Dependencies

- `core`, `auth`, `grants`, `router`, `mark3labs/mcp-go`

## Files

- `ipc.go` — server wiring, action tools, `SO_PEERCRED` accept gate,
  social verbs, routing/groups, tasks, invites, tokens, network
- `inspect.go` — read-only introspection (`inspect_routing`,
  `inspect_tasks`, `inspect_session`, `inspect_identity`,
  `inspect_messages`, `find_messages`)
- `connector.go` — MCP-subprocess tool broker (spec 7/Y)
- `SECURITY.md` — threat model (peer UID, path resolution, grants)

## Related docs

- `ARCHITECTURE.md` (IPC section)
- `specs/5/30-inspect-tools.md`
- `specs/5/5-uniform-mcp-rest.md` — MCP host's role as token issuer +
  HTTP-federation pattern for foreign-domain tools
- `specs/7/Y-connectors.md` — MCP-subprocess tool catalog
- `../auth/README.md` — `Authorize` / identity resolution
- `../routd/README.md` — host process; routd-owned tables stay local
- `../SECURITY.md` — root threat model
