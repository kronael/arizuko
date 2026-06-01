# runed

Execution plane: the work queue + per-spawn container lifecycle.

## Purpose

Pure container-spawner. runed runs the agent work queue, starts one
Docker container per spawn, enforces run timeouts, and brokers one
downscoped capability token per spawn. It never appends a message
(routd is the sole appender) and never signs a token (authd is the sole
signer). The per-turn agent MCP socket is hosted in-process by **routd**
(`Input.ExternalMCP=true`), so runed only mounts the ipc dir and does no
in-process `ServeMCP`. Spec: `specs/5/P`.

## Responsibilities

- Serve `POST /v1/runs` (the routd↔runed contract): claim, spawn, return.
- Per-spawn Docker lifecycle: `docker run --rm`, egress allowlist,
  steer via `docker kill --signal=SIGUSR1`, kill via stop→kill→`rm -f`.
- Enforce `RUNED_RUN_TIMEOUT` per run; hourly GC of expired spawns/tokens.
- Broker a downscoped per-spawn token from authd (parent = runed's own
  `service:runed` token); runed mints nothing.
- Graceful shutdown detaches in-flight runs (containers outlive the daemon).

## Tables owned

`runed.db` (separate from gated's `messages.db`): `spawns`, `session_log`,
`spawn_logs`, `mcp_tokens` — runtime execution state with no home in
routd. Migrations in `runed/migrations/`. These are runtime tables, not
manifest-addressable config, so `/openapi.json` exposes zero resource
paths (emitted only for aggregator uniformity).

## Entry points

- Binary: `runed/cmd/runed/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface (`server.go`):
  - `POST /v1/runs` — start a run (scope `runs:run`)
  - `GET /v1/runs/{run_id}` — status; `DELETE /v1/runs/{run_id}` — kill (`runs:kill`)
  - `GET /v1/sessions` — session history (scope `sessions:read`, folder-bound)
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (offline token verify via authd JWKS; `ServiceToken` bootstrap)
- `container` (docker runner), `core`, `groupfolder`, `obs`, `resreg`, `types`
- `authd` (token broker + JWKS), `routd` (forwards agent tools back to `/v1/turns/*`)

## Configuration

- `AUTHD_URL` — authd base URL; unset → verify open + static broker (local-dev)
- `AUTHD_SERVICE_KEY` — exchanged for an auto-refreshing `service:runed`
  token; falls back to static `RUNED_SERVICE_TOKEN`
- `RUNED_RUN_TIMEOUT` (default 20m), `RUNED_SHUTDOWN_GRACE`
- `LISTEN_ADDR`; container config via `core.LoadConfig` (`MaxContainers`, dirs)

## Health signal

`GET /health` returns 200 once the process is up. Red flag: spawns stuck
in `state=queued` (broker or docker unavailable).

## Files

- `cmd/runed/main.go` — daemon wiring, verifier/broker bootstrap, shutdown
- `server.go` — HTTP surface + bearer/scope gate
- `manager.go`, `runtime.go`, `runtimes.go` — run lifecycle + concurrency
- `docker.go` — `dockerRuntime`: spawn / steer / kill (ExternalMCP)
- `broker.go` — per-spawn token brokering from authd
- `db.go` — `runed.db` spawn/session/token persistence

## Status

Built and tested in-tree, NOT yet deployed (behind `CUTOVER_SPLIT`);
`gated` is the live monolith. Spec: `specs/5/P`.
