# gated

Gateway daemon: HTTP API, message loop, container orchestration, MCP host.

## Purpose

Owns `messages.db` (runs all migrations), runs the poll loop that routes
inbound messages to per-group agent containers, hosts the channel
registration API that adapters register against, hosts the per-group
MCP unix sockets for in-container agents (subsystem documented in
`../ipc/README.md`), and is the home daemon for the largest slice of
the federated platform API.

## Tables owned

Per `specs/6/7-platform-api.md` §"Daemon ownership", gated owns and is
the sole writer for:

- `groups` — folder tree, group metadata
- `routes` — channel↔group routing rules
- `sessions` — agent session state per group
- `channels` — registered channel adapter rows
- `messages` — inbound + outbound platform messages
- `grants` — authorization rules (rule evaluator in `core/grants.go`,
  called only at token issuance sites — gated, proxyd, onbod)

Schema lives in `store/migrations/`; gated runs them on startup. **No
other daemon migrates.** Other daemons may still hold a read connection
to the shared SQLite today, but post-`/v1/*` cutover all writes to
gated-owned tables go through gated's HTTP surface.

## Responsibilities

- Run SQL migrations (`store/migrations/`) on startup — no other daemon migrates.
- Serve HTTP API for channel adapters: `/v1/channels/register`, `/v1/messages`, `/v1/outbound` (`api/api.go`).
- Poll messages, resolve groups, enqueue container runs (`gateway/gateway.go`).
- Spawn and supervise agent containers (`container/runner.go`).
- Host the per-group MCP socket subsystem (`../ipc/`) and stream agent output back.
- Run adapter health loop; auto-deregister after 3 failures (`chanreg/health.go`).

## Federated `/v1/*` surface (planned)

Per `specs/6/7-platform-api.md` Phase 2. Gated will mount REST handlers
for each owned resource:

| Path           | Verbs                    | Notes                                                    |
| -------------- | ------------------------ | -------------------------------------------------------- |
| `/v1/groups`   | GET, POST, PATCH, DELETE | folder tree                                              |
| `/v1/routes`   | GET, POST, PATCH, DELETE | replaces ad-hoc `set_routes`/`add_route` MCP tools       |
| `/v1/sessions` | GET, POST `/{id}:reset`  | session lifecycle (action verb is Google-style)          |
| `/v1/channels` | GET, PATCH, DELETE       | adapter registry (POST stays on `/v1/channels/register`) |
| `/v1/messages` | GET, POST                | `POST` IS "send"; `GET` replaces `inspect_messages`      |
| `/v1/grants`   | GET, POST, PATCH, DELETE |                                                          |

Every endpoint validates an `auth.VerifyHTTP(r)` token, then checks
`auth.HasScope(ident, resource, verb)` and `auth.MatchesFolder(ident,
target)`. None shipped yet — today these tables are reached via the
MCP surface in `../ipc/` and direct DB reads from sibling daemons.

## Entry points

- Binary: `gated/main.go`
- Listen: `$API_PORT` (default `8080`)
- Config: `core.LoadConfig` reads `.env` in cwd + env vars

## Dependencies

- `core`, `store`, `gateway`, `api`, `chanreg`

## Configuration

See CLAUDE.md for the full env table. Key vars: `API_PORT`,
`CHANNEL_SECRET`, `CONTAINER_IMAGE`, `CONTAINER_TIMEOUT`, `IDLE_TIMEOUT`,
`MAX_CONCURRENT_CONTAINERS`, `HOST_DATA_DIR`, `HOST_APP_DIR`.

## Health signal

`GET /health` returns 200 when the API server is up and DB is reachable.
Agent liveness is observed via journalctl (`"container run complete"`
events) and `docker ps` for running `arizuko-*` containers.

## Files

- `main.go` — wiring: store, gateway, channel registry, API server, health loop.

Logic lives in imported packages (`gateway/`, `api/`, `container/`, `store/`).

## Related docs

- `ARCHITECTURE.md` — message flow, container lifecycle
- `ROUTING.md` — route resolution rules
- `../gateway/README.md`, `../store/README.md`, `../container/README.md`
- `../ipc/README.md` — MCP host subsystem (lives inside gated's process)
- `specs/6/7-platform-api.md` — federated `/v1/*` contract gated implements
