# runed

Execution plane: the work queue + per-spawn container lifecycle.

## Purpose

Pure container-spawner. runed runs the agent work queue, starts one
Docker container per spawn, enforces run timeouts, and brokers one
downscoped capability token per spawn. It never appends a message
(routd is the sole appender) and never signs a token (authd is the sole
signer). The per-turn agent MCP socket is hosted in-process by **routd**
(`Input.ExternalMCP=true`), so runed only mounts the ipc dir and does no
in-process `ServeMCP`. Spec: `specs/5/P-runed.md`.

## Responsibilities

- Serve `POST /v1/runs` (the routd↔runed contract): claim, spawn, return.
- Per-spawn Docker lifecycle: `docker run --rm`, egress allowlist,
  steer via `docker kill --signal=SIGUSR1`, kill via stop→kill→`rm -f`.
- Enforce `RUNED_RUN_TIMEOUT` per run; hourly GC of expired spawns/tokens.
- Broker a downscoped per-spawn token from authd (parent = runed's own
  `service:runed` token); runed mints nothing.
- Graceful shutdown detaches in-flight runs (containers outlive the daemon).

## Tables owned

`runed.db` (separate from routd's `routd.db`): `spawns`, `session_log`,
`spawn_logs`, `mcp_tokens`, `circuit_breaker` — runtime execution state
with no home in routd. Migrations in `runed/migrations/`. These are
runtime tables, not manifest-addressable config, so `/openapi.json`
exposes zero resource paths (emitted only for aggregator uniformity).

## Entry points

- Binary: `runed/cmd/runed/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface (`server.go`):
  - `POST /v1/runs` — start a run (scope `runs:run`)
  - `POST /v1/runs/stop` — operator kill by folder (`runs:kill`)
  - `GET /v1/runs/{run_id}` — status; `DELETE /v1/runs/{run_id}` — kill (`runs:kill`)
  - `GET /v1/sessions` — session history (scope `sessions:read`, folder-bound)
  - `GET /v1/sessions/recent` — recent session_log rows (scope `sessions:read`, routd federation read)
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (offline token verify via authd JWKS; `ServiceToken` bootstrap)
- `container` (docker runner), `core`, `groupfolder`, `obs`, `resreg`, `types`
- `authd` (token broker + JWKS), `routd` (hosts the per-turn agent MCP socket)

## Configuration

- `AUTHD_URL` — authd base URL; unset → verify open + static broker (local-dev)
- `AUTHD_SERVICE_KEY` — exchanged for an auto-refreshing `service:runed`
  token; falls back to static `RUNED_SERVICE_TOKEN`
- `RUNED_RUN_TIMEOUT` (default 20m) — the run ceiling: bounds BOTH the
  container hard-kill AND the in-container agent query timeout
  (`ARIZUKO_QUERY_TIMEOUT_MS` = `RUNED_RUN_TIMEOUT − 30s`), so the agent aborts
  and delivers a graceful summary before runed kills the container.
- `RUNED_SHUTDOWN_GRACE` (default = `RUNED_RUN_TIMEOUT`) — graceful shutdown wait for in-flight handlers
- `LISTEN_ADDR` (default `:8080`)
- Container config via `core.LoadConfig`: `MaxContainers`, dirs (GroupsDir, IpcDir, StoreDir, etc.)

## Health signal

`GET /health` returns 200 once the process is up. Red flag: spawns stuck
in `state=queued` (broker or docker unavailable).

## Observability

Metrics emitted when `METRICS_ENABLED=true`:

- `arizuko_container_spawns_total` — spawn attempts (folder, outcome)
- `arizuko_container_active` — running containers (gauge)
- `arizuko_container_duration_seconds` — container run time (folder, outcome)
- `arizuko_requests_total` — HTTP requests (daemon, method, status)

Spans: `container_spawn`, `cross_daemon`.
Spec: `specs/5/O-otlp-export.md`.

## Files

- `cmd/runed/main.go` — daemon wiring, verifier/broker bootstrap, shutdown
- `server.go` — HTTP surface + bearer/scope gate
- `manager.go` — Manager: run lifecycle, concurrency, queue, circuit breaker
- `runtime.go` — Runtime interface (Run, Kill); RunSpec
- `docker.go` — dockerRuntime: spawn / steer / kill (ExternalMCP)
- `broker.go` — per-spawn token brokering from authd
- `db.go` — runed.db spawn/session/token persistence

## Status

Live — the split is the only topology (gated removed, v0.50.0). Spec: `specs/5/P-runed.md`.
