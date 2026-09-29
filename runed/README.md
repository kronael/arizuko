# runed

Execution plane: the work queue + per-spawn guest lifecycle.

## Purpose

Pure turn-spawner. runed runs the agent work queue, starts one KVM guest
per spawn and enforces run timeouts. It never appends a
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
- Per-turn KVM lifecycle: one `qemu-system-x86_64` guest, its virtio-fs shares,
  its tap and its egress proxy; steer as a ctl frame over vsock, kill by reaping
  every pid the turn's record names (spec `6/41`).
- Enforce `RUNED_RUN_TIMEOUT` per run; hourly sweep of spawns older than the
  seven-day retention, which is what bounds the one partition they share.
- Graceful shutdown detaches in-flight runs (a guest outlives the daemon, and the
  next runed reaps it from the record on disk).

## Entities owned

The `runed` owner, separate from routd's: `spawns`, `session_log`,
`circuit_breaker`, `audit_log` — runtime execution state with no home in routd.
These are runtime state, not manifest-addressable config, so `/openapi.json`
exposes only the `audit` read path (emitted also for aggregator uniformity).

There is no migration set. Each is an objstore ENTITY, declared beside the code
that reads it — `runed/runstore` for the first three, `audit` for the fourth —
and created by whichever driver the opener holds, so a second engine is a second
driver and no handler changes (spec `6/37` step 5). `runed` itself holds no SQL
statement at all, `runstore` included.

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
- `container` (mount rendering, app-src staging, the egress rules), `core`,
  `groupfolder`, `obs`, `resreg`, `types`
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
  guest hard-kill AND the in-guest agent query timeout
  (`ARIZUKO_QUERY_TIMEOUT_MS` = `RUNED_RUN_TIMEOUT − 30s`), so the agent aborts
  and delivers a graceful summary before runed kills the guest.
- `RUNED_SHUTDOWN_GRACE` (default = `RUNED_RUN_TIMEOUT`) — graceful shutdown wait for in-flight handlers
- `LISTEN_ADDR` (default `:8080`)
- Guest config via `core.LoadConfig`: `MaxContainers`, dirs (GroupsDir, IpcDir, StoreDir, etc.)

### The turn substrate

There is ONE substrate and no selector: one KVM guest per turn, for the whole
instance (spec `specs/6/41-qemu-substrate.md` §"The seam"). What it delivers is on
runed's startup `substrate` log line and on `/dash/runed/`.

It refuses to start unless every precondition below holds, and the refusal names
all of them at once, because an input found missing per turn is a fleet that
looks healthy and answers nothing:

- runed runs as root, and `/dev/kvm`, `/dev/vhost-vsock` and `/dev/net/tun` open
- `RUNED_QEMU` (default `qemu-system-x86_64`) runs `-version`
- `RUNED_KERNEL_DIR` holds `bzImage` and `initrd.gz` from `make kernel`
- `RUNED_ROOTFS_DIR` is the release rootfs `make images` exports once per release
- `RUNED_VIRTIOFSD` (default `/usr/libexec/virtiofsd`, where Alpine's Rust
  daemon lands) advertises `--uid-map` and `--gid-map`. Those flags are NOT sent:
  they are how the check tells the Rust build from QEMU's C one, which needs real
  root and is refused. **The clamp is the daemon's uid** — the writable share is
  exported by a child running as the agent's own uid, so it cannot create a host
  file owned by anyone else whatever uid the guest states. Without the clamp a
  guest as uid 0 writes host-root-owned files into the folder and they outlive the
  turn (measured 2026-09-24). An id map was the first design and cannot run here:
  it is legal only under `--sandbox namespace`, whose own `/proc` mount docker
  refuses (BUGS `L372`)
- `RUNED_EGRESS_BIN` (default `egress`), `RUNED_FIREWALL_BIN` (default
  `iptables`) and `RUNED_IP_BIN` (default `ip`) are executable
- `RUNED_GUEST_MEM` (default `2G`) × `MAX_CONCURRENT_CONTAINERS` fits the
  host's `MemTotal` — the guest size and the cap are one setting

Tunables: `RUNED_GUEST_SMP` (2), `RUNED_VSOCK_PORT_BASE` (5555),
`RUNED_TAP_SUBNET` (`10.97.0.0/24`), `RUNED_PROXY_PORT` (3128),
`RUNED_VM_DIR` (`<data>/vms`), `RUNED_BOOT_TIMEOUT` (30s),
`RUNED_REAP_GRACE` (5s).

## Health signal

