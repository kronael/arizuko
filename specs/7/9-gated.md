# gated

Gateway daemon. Message loop, route resolution, job queue,
container runner, HTTP API, session management.

## Responsibilities

- **HTTP API**: channel registration, inbound messages, admin
- **Message loop**: polls `messages` for unprocessed rows
- **Route resolution**: JID → group via `routes` table
- **Job queue**: per-group serialization, concurrency cap
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
| `jobs`              | active/queued container jobs         |

Shared tables (read/write): `messages`, `chats`.
Migration service name: `gated`.

## Message loop

```
every 1s:
  SELECT * FROM messages WHERE processed = 0 ORDER BY created_at

  for each message:
    route = resolve_route(message.chat_jid)
    if no route: skip (or create default route)
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

## Job queue

Per-group serialization with global concurrency cap:

- Each group processes one job at a time (serial)
- Global cap: `MAX_CONCURRENT_CONTAINERS` (default 5)
- Queue overflow: jobs wait in `jobs` table
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

| Endpoint                    | Method | Purpose              |
| --------------------------- | ------ | -------------------- |
| `/v1/channels/register`     | POST   | channel registration |
| `/v1/messages`              | POST   | inbound message      |
| `/v1/channels/:name/health` | GET    | channel health check |
| `/v1/groups`                | GET    | list groups          |
| `/v1/status`                | GET    | daemon status        |

## Session management

- Sessions persist across messages within a group
- Session ID stored in `sessions` table
- On error: evict session, start fresh
- Session state lives in container volume
  (`/srv/data/.../data/sessions/<folder>/.claude/`)

## MCP tool handling

gated is a consumer of MCP tools routed by actid.
When actid forwards a tool call, gated:

1. Receives the stamped request (caller folder + tier)
2. Calls authd to authorize
3. Executes if allowed, rejects if not

Tools consumed: `send_message`, `send_file`, `register_group`,
`reset_session`, `delegate_group`, `inject_message`, `escalate_group`,
`get_routes`, `set_routes`, `add_route`, `delete_route`.

## Channel health checks

Every 30s, gated pings registered channels:

```
GET http://channel-url/health
→ 200 {"ok": true}
```

Three consecutive failures → auto-deregister. Channel
re-registers on restart.

## Layout

```
cmd/arizuko/
  main.go          ← entrypoint (includes gated startup)
gateway/           ← message loop, commands
queue/             ← per-group concurrency, circuit breaker
container/         ← docker runner
api/               ← HTTP API server
chanreg/           ← channel registry, health checks
router/            ← message formatting, routing rules
```
