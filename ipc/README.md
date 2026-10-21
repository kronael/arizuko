# ipc

The MCP host: the guest's only way to the host. It runs inside the `routd`
process and serves one socket per turn. The figure shows what one frame
passes through and which tools the socket registers.

<!-- twin:mcp -->

```
┌─────────────────────────────────────────────────────────────────────────┐
│ ipc/<folder>/gated.sock, bound by routd before runed boots the guest;   │
│ ServeMCP takes the folder as a parameter, never from the path           │
└────────────────────────────────────┬────────────────────────────────────┘
                                     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ accept: at most 8 connections; SO_PEERCRED uid must equal expectedUID   │
└────────────────────────────────────┬────────────────────────────────────┘
                                     ▼ one JSON-RPC frame per line
┌───────────────────┬───────────────────┬─────────────────────────────────┐
│ submit_turn       │ submit_status     │ tools/call                      │
│ not in tools/list │ not in tools/list │ hold gate (CheckHold), then     │
│ records the turn  │ updates the       │ an mcp_tool span, then the      │
│ result once       │ turn's progress   │ registered handler              │
│                   │ row               │                                 │
└───────────────────┴───────────────────┴────────────────┬────────────────┘
                                                         ▼
┌────────────────────┬────────────────────────┬───────────────────────────┐
│ tool source        │ registered at bind if  │ checked on each call      │
├────────────────────┼────────────────────────┼───────────────────────────┤
│ hot-tier verbs:    │ the turn holds         │ mcp:<tool>, then JID      │
│ reply, send,       │ mcp:<tool>             │ containment for a chat    │
│ engage, ...        │                        │ verb                      │
├────────────────────┼────────────────────────┼───────────────────────────┤
│ reads: inspect_*,  │ always                 │ no grant; the handler     │
│ find_messages,     │                        │ binds rows to the folder  │
│ get_work, set_work │                        │ subtree                   │
├────────────────────┼────────────────────────┼───────────────────────────┤
│ cold tier: resreg  │ the turn holds the     │ AuthorizeTurn on the      │
│ via postBuild      │ tool's mcp: action     │ target the handler        │
│                    │                        │ resolves                  │
├────────────────────┼────────────────────────┼───────────────────────────┤
│ mcp connector:     │ the folder's secret    │ mcp:<name>; secrets       │
│ <name>_<tool>      │ reads the catalog, and │ resolved per call and     │
│                    │ the turn holds         │ scrubbed from the result  │
│                    │ mcp:<name>             │                           │
├────────────────────┼────────────────────────┼───────────────────────────┤
│ rest connector:    │ the turn holds the     │ mcp:<name>; secrets       │
│ <name>_<tool>      │ tool's ext: scope      │ resolved per call and     │
│                    │                        │ scrubbed                  │
└────────────────────┴────────────────────────┴───────────────────────────┘
 a call check is AuthorizeTurn(sender, turn folder, target, action, params);
 "holds" at bind is EffectiveActions over the same acl rows; an elevated
 /root turn sees every tool and passes AuthorizeTurn and JID containment
```

<!-- /twin -->

## The turn's credential is the socket

There is no minted agent token. routd binds one socket per turn at
`ipc/<folder>/gated.sock`, sets mode 0660, chowns it to the spawning uid,
and checks every accept against the kernel-attested peer uid
(`SO_PEERCRED`). A peer with a different uid gets no connection.

The folder is a parameter of `ipc.ServeMCP`. routd passes the turn's folder
(`routd/mcp.go` `ServeTurnMCP`), and ipc never parses the folder from the
socket path. The path is a deployment detail; the identity is what the host
chose for the turn. Nothing the agent sends changes it.

Inside the guest, `guest-init` starts `socat`, which bridges a guest-local
unix socket to vsock. The agent's `settings.json` names the `arizuko` MCP
server with `socat UNIX-CONNECT`. A tool call crosses the vsock to the
bridge runed starts per turn as uid 1000, and the bridge dials routd's
socket. routd binds the socket before runed boots the guest and removes it
when the run returns. runed never mounts it (`runed/README.md` § The peer
credential).

## submit_turn ends the turn

`submit_turn`, `check_turn` and `submit_status` are JSON-RPC methods on the
same socket. `tools/list` does not show them, they have no REST twin, and they
are not resreg resources. They use the same bound socket and peer checks as
tool calls. The guest's stdout is discarded.

- **One result per turn.** `turn_id` is required. routd records the first
  call per folder and `turn_id` (`routd/turns.go` `recordTurnResult`); a
  later call answers `ok` and changes nothing. The first terminal signal
  takes the cursor and the delivery.
- **Prose is never delivered.** `submit_turn` carries the outcome and the
  session id, not assistant prose. Only the delivery tools (`reply`, `send`,
  `send_file`, `send_voice`) send an answer to the user.
- **One nudge.** The response is `{ok:true,nudge?:string}`. A turn that owes a
  delivery and made none gets a `nudge` once, and the turn continues. The
  hidden `check_turn` method takes `turn_id` and returns
  `{delivered,owed,nudge}` to a Stop hook. Both readers share the same
  persisted nudge count.