`GET /health` returns 200 once the process is up. Red flag: spawns stuck
in `state=queued` (the concurrency cap is full, or a precondition the guest
needs went away under a running daemon).

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

## The turn envelope

The guest's stdin frame carries model credentials only: the operator anchors from the
host `.env` (`store.EnvProfileKeys` — `ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`, required for LLM
calls), overlaid with the triggering user's own rows for those same keys
(BYOA, `routd.DB.EnvProfileSecrets`). With a model gateway configured
(`MODEL_GATEWAY_URL`), `spawnSecrets` withholds those keys from both sources
and the guest holds only `MODEL_GATEWAY_KEY`, the gateway's virtual key
(spec `6/23`). Capability credentials — folder- or
user-scoped — are broker-resolved on the host at tool-call time and never
enter the guest (specs `5/13`, `5/14`). Agent model and query timeout
ride the process environment as `ARIZUKO_MODEL` and `ARIZUKO_QUERY_TIMEOUT_MS`
(read by ant's index.ts and claude.ts at module load) — `turnEnv` is the one
place that decides it, and a key it omits is silently dead in the guest.

### Mounts

`buildMounts` renders the table's right-hand side, in this order. Every turn:

- `groupDir` → `/home/node` (rw)
- `HOST_APP_DIR` → `/opt/arizuko` (ro)
- `ipc/` → `/run/ipc` (rw, for MCP socket)
- `groups/<world>/share/` → `/var/lib/share` (rw; ro if `share_mount` grant with `readonly=true`)

Elevated `/root` turn only:

- `GROUPS_DIR` → `/var/lib/groups` (rw)

With the `web:publish` grant:

- `WEB_DIR/pub/` → `/var/lib/www` (ro, whole tree)
- `WEB_DIR/pub/<folder>/` → `~/public_html` (rw)
- `WEB_DIR/priv/<folder>/` → `~/private_html` (rw)

Without it, one subtree of that tree still mounts — the platform doc site
the `arizuko` skill every folder ships reads from (spec `5/V`):

- `WEB_DIR/pub/arizuko/` → `/var/lib/www/arizuko` (ro)

When `HOST_CODEX_DIR` set:

- `groupDir/.codex/` → `~/.codex` (rw, per-group)
- `HOST_CODEX_DIR/auth.json` → `~/.codex/auth.json` (ro, overmount)
- `HOST_CODEX_DIR/config.toml` → `~/.codex/config.toml` (ro, overmount)

Additional mounts from the group's `container_config` are validated via
`mountsec`. The substrate adds the proxy variables (`HTTP_PROXY` and its three
companions) and the guest uid, because both are its own.

## Files

- `cmd/runed/main.go` — daemon wiring, verifier bootstrap, shutdown
- `server.go` — HTTP surface + bearer/scope gate
- `audit.go` — the run-slot audit renderer (what runed records, and why not turns)
- `manager.go` — Manager: run lifecycle, concurrency, queue, circuit breaker
- `runtime.go` — the port: the `Runtime` interface (`Run`, `Kill`) and the
  `Turn` it carries (spec 6/41 §"The seam")
- `render.go` — the ONE renderer: a wire request in, one `Turn` out. With
  `mounts.go` (the mount table), `settings.go` (`settings.json` + the output
  style) and `secrets.go` (what credential a guest receives)
- `runtimes.go` — `FakeRuntime`, the test seam
- `substrate.go` — `NewRuntime`: the one place the substrate is built, its
  preconditions checked and a previous runed's guests reaped. There is one
  substrate and no selector — the Docker runtime and `RUNED_SUBSTRATE` went at
  spec `6/41` step 4
- `qemu.go` — the KVM substrate: the argument list, the turn, the reap
- `qemuhost.go` — what this host can run: every precondition, checked once at
  start, including the uid clamp and guest memory against the concurrency cap
- `vmrecord.go` — the on-disk VM record (every pid the turn starts) and the
  disk scan that reaps what a dead runed left
- `vmtree.go` — the staging tree: one share, mirroring the guest's own paths
- `vmctl.go` — the `ctl` frame protocol, the vsock listeners, and the
  uid-1000 MCP bridge (`runed --mcp-bridge`)
- `vmnet.go` — the per-turn tap, its proxy-only rule set, and the `egress`
  process on the tap's host side
- `turnlog.go` — the per-turn log beside the group's files, secrets removed
- `hold.go` — `POST /v1/holds`, the folder run-slot claim
- `dash.go` — `/dash/runed/` and its kill form
- `db.go` — spawn/session persistence, and the boot that dials the endpoint
  or opens the file
- `runstore/` — the three entities and their coordinates, no SQL
