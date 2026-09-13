# runed

Execution plane: the work queue + per-spawn container lifecycle.

## Purpose

Pure container-spawner. runed runs the agent work queue, starts one
Docker container per spawn and enforces run timeouts. It never appends a
message (routd is the sole appender) and never signs or brokers a token —
the turn is credentialed by the MCP unix socket, which `SO_PEERCRED` binds
to the spawned process. The per-turn agent MCP socket is hosted in-process by **routd**
(`Input.ExternalMCP=true`), so runed only mounts the ipc dir and does no
in-process `ServeMCP`. Spec: `specs/5/P-runed.md`.

## Responsibilities

- Serve `POST /v1/runs` (the routd↔runed contract): claim, spawn, return.
- Serve `POST /v1/holds`: claim a folder's run slot for an external,
  folder-exclusive job (a filesystem restore, a vacuum, a migration) so
  no agent turn can start in that folder until it is released.
- Per-spawn Docker lifecycle: `docker run --rm`, egress allowlist,
  steer via `docker kill --signal=SIGUSR1`, kill via stop→kill→`rm -f`.
- Enforce `RUNED_RUN_TIMEOUT` per run; hourly sweep of spawns older than the
  seven-day retention, which is what bounds the one partition they share.
- Graceful shutdown detaches in-flight runs (containers outlive the daemon).

## Entities owned

The `runed` owner, separate from routd's: `spawns`, `session_log`,
`circuit_breaker`, `audit_log` — runtime execution state with no home in routd.
These are runtime state, not manifest-addressable config, so `/openapi.json`
exposes only the `audit` read path (emitted also for aggregator uniformity).

There is no migration set. Each is an objstore ENTITY, declared beside the code
that reads it — `runed/runstore` for the first three, `audit` for the fourth —
and created by whichever driver the opener holds, so a second engine is a second
driver and no handler changes (spec `6/37` step 5). `runed` itself holds no SQL
statement at all; `db_utils/sql_layer_test.go` fails the build if one appears
anywhere under `runed/`, including `runstore`.

Each entity's file states its own COORDINATE and what that choice costs:
`spawns` takes one partition and the run id as its sort key, because six
operations address a spawn by run id with no folder in hand and three of them
are guarded transitions; `session_log` takes the folder and a time key, because
every read is "the n newest in this folder" and nothing sweeps it; and
`circuit_breaker` takes the folder as the whole coordinate.

## Entry points

- Binary: `runed/cmd/runed/main.go`
- Listen: `:8080` (`LISTEN_ADDR` default). Surface (`server.go`):
  - `POST /v1/runs` — start a run (scope `runs:run`)
  - `POST /v1/holds` — claim a folder's run slot, returns the handle
    immediately (`runs:run`); release with `DELETE /v1/runs/{run_id}`
  - `POST /v1/runs/stop` — operator kill by folder (`runs:kill`)
  - `GET /v1/runs/{run_id}` — status; `DELETE /v1/runs/{run_id}` — kill (`runs:kill`)
  - `GET /v1/spawn_sessions` — `session_log` history, one row per agent spawn
    (scope `sessions:read`, folder-bound). NOT `sessions` — that wire identity
    is authd's refresh-token families (BUGS `F46`).
  - `GET /v1/spawn_sessions/recent` — the n newest `session_log` rows, full-fielded
    (scope `sessions:read`, routd federation read)
  - `GET /v1/audit` — runed's own `audit_log` (scope `audit:read`, folder-bound;
    spec `5/I`). The only surface that answers "who killed that run" — a kill
    leaves a `spawns` row indistinguishable from a clean stop, and a busy hold
    leaves none. `/dash/audit/` federates it.
  - `GET|POST /dash/runed/` — runed's own operator page: active runs with a
    per-folder kill, and recent spawns. Gated by the proxyd-stamped operator
    identity, not a bearer (spec `6/30`).
  - `GET /openapi.json`, `GET /health`

## Dependencies

