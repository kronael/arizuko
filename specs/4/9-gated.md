# gated

Gateway daemon. Message loop, route resolution, job queue,
container runner, HTTP API, session management.

## Responsibilities

- **HTTP API**: channel registration, inbound messages, admin
- **Message loop**: polls `messages` for unprocessed rows
- **Route resolution**: JID → group via `routes` table
- **Job queue**: per-group serialization, concurrency cap (in-memory, queue/ package)
- **Container runner**: docker-in-docker lifecycle (spawn,
  stream output, collect results, cleanup)
- **Session management**: resume previous session, evict on
  error, track in `sessions` / `session_log`

## Tables owned

| Table               | Purpose                              |
| ------------------- | ------------------------------------ |
| `routes`            | JID prefix → group mapping           |
| `registered_groups` | known groups and their config        |
| `router_state`      | daemon state (last poll cursor)      |
| `sessions`          | active agent sessions per group      |
| `session_log`       | session history (start, end, reason) |
| `system_messages`   | system-generated messages            |

Shared tables (read/write): `messages`, `chats`.
Migration service name: `gated`.

## Message loop

```
every 1s:
  SELECT * FROM messages WHERE processed = 0 ORDER BY created_at

  for each message:
    route = resolve_route(message.chat_jid)
    if no route:
      if ONBOARDING_ENABLED: insert into onboarding table, skip
      else: drop message
    enqueue_job(route.group, message)
    UPDATE messages SET processed = 1
```

## Route resolution

Routes map JID prefixes to groups:

```sql
SELECT folder FROM routes
  WHERE ? LIKE jid_prefix || '%'
  ORDER BY length(jid_prefix) DESC
  LIMIT 1
```

Longest prefix match wins. A route for `telegram:-1001234`
beats `telegram:`.

Route type `command` matches `text == match || text.startsWith(match + " ")`.
Route type `prefix` matches `text.startsWith(match)` (no space required).

### Route targets

- **Group folder** (contains `/`): write message to messages table, enqueue for container.
- **Service name** (no `/`): look up URL in channels table, POST message to channel's `/send` endpoint.

Service routing enables commands like `/approve` and `/reject` to be plain
prefix routes pointing to `onbod`, resolved identically to group routes.
No special gateway code required.

### Template routing

Route targets support RFC 6570 Level 1 template expansion.
A target containing `{sender}` expands per-message to
`<base>/<sender-file-id>` via `router.SenderToUserFileID`.

```
seq=0  type=default  target=atlas/{sender}
seq=1  type=default  target=atlas/support
```

Non-existent targets are auto-created from the hub's
`prototype/` directory. If creation fails (max_children,
no prototype dir, mkdir error), fall through to next route.

```
atlas/              routing hub
  prototype/        seed files for auto-created children
  support/          fallback group (tier 2)
  tg-123456/        per-user, auto-created (tier 2)
  wa-5551234/       per-user, auto-created (tier 2)
```

All children are siblings at the same tier. No sibling
visibility. `max_children` on the hub caps total
auto-created groups (default 50).

Template variables: `{sender}` only for now. Future:
`{platform}`, `{chat}`.

Folder names use sender IDs directly — `@`, `.` etc
are valid Unix filenames. `SenderToUserFileID` converts
`telegram:123` to `tg-123`, `whatsapp:5551234` to
`wa-5551234`.

Implementation: `router.SenderToUserFileID` in
`router/router.go`. Auto-creation via `groupfolder`
package.

### Predefined routes

On group registration (tiers 0-2), gateway inserts:

- `seq=-2, type=prefix, match=@` — delegate to named child
- `seq=-1, type=prefix, match=#` — topic-scoped session

See `specs/7/23-topic-routing.md`.

## Job queue

Per-group serialization with global concurrency cap:

- Each group processes one job at a time (serial)
- Global cap: `MAX_CONCURRENT_CONTAINERS` (default 5)
- Queue overflow: jobs wait in memory (pending queue)
- Circuit breaker: consecutive failures pause the group

