---
status: shipped
---

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

| Table             | Purpose                                      |
| ----------------- | -------------------------------------------- |
| `routes`          | message → target resolution (match → target) |
| `groups`          | known groups and their config                |
| `router_state`    | daemon state (last poll cursor)              |
| `sessions`        | active agent sessions per group              |
| `session_log`     | session history (start, end, reason)         |
| `system_messages` | system-generated messages                    |

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

### Steering a running container

If the target group already has a running container when new messages
arrive for the same chat, the poll loop steers them into the live turn
instead of respawning. On a successful `queue.SendMessages` the loop
advances the per-chat `agentCursor` alongside `SetLastReplyID` /
`ClearChatErrored`; without this advance, `drainGroupLocked` would see
the same rows as unprocessed after the container exits and respawn on
the same inputs (duplicate delivery). Success is logged at Info:
`"poll: steered messages into running container" count=N`.

Delivery into the container is hook-based: a PostToolUse hook drains
steered messages mid-loop between tool calls (primary path), and
`pollIpcDuringQuery`'s `stream.push` serves as a fallback for
text-only turns where no tool call runs before the turn ends
(next-turn delivery).

## Route resolution

Routes are a flat list of rows, each with a `match` expression and a
`target`. The gateway loads all rows once per poll and walks them in
`seq` order; the first row whose `match` evaluates true for the
current message wins.

```sql
SELECT id, seq, match, target, impulse_config
FROM routes
ORDER BY seq ASC
```

`match` is a space-separated list of `key=glob` pairs over message
fields (`platform`, `room`, `chat_jid`, `sender`, `verb`). All pairs
must match; empty `match` matches everything. Globs use Go
`path.Match`. See `specs/1/F-group-routing.md` for the full vocabulary.

### Route targets

- **Folder path** — plain path (e.g. `REDACTED/content`) or explicit
  `folder:REDACTED/content`. Gateway writes the message to the messages
  table and enqueues for the container.
- **`daemon:<name>`** — HTTP POST to a registered daemon's `/send`
  endpoint. Reserved for future use.
- **`builtin:<name>`** — in-gateway handler (future). Reserved.

`folder:` is optional; bare paths are folder targets. The prefix is
only required for `daemon:` / `builtin:` disambiguation.

### Template routing

Route targets support RFC 6570 Level 1 template expansion.
A target containing `{sender}` expands per-message to
`<base>/<sender-file-id>` via `router.SenderToUserFileID`.

```
seq=0  match=  target=atlas/{sender}
seq=1  match=  target=atlas/support
```

Non-existent targets are auto-created from the hub's
`prototype/` directory. If creation fails (max_children,
no prototype dir, mkdir error), fall through to next route.

```
atlas/              routing hub
  prototype/        seed files for auto-created children
  support/          fallback group (tier 2)
  tg-123456/        per-user, auto-created (tier 2)
  wa-REDACTED@lid/  per-user, auto-created (tier 2)
```

All children are siblings at the same tier. No sibling
visibility. `max_children` on the hub caps total
auto-created groups (default 50).

Template variables: `{sender}` only for now. Future:
`{platform}`, `{chat}`.

Folder names use sender IDs directly — `@`, `.` etc
are valid Unix filenames. `SenderToUserFileID` converts
`telegram:123` to `tg-123`, `whatsapp:REDACTED@lid` to
`wa-REDACTED@lid`.

Implementation: `router.SenderToUserFileID` in
`router/router.go`. Auto-creation via `groupfolder`
package.

### Predefined routes

On group registration the gateway inserts a single row:

```sql
INSERT OR IGNORE INTO routes (seq, match, target)
VALUES (0, 'room=<post-colon of jid>', '<folder>');
```

There are no predefined `@` / `#` prefix rows. Inline prefix
navigation is handled in code by the prefix layer (see
`specs/4/23-topic-routing.md`).

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
- **Idle > 2 days: evict session, start fresh.** At spawn time `gated`
  compares the per-chat agent cursor against `sessionIdleExpiry`
  (hard-coded 2 days). If the chat has been silent longer than the
  threshold, the stored session id for `(folder, topic)` is deleted
  before the container runs. Prevents MacroHype-class hallucinations
  from agents resuming multi-day-old state as if it were current.
- Session state lives in container volume
  (`/srv/data/.../groups/<folder>/.claude/`)

## MCP tool handling

ipc handles all MCP tools directly and calls gateway
functions as callbacks. gated exposes these callbacks to
ipc at server creation time:

- Messaging: `send`, `send_file`, `inject_message`
- Groups: `register_group`, `delegate_group`, `escalate_group`
- Sessions: `reset_session`
- Routing: `get_routes`, `set_routes`, `add_route`, `delete_route`

ipc resolves identity and calls auth.Authorize before
invoking the callback. gated does not see raw MCP requests.

## Gateway commands

Intercepted before agent dispatch by the command layer of the
pipeline. All commands live in a single Go registration table
(`gatewayCommands` in `gateway/commands.go`) so adding one is a
one-line addition.

| Command   | Effect                                          |
| --------- | ----------------------------------------------- |
| `/new`    | Clear session, enqueue trailing args as message |
| `/ping`   | Reply with group, session, active containers    |
| `/chatid` | Reply with the chat JID                         |
| `/stop`   | Kill active container for this chat             |
| `/status` | Show gateway state, channels, containers        |

Commands never touch the routes table. The command registry is not
exported to agents. `/grant` is an MCP tool in ipc.

Implementation: `gateway/commands.go`.

## Notifications

gated imports `notify/` for container errors and channel health events.
See `specs/3/b-control-chat.md`.

## Agent output processing

Agent output arrives via the `submit_turn` JSON-RPC method on the
gated MCP socket. The handler invokes the active per-folder
delivery callback, which applies outbound filtering
(`router.FormatOutbound`) before sending to channels. Idempotent
on `(folder, turn_id)`.

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