- **What the user never sees.** `router.FormatOutbound` strips `<internal>`,
  `<think>` and `<status>` blocks, and drops a whole message that only
  announces silence.
- **Why a method and not stdout.** A method call makes silence explicit. A
  marker on stdout cannot tell "no output" from "the agent crashed".

`submit_status` carries the task summary and the mid-turn progress text to the
turn's progress rows and does not end the turn.

## Who may call what

Two checks read the same `acl` rows, so the listing and the gate cannot
disagree. `tools/list` shows a tool when `auth.EffectiveActions` finds the
turn holds `mcp:<tool>` at any scope. Each call then runs
`db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)`, the
gate routd injects. There is no second check inside a handler.

A chat verb (`send`, `reply`, `post`, `like` and the rest) passes a second
gate, JID containment (`ipc/ipc.go` `authorizeJID`):

- The target chat resolves to a folder: its route row, else the folder its
  route token was minted for, else the folder in a `web:` JID.
- The call passes when that folder is the turn's folder or a descendant, or
  when the turn's folder is a route target for the JID under any verb.
- A JID with no route is denied ("has no route in this instance").
  Agents once sent to chats they remembered from past
  conversations and spammed chats that no route gave them. The route table,
  not the agent's memory, decides reach. Cross-folder work goes through
  `delegate_group` or `escalate_group`, which leave an auditable hop.
- An elevated `/root` turn passes this gate for every JID.
- The gate runs at the verb, not at the adapter, so the agent gets a denial
  it can read and correct in the same turn. A check at the adapter would
  drop the message silently.

A denial writes an `authz_denied` audit row. The hold gate (`CheckHold`)
runs before both checks, and only for a tool the socket registered and the
turn can see, so a raw caller cannot mint approval rows for tools it could
never run.

## Tool surface

Social verbs, served through `chanreg`: `send`, `reply`, `post`, `like`,
`dislike`, `delete`, `forward`, `quote`, `repost`, `edit`, `pin_message`,
`unpin_message`, `unpin_all`, `send_file`, `send_voice`. The platform
decides whose message an agent may edit or delete. An adapter without the
primitive returns a typed `chanlib.UnsupportedError` whose hint names an
alternative, for example `like(emoji='👎')` for `dislike`.

`send` and `send_file` are two tools over one `internalSend`, so their
persistence and routing cannot diverge. A failed send returns the error to
the agent and still records the undelivered row, `pending` or `failed`
(`routd/README.md` § The turn in routd).

Groups: `register_group`, `escalate_group`, `delegate_group`,
`inject_message`, `fork_topic`, `reset_session`, `observe_group`,
`unobserve_group`, `set_observe_window`, `set_group_open`, `engage`,
`disengage`. `role:member` includes `disengage`; a final answer cannot
reopen a window that the agent cleared during that turn.

Invites: `invite_create`, `invite_list`, `invite_revoke`. Route tokens:
`issue_chat_link`, `issue_webhook`, `list_tokens`, `revoke_token`. Slack
pane: `pane_set_prompts`, `pane_set_title`. Web: `get_web_presence`, a
presence report, not `web_routes` CRUD.

**Cold tier.** The `routes`, `web_routes`, `network_rules`,
`scheduled_tasks` and `acl` tools, and the rest of routd's resreg
resources, are not hand-written here. routd mounts each `resreg.Resource`
on this server through the `ServeMCP` `postBuild` seam, so one handler
serves `/v1/<name>` and the MCP tool. ipc keeps the transport, the hot-tier
tools and the shared visibility and gate closures.

`delete_route` and `set_routes` refuse to remove the caller's own seq-0
route (`routd/routes_resource.go` `isSelfDefault`). An agent that chased
502s once deleted its own default route, and its inbound fell into
onboarding.

**Reads.** `inspect_messages`, `inspect_routing`, `inspect_tasks`,
`inspect_session`, `inspect_identity`, `find_messages`, `get_thread`,
`fetch_history`, `get_work` and `set_work` register without a grant. The
handler binds every row and every `jid` argument to the turn's folder
subtree; an elevated `/root` turn sees every folder.

- **A tool family, not `Bash`.** `Bash` makes the agent know the instance
  name, the unit name and grep patterns, its output is unbounded, and a
  non-operator agent should not have it. An `inspect_*` tool returns shaped
  JSON with a clamped `limit`.
- **No `inspect_logs`, no `inspect_health`.** Both need journal or host
  access the guest does not have, and that access would defeat the guest
  boundary. A mutating operator verb belongs in its own family, never in
  `inspect_*`.
- **`find_messages`** is ranked search over routd's in-memory BM25 index:
  terms that must all appear, `"exact phrase"`, `-exclude`; no OR,
  wildcard or NEAR. Optional `scope` (a chat JID or a folder subtree),
  `sender`, `since` (RFC3339), `limit` (default 20, max 200). A search made
  while the index warms is refused, not answered empty. A hit survives only
  when `JIDRoutedToFolder` binds its chat to the turn's folder. Each call
  writes one `audit_log` row that stores the raw query; only the key-name
  redaction and the 200-rune value cap apply (`audit/README.md`). The index:
  `routd/README.md` § Ranked message search.