- `auth` (offline token verify via authd JWKS; `ServiceToken` bootstrap)
- `container` (docker runner), `core`, `groupfolder`, `obs`, `resreg`, `types`
- `authd` (JWKS), `routd` (hosts the per-turn agent MCP socket)

## Configuration

- `AUTHD_URL` — authd base URL, fetched once at boot for the JWKS. runed only
  VERIFIES — it holds no signing key and exchanges no token of its own, because
  nothing it does is an outbound call that needs one (spec `5/P` §Auth). Unset
  leaves both the `/v1` verifier and the `/dash/runed/` gate open, which is the
  local-dev shape; a fetch that fails is fatal
- `DATABASE` — runed's own DSN when the instance is not served: a file path or
  a `postgres://` URL. Ignored once `DYNAMODB_URL` names an endpoint
- `CRACKBOX_ADMIN_API` — set with no egress substrate selected is fatal at
  boot, rather than a spawn that quietly gets no isolation
- `RUNED_RUN_TIMEOUT` (default 20m) — the run ceiling: bounds BOTH the
  container hard-kill AND the in-container agent query timeout
  (`ARIZUKO_QUERY_TIMEOUT_MS` = `RUNED_RUN_TIMEOUT − 30s`), so the agent aborts
  and delivers a graceful summary before runed kills the container.
- `RUNED_SHUTDOWN_GRACE` (default = `RUNED_RUN_TIMEOUT`) — graceful shutdown wait for in-flight handlers
- `LISTEN_ADDR` (default `:8080`)
- Container config via `core.LoadConfig`: `MaxContainers`, dirs (GroupsDir, IpcDir, StoreDir, etc.)

## Health signal

`GET /health` returns 200 once the process is up. Red flag: spawns stuck
in `state=queued` (docker unavailable, or the concurrency cap is full).

## Observability

Metrics emitted when `METRICS_ENABLED=true`:

- `arizuko_container_spawns_total` — spawn attempts (folder, outcome)
- `arizuko_container_active` — running containers (gauge)
- `arizuko_container_duration_seconds` — container run time (folder, outcome)

plus the two HTTP metrics every daemon emits (`obs/README.md`). Spans:
`container_spawn`, `cross_daemon`.

### Audit trail

runed writes `audit_log` rows into its OWN tables; each daemon owns its own
audit trail and correlation across them is `turn_id` (spec `5/I`). Two actions, category `agent`, surface
`rest`:

- `run.hold` — `POST /v1/holds` claimed (or was refused) a folder's run slot
- `run.kill` — `DELETE /v1/runs/{run_id}` or `POST /v1/runs/stop`

Both name the caller (`actor` = the bearer's JWT sub, `actor_sub` = the
bare principal). **A turn dispatch writes no audit row**: the `spawns` row
already carries kind/state/outcome/exit_code/timings and dashd renders it,
so a row per turn would duplicate it at turn volume. What `spawns` never
records is who asked — and a busy hold, or a kill that found nothing live,
writes no `spawns` row at all. See `runed/audit.go`.

## Files

- `cmd/runed/main.go` — daemon wiring, verifier bootstrap, shutdown
- `server.go` — HTTP surface + bearer/scope gate
- `audit.go` — the run-slot audit renderer (what runed records, and why not turns)
- `manager.go` — Manager: run lifecycle, concurrency, queue, circuit breaker
- `runtime.go` — the `Runtime` interface (`Run`, `Kill`) and `RunSpec`
- `runtimes.go` — which substrate a spawn gets
- `docker.go` — `dockerRuntime`: spawn / steer / kill. Mounts the ipc dir
  routd bound the turn's socket in; hosts no MCP of its own
- `hold.go` — `POST /v1/holds`, the folder run-slot claim
- `dash.go` — `/dash/runed/` and its kill form
- `db.go` — spawn/session persistence, and the boot that dials the endpoint
  or opens the file
- `runstore/` — the three entities and their coordinates, no SQL