States: `queued` → `running` → `done` | `failed`.

## Container runner

Spawns agent containers via docker-in-docker:

1. Build docker run command (image, volumes, security flags)
2. Pipe prompt + secrets via stdin
3. Stream stdout (JSONL results)
4. Collect output between sentinel markers
5. Write response to `messages`
6. Cleanup container

Security flags: `--cap-drop ALL`,
`--security-opt no-new-privileges`, `--memory 1g`, `--cpus 2`.

## HTTP API

Runs on `API_PORT` (default 8080).

| Endpoint                  | Method | Purpose                   |
| ------------------------- | ------ | ------------------------- |
| `/v1/channels/register`   | POST   | channel registration      |
| `/v1/channels/deregister` | POST   | channel deregistration    |
| `/v1/channels`            | GET    | list registered channels  |
| `/v1/messages`            | POST   | inbound message           |
| `/v1/chats`               | POST   | chat metadata             |
| `/v1/outbound`            | POST   | send outbound via channel |
| `/health`                 | GET    | daemon health check       |

## Session management

- Sessions persist across messages within a group
- Session ID stored in `sessions` table
- On error: evict session, start fresh
- Session state lives in container volume
  (`/srv/data/.../data/sessions/<folder>/.claude/`)

## MCP tool handling

ipc handles all MCP tools directly and calls gateway
functions as callbacks. gated exposes these callbacks to
ipc at server creation time:

- Messaging: `send_message`, `send_file`, `inject_message`
- Groups: `register_group`, `delegate_group`, `escalate_group`
- Sessions: `reset_session`
- Routing: `get_routes`, `set_routes`, `add_route`, `delete_route`

ipc resolves identity and calls auth.Authorize before
invoking the callback. gated does not see raw MCP requests.

## Gateway commands

Intercepted before agent dispatch. Text-command model for
channel consistency (some channels have native commands,
arizuko uses text interception).

| Command   | Effect                                                         |
| --------- | -------------------------------------------------------------- |
| `/new`    | Clear session, enqueue trailing args as message                |
| `/ping`   | Reply with group, session, active containers                   |
| `/chatid` | Reply with the chat JID                                        |
| `/stop`   | Kill active container for this chat                            |
| `/status` | Show gateway state, channels, containers (TBD: route to dashd) |

Commands are gateway code, not agent tools. The command
registry is not exported to agents. `/file` commands
(put/get/list) are deferred — agents handle files via
MCP tools instead.

Service routes (not gateway code): `/approve` → `onbod`,
`/reject` → `onbod`. These are prefix routes in the routing
table resolved via channels table. `/grant` is an MCP tool in ipc.

Implementation: `gateway/commands.go`.

## Notifications

gated imports `notify/` for container errors and channel health events.
See `specs/7/20-control-chat.md`.

## Agent output processing

Agent output is delimited by sentinel markers
(`---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---`).
Between markers, gated applies outbound filtering
(`router.FormatOutbound`) before sending to channels.

### Think blocks

`<think>...</think>` tags for agent internal reasoning.
Stripped before channel delivery. Supports nesting
(depth-tracked). Unclosed `<think>` hides everything
after it (safe default — agent stays silent). Empty
result after stripping = silent turn (no message sent).

Use case: group-chat agents that must sometimes stay
silent. Agent opens `<think>`, reasons, decides not to
respond, never closes — silence is the natural result.

### Status messages

`<status>text</status>` blocks for agent-initiated
progress updates. Extracted and sent as interim messages
before the final response. Multiple per turn OK.

`<status>` inside `<think>` is silently dropped (think
stripping runs first). Unclosed `<status>` tags treated
as literal text (not stripped).

Implementation: `router/router.go` (`StripThinkBlocks`,
`ExtractStatusBlocks`, `FormatOutbound`).
