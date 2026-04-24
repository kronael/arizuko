# gated

Gateway daemon: HTTP API, message loop, container orchestration.

## Purpose

Owns `messages.db` (runs all migrations), runs the poll loop that routes
inbound messages to per-group agent containers, hosts the channel
registration API that adapters register against, and manages per-group
IPC sockets for the in-container MCP server.

## Responsibilities

- Run SQL migrations (`store/migrations/`) on startup — no other daemon migrates.
- Serve HTTP API for channel adapters: `/v1/channels/register`, `/v1/messages`, `/v1/outbound` (`api/api.go`).
- Poll messages, resolve groups, enqueue container runs (`gateway/gateway.go`).
- Spawn and supervise agent containers (`container/runner.go`).
- Maintain the per-group MCP socket and stream agent output back (`ipc/ipc.go`).
- Run adapter health loop; auto-deregister after 3 failures (`chanreg/health.go`).

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
