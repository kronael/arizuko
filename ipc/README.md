# ipc

MCP host — the in-container agent's only way to the host. Runs as a
subsystem inside the `routd` process, one socket per turn.

## Purpose

One MCP server per group at `ipc/<folder>/gated.sock`, JSON-RPC over a
unix socket, one connection per agent guest. Concurrent connections
bounded to 8 per socket. Hot-tier action tools (send/reply/post/like/…,
`register_group`, `inject_message`, `engage`, …) mutate state; the
`inspect_*` family is read-only introspection. Cold-tier management
tools (`set_routes`/`schedule_task`/`network_allow`/…) are resreg-served,
not hand-rolled here — see "Tool surface" below. Identity (the folder) is
resolved from the socket path; the kernel-attested peer uid
(`SO_PEERCRED`) gates every connection. Tool visibility is
`auth.EffectiveActions` over the folder's `acl` rows (a tool the caller
holds at no scope is never registered); each call is then authorized by
the agent gate `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` routd
injects. One row set feeds both, so list and gate agree.

Hot vs cold tier: the **hot-tier** runtime actions (`reply`/`send`/
`like`/`inspect_*`/`engage`/`fork_topic`) are MCP-only, hand-authored,
and stay here — no REST twin. The **cold-tier** management tools
(`add_route`/`set_routes`/`set_secret`/`network_allow`/…) migrate onto
shared `resreg` handlers — one in-process handler serving both the REST
`/v1/*` face and this socket's generated MCP facade (`5/16`). ipc keeps
the unix-socket transport, the hot-tier tools, and the shared authz +
visibility closures (`authorizeCall`, `registerRaw`/`granted`); it loses
only the cold-tier per-tool bodies.

## The turn's credential is the socket

There is no minted agent token. The turn's identity is the unix socket
itself: routd binds one per turn at `ipc/<folder>/gated.sock`, chowns it to
the spawning uid, and every accept is checked against the kernel-attested
peer uid via `SO_PEERCRED`. The folder comes from the socket path, which the
host chose — never from anything the agent sends. Spec `5/17`'s auth-model
table names this directly: the agent surface's identity source is "the
in-container socket, folder-bound at spawn".

That is what makes the two layers work. Visibility is
`auth.EffectiveActions` over the folder's `acl` rows, so a tool the caller
holds at no scope is never registered; the call is then authorized by
`db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)`, the gate
routd injects. One row set feeds both, so the listing and the gate cannot
disagree.

## Cross-owner tools federate

A tool over routd's own tables is an in-process call. A tool over another
owner's tables is an HTTP call to that owner with the `service:routd` bearer —
`invite_create`/`invite_list`/`invite_revoke` go to onbod through
`routd/onbod.go`'s `httpOnbod`, not into onbod's tables. ipc never opens a
second owner's store.

The cold-tier management tools are not hand-rolled here at all: routd mounts
its resreg resources onto this server through the `postBuild` seams, so one
handler serves both `/v1/<name>` and the MCP facade with a different injected
Gate (spec `5/16`).

## Tool surface

Social verbs (chanreg-backed, `*UnsupportedError`-aware):

- `send`, `reply`, `post`, `like`, `dislike`, `delete`, `forward`,
  `quote`, `repost`, `edit`, `pin_message`, `unpin_message`,
  `unpin_all`, `send_file`, `send_voice`

Groups (hot-tier, hand-authored here): `register_group`, `escalate_group`,
`delegate_group`, `inject_message`, `fork_topic`, `reset_session`,
`observe_group`, `unobserve_group`, `set_observe_window`,
`set_group_open`, `engage`, `disengage`.
`role:member` includes `disengage`; a final answer cannot reopen a window
that the agent cleared during that turn.

Read-only: `inspect_tasks`, `get_web_presence` (a presence report, not
web_routes CRUD).

Invites: `invite_create`, `invite_list`, `invite_revoke`.

Route tokens: `issue_chat_link`, `issue_webhook`, `list_tokens`,
`revoke_token`.