**Connectors.** Each `[[connector]]` block in `connectors.toml` adds tools
named `<connector>_<tool>`. An `mcp` connector reads its catalog per folder
with that folder's credential; a `rest` connector declares its tools. The
broker resolves the declared secrets per call and scrubs every resolved
value from the result. The block format and the grants:
`EXTENDING.md` § Adding an MCP connector, § Adding a REST connector.

## Designing a tool

The MCP description is the model's training material every turn. It is
read on every prompt, costs tokens, and shapes which tool the model
picks. The rules below follow from that.

**Descriptions answer "when", not just "what".** Every tool description
states _use when X_ and _not for Y_, naming the sibling tool that
covers Y. The model picks instantly instead of reasoning about
disambiguation at call time. See the `ipc.go` registrations
(`reply`, `like`, `reset_session`, `register_group`, …) for the
canonical pattern.

**No surrogates — `Unsupported(...)` with a hint.** When a verb has no
native primitive on a platform, the adapter returns
`chanlib.Unsupported(tool, platform, hint)` carrying a concrete
alternative tool the agent should call instead. Do not synthesize a
fake implementation by gluing other primitives together. The hint
travels through HTTP 501 → `*chanlib.UnsupportedError` →
`toolMaybeUnsupported` and is rendered to the agent as
`unsupported: <tool> on <platform>\nhint: <alternative>`.

**Distinct intents → distinct tool names.** Default to one tool per
intent. A sharp per-intent description outperforms a fuzzy umbrella
description with a `kind`/`mode`/`type` enum: the umbrella forces the
model to disambiguate at call time and dilutes signal in every other
tool's description by proximity.

Only collapse two names into one tool when the action is mechanically
identical AND the same description naturally covers both — e.g.
`reply` covers comment/reply because both create a threaded
response to a parent message. Do NOT collapse repost/forward/quote
into `share(kind=…)`; three intents, three tools.

Two tools may share one implementation in an adapter. Email's `forward` may
compile to `send` + `Fwd:` subject; Telegram's `forward` uses a native
protocol field. Both expose `forward` as a distinct MCP tool because
the agent's intent is the same ("show X this thing I saw"). The
adapter does the translation.

UNIX precedent: `cp` / `mv` / `ln` are three commands with three man
pages, not `relocate(kind=copy|move|link)`.

The autocall-vs-MCP-tool decision (`EXTENDING.md` § Adding an autocall) is the
same principle on a different axis: minimize the model's per-turn cost of choosing and
calling. Zero-arg pure-read facts → autocall. Distinct intents →
distinct tools.

## Cross-owner tools federate

A tool over routd's own tables is an in-process call. A tool over another
owner's tables is an HTTP call to that owner with the `service:routd`
bearer: `invite_create`, `invite_list` and `invite_revoke` go to onbod
through `routd/onbod.go` `httpOnbod`. ipc never opens a second owner's
store.

## Public API

- `ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, isRoot bool, expectedUID int, callerSub string, postBuild ...func(*server.MCPServer)) (stop func(), err error)`
  — `expectedUID` is the kernel-attested uid required on every accept
  (1000 = the ant image's `node` user in prod; ≤0 disables the check for
  tests). `isRoot` marks an elevated `/root` turn. `callerSub` is the
  socket's own principal, `folder:<path>`, stamped into audit rows as the
  actor; a non-empty value turns the row-based gate on. `postBuild` are
  routd's resreg seams that mount the cold-tier tools. Returns a stop func
  that tears down the listener and removes the socket.
- `GatedFns` — callbacks into routd (enqueue, register channel, social
  verbs, `SubmitTurn`, `SubmitStatus`).
- `StoreFns` — callbacks into the store (a typed subset, not the full
  `*store.Store`), plus `Authorize`, `Visible` and `CheckHold`.
- `TurnResult`, `ModelUsage` — the `submit_turn` payload.
- `RouteTokenInfo` — route token metadata.
- `PlatformHistory`, `ErroredChat`, `TaskRunLog` — DTO types.

## Dependencies

- `audit`, `auth`, `chanlib`, `core`, `groupfolder`, `mountsec`, `obs`,
  `resreg`, `router`, `mark3labs/mcp-go`

## Files

- `ipc.go` — server wiring, the accept gate, `submit_turn`, the hot-tier
  tools, `find_messages`, connector and REST-tool registration
- `inspect.go` — `inspect_routing`, `inspect_tasks`, `inspect_session`,
  `inspect_identity`
- `connector.go` — the per-folder MCP connector catalog and call
- `extcall.go` — the REST connector call
- `SECURITY.md` — threat model (peer uid, path resolution, grants)

## Related docs

- `ARCHITECTURE.md` § Cold tier vs hot tier — one evaluator, two identity
  sources, the gate injected per surface
- `../auth/README.md` — `Authorize` and identity resolution
- `../routd/README.md` — the host process; routd-owned tables stay local
- `../SECURITY.md` § Why a tool call beats a raw socket
