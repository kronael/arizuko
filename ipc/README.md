# ipc

MCP server on unix socket — the in-container agent's only way to the host.

## Purpose

One MCP server per group at `ipc/<folder>/gated.sock`. Action tools
(send/reply/post/like/…, `schedule_task`, `register_group`,
`set_routes`, …) mutate state; the `inspect_*` family is read-only
introspection. Tools are filtered per-caller by `auth.Authorize`
against derived grant rules. Identity (folder, tier) is resolved from
the socket path; the kernel-attested peer uid (`SO_PEERCRED`) gates
every connection.

## Tool surface

Social verbs (chanreg-backed, `*UnsupportedError`-aware):

- `send`, `reply`, `post`, `like`, `dislike`, `delete`, `forward`,
  `quote`, `repost`, `edit`, `send_file`

(Names are post-rename: `send_message`→`send`, `send_reply`→`reply`,
`react`→`like`, `score_down`→`dislike`, `delete_post`→`delete`. No
aliases.)

Routing / groups: `register_group`, `escalate_group`, `delegate_group`,
`refresh_groups`, `list_routes`, `set_routes`, `add_route`,
`delete_route`, `get_grants`, `set_grants`, `fetch_history`,
`get_history`, `inspect_messages`.

Read-only introspection: `inspect_routing`, `inspect_tasks`,
`inspect_session`, `inspect_identity`. Tier 0 sees all instances;
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
- `inspect.go` — read-only introspection (`inspect_routing`, `inspect_tasks`, `inspect_session`)
- `SECURITY.md` — threat model (peer UID, path resolution, grants)

## Related docs

- `ARCHITECTURE.md` (IPC section)
- `specs/7/33-inspect-tools.md`
- `SECURITY.md` — root threat model