**Cold-tier management (resreg-served, NOT hand-rolled here) — spec 5/16:**
the `routes` (`add_route`/`set_routes`/`list_routes`/`delete_route`),
`web_routes` (`set_web_route`/`del_web_route`/`list_web_routes`),
`network_rules` (`network_allow`/`network_deny`/`network_list`),
`scheduled_tasks` (`schedule_task`/`pause_task`/`resume_task`/`cancel_task`/
`list_tasks`), and `acl` (`add_acl`/`remove_acl`/`list_acl`) tools each ride
one `resreg.Resource`. routd owns the shared handler + tx/audit and mounts
them on this server through the `ServeMCP` postBuild seam, injecting a
the agent `Gate` + `EffectiveActions` visibility. ipc keeps the transport +
the hot-tier tools above, not these bodies.

Slack pane controls: `pane_set_prompts`, `pane_set_title`.

MCP HTTP connectors: each turn discovers the upstream catalog with that
folder's broker-resolved credential and registers the tools (spec 5/13).

External REST tools (`extcall.go` `ExtTool`): loaded from `[[ext]]` TOML
blocks as REST-backed MCP tools. Each maps a tool call to one outbound
HTTP request with path-param substitution and auth injected from folder
secrets (bearer / apikey-header / apikey-query / basic / json-body); the
secret is scrubbed from the response before return. Grant-gated on
`ext:<service>:<operation>`. Spec 5/13.

Per-turn agent output flows back over the same socket via the
`submit_turn` JSON-RPC method (hidden from `tools/list`);
`submit_status` delivers mid-turn progress notices. Stdout from the
container is discarded.

Read-only introspection: `inspect_messages`, `inspect_routing`,
`inspect_tasks`, `inspect_session`, `inspect_identity`, `find_messages`.
An elevated `/root` turn sees every folder; an ordinary turn is scoped to
its own folder subtree.

`find_messages` is ranked search over routd's in-memory BM25 index
(specs 5/C, 6/42 §8): terms that must all appear, `"exact phrase"`,
`-exclude`; no OR, wildcard or NEAR. Optional `scope` (chat_jid or folder
subtree), `sender`, `since` (RFC3339), `limit` (default 20, max 200). A
search made while the index is still warming is refused, not answered
empty. ACL gate is post-fetch `JIDRoutedToFolder` per row.

## Public API

- `ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, isRoot bool, expectedUID int, callerSub string, postBuild ...func(*server.MCPServer)) (stop func(), err error)`
  — `expectedUID` is the kernel-attested uid required on every accept
  (1000 = the ant image's `node` user in prod; ≤0 disables the check for
  tests). `isRoot` marks an elevated `/root` turn, where the socket
  authorizes every call. `callerSub` is the agent's auth subject, stamped
  into audit rows as the actor. `postBuild` are routd's resreg seams that
  mount the cold-tier resource tools onto the server. Returns a stop func
  that tears down the listener.
- `GatedFns` — callbacks into the host daemon, routd (enqueue, register
  channel, run container, social verbs).
- `StoreFns` — callbacks into store (typed subset, not the full `*store.Store`)
- `TurnResult`, `ModelUsage` — turn submission payloads
- `RouteTokenInfo` — route token metadata
- `PlatformHistory`, `ErroredChat`, `TaskRunLog` — DTO types

## Dependencies

- `audit`, `auth`, `chanlib`, `core`, `groupfolder`, `mountsec`, `obs`,
  `resreg`, `router`, `mark3labs/mcp-go`

## Files

- `ipc.go` — server wiring, action tools, `SO_PEERCRED` accept gate,
  social verbs, routing/groups, tasks, invites, tokens, network
- `inspect.go` — read-only introspection (`inspect_routing`,
  `inspect_tasks`, `inspect_session`, `inspect_identity`,
  `inspect_messages`, `find_messages`)
- `connector.go` — the per-folder MCP HTTP connector catalog (spec 5/13)
- `extcall.go` — the call into one connector's tools
- `SECURITY.md` — threat model (peer UID, path resolution, grants)

## Related docs

- `ARCHITECTURE.md` (IPC section)
- `specs/5/3-inspect-tools.md`
- `specs/5/17-openapi-mcp.md` — the auth model: one evaluator, two identity
  sources, the gate injected per surface
- `specs/5/13-ext-mcp.md` — MCP-subprocess tool catalog
- `../auth/README.md` — `Authorize` / identity resolution
- `../routd/README.md` — host process; routd-owned tables stay local
- `../SECURITY.md` — root threat model
