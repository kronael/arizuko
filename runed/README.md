# runed

Execution plane: the work queue + per-spawn guest lifecycle.

## Purpose

Pure turn-spawner. runed admits a turn, starts one KVM guest for it and ends
the guest at the run deadline. It never appends a message, because routd is
the sole appender, and it never signs or brokers a token. The turn's
credential is the guest's vsock CID, which the host kernel stamps on every
packet. routd hosts the per-turn MCP socket in-process, and runed only
bridges the guest to that socket (§ The peer credential).

## Responsibilities

- Serve `POST /v1/runs` (the routd↔runed contract): claim, spawn, return.
- Serve `POST /v1/holds`: claim a folder's run slot for an external,
  folder-exclusive job (a filesystem restore, a vacuum, a migration) so
  no agent turn can start in that folder until it is released.
- Per-turn KVM lifecycle: one `qemu-system-x86_64` guest, its virtio-fs shares,
  its tap and its egress proxy; steer as a ctl frame over vsock, kill by reaping
  every pid the turn's record names (§ The guest's turn, step by step).
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
driver and no handler changes. `runed` itself holds no SQL
statement at all, `runstore` included.

Each entity's file states its own COORDINATE and what that choice costs:
`spawns` takes one partition and the run id as its sort key, because six
operations address a spawn by run id with no folder in hand and three of them
are guarded transitions; `session_log` takes the folder and a time key, because
every read is "the n newest in this folder" and nothing sweeps it; and
`circuit_breaker` takes the folder as the whole coordinate.

| Entity            | Coordinate                                                          | Attributes                                                                                                                                   |
| ----------------- | ------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `spawns`          | one constant partition, then the run id                             | folder, topic, container name, `session_log_id`, `session_id`, kind, state, outcome, exit code, steered, created, started and ended instants |
| `session_log`     | the folder, then the start instant at `core.TS` width plus a suffix | `session_id`, ended instant, result, error, message count                                                                                    |
| `circuit_breaker` | the folder, as both halves                                          | consecutive failures, the last failure; it survives a restart                                                                                |

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
  nothing it does is an outbound call that needs one. Unset
  leaves both the `/v1` verifier and the `/dash/runed/` gate open, which is the
  local-dev shape; a fetch that fails is fatal
- `DYNAMODB_URL` and runed's store key — the store server runed's entities
  live on (`runed.Open`, `INSTALL.md` § The store server)
- `RUNED_RUN_TIMEOUT` (default 20m) — the run ceiling: bounds BOTH the
  guest hard-kill AND the in-guest agent query timeout
  (`ARIZUKO_QUERY_TIMEOUT_MS` = `RUNED_RUN_TIMEOUT − 30s`), so the agent aborts
  and delivers a graceful summary before runed kills the guest.
- `RUNED_SHUTDOWN_GRACE` (default = `RUNED_RUN_TIMEOUT`) — graceful shutdown wait for in-flight handlers
- `LISTEN_ADDR` (default `:8080`)
- Guest config via `core.LoadConfig`: `MaxContainers`, dirs (GroupsDir, IpcDir, StoreDir, etc.)

### The turn substrate

There is ONE substrate and no selector: one KVM guest per turn, for the whole
instance (§ The port carries one rendered turn). What it delivers is on
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

The machine uses ACPI microvm with modern virtio-MMIO transports and a shared
memfd bound through `memory-backend=mem`. ACPI supplies device discovery and
poweroff. The kernel command line uses `loglevel=5`, which retains warnings
and errors. The pinned kernel uses LZ4 compression.

PID 1 is the static Go `guest-init` binary in the initrd. The release rootfs
supplies the Debian/Node userspace and agent tools. Each turn uses that rootfs
read-only with a temporary writable overlay.

### Boot timing measures the first ready frame

Production logs `guest ready` once per turn with `boot_ms` and the guest's probe
map. A monotonic host clock starts immediately before QEMU process start and
stops at the first decoded ready frame. This frame precedes root assembly and
agent startup. Guest uptime excludes QEMU startup and measures a different
interval.

`make bench-boot` compares q35 and microvm in alternating pairs through the
production argument renderer. It uses the release image's tools, an owned
internal network and test database, and private process and mount namespaces.
It checks boot poweroff, both visible pre-control failures, and normal, Kill,
and cancelled turns. The turn proof checks two CPUs, stdin, steering, MCP,
UID 1000 writes, read-only root, the final 1 MiB shared write, proxy policy,
blocked direct traffic, zero host-received guest DNS packets, and cleanup.

The untraced run on 2026-10-18 used source
`8649d24d0c257079112fd913eb1d00bc48de9f4a`, QEMU 9.0.2, virtiofsd 1.10.1,
two CPUs and 2 GiB per guest. This command produced all 40 samples:

```sh
env -u GOROOT PATH=/home/onvos/sdk/go1.27.2/bin:$PATH make bench-boot \
  DATA_DIR=/srv/data/arizuko_krons \
  KERNEL_DIR=/srv/data/arizuko_krons/images/kernel-6.1.112-lz4-5551dd2fac39 \
  ROOTFS_DIR=/srv/data/arizuko_krons/images/rootfs-90121c059c96 \
  BENCH_MODE=microvm BENCH_PAIRS=20 \
  BENCH_ARTIFACT_DIR=/home/onvos/app/arizuko/.qemu-startup/tmp/phase6-startup-20261018-pairs
```

| Machine                   | Samples | First ready p50 | First ready p90 |
| ------------------------- | ------- | --------------- | --------------- |
| q35                       | 20      | 1263 ms         | 1354 ms         |
| ACPI microvm, modern MMIO | 20      | 1085 ms         | 1157 ms         |

Independent calculations use the logged integer milliseconds and nearest-rank
p90. Microvm was faster in 19 of 20 pairs. The median paired saving was
206.5 ms. The ratio of medians gives a 14.09% reduction. All functional and
cleanup assertions passed without skips. Both owned containers and the owned
network were absent after the run.

Host load changed from 2.50/1.63/4.03 to 2.75/1.94/3.96 during the run.
CPU pressure `some avg10` changed from 6.67% to 18.15%. Memory pressure
`some avg10` changed from 0% to 0.09%, with full pressure at 0%.
I/O pressure `some avg10` stayed at 0%. These are shared-host measurements.

Three separate turn observations detected the probe agent's entrypoint marker
at 1300, 1200 and 1100 ms from host `Run`, for normal, Kill and cancel.
The host polls every 50 ms. These observations measure probe startup, not model
response latency. They do not form a latency distribution.

Retained raw samples, consoles, host pressure, independent calculations and
parent acceptance:
`/home/onvos/.claude/projects/-home-onvos-app-arizuko/ship/qemu-startup-proof-20261018/pairs`.
The initial native smoke evidence lives in the sibling `smoke` directory.
Source evidence:
[boot test](qemu_boot_test.go), [turn proof](qemu_turn_boot_test.go),
[argument checks](qemu_boot_profile_test.go), and [control tests](vmctl_test.go).
The real vsock loopback CID test does not prove isolation between two real VMs.
Live guest-observable smoke and abrupt runed-crash recovery remain separate
acceptance gaps.

## Health signal

`GET /health` returns 200 once the process is up. Red flag: spawns stuck
in `state=queued` (the concurrency cap is full, or a precondition the guest
needs went away under a running daemon).

## Observability

Metrics emitted when `METRICS_ENABLED=true`:

- `arizuko_container_spawns_total` — spawn attempts (folder, outcome)
- `arizuko_container_active` — running guests (gauge)
- `arizuko_container_duration_seconds` — guest run time (folder, outcome)

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

### The port carries one rendered turn

`renderTurn` (`render.go`) is the one renderer. A wire request goes in and one
`Turn` comes out: the mount table, the process environment, the stdin frame
and the egress allowlist. The substrate delivers those bytes and names and
reads no grant, persona or session id. A new per-turn fact therefore changes
the renderer and never the port. The port is `Runtime` (`runtime.go`), two
methods: `Run` and `Kill`. The substrate's own state, the CID, the tap and the
pids, hangs off `Turn.Handle`, so `Kill(handle)` finds all of it.
`Turn.RegisterSteer` is the one hook beside the two methods: the substrate
calls it once the guest is up, and hands back the steer closure.

The renderer also lands the per-turn files the mounts point at:
`.claude/settings.json` with the `arizuko` MCP server (`socat STDIO
UNIX-CONNECT:/run/ipc/gated.sock`, `settings.go`), the gateway caps file, and
the context layers prefixed onto the prompt: the pending-migration note, the
episodes, the newest 14 diary entries, `work.md`, the `<user>` pointer,
`PERSONA.md` and `SYSTEM.md` (`preparePayload`).

### Admission is claim or reject

`Manager.Run` (`manager.go`) answers one of three ways, and keeps no queue of
its own. A second queue inside runed would drift from routd's, which already
holds the batch.

| Folder state                         | Answer                                               | routd then                                                     |
| ------------------------------------ | ---------------------------------------------------- | -------------------------------------------------------------- |
| a live guest                         | the batch goes in as a steer frame; `steered` on ack | marks this turn done; the first run owns the batch             |
| a live spawn row, no ack             | `busy`                                               | re-feeds the batch later, no cursor advance, no breaker charge |
| cap `MAX_CONCURRENT_CONTAINERS` full | `busy`                                               | the same                                                       |
| idle, under the cap                  | a `spawns` row in `queued`, then the guest           | waits for the outcome                                          |

- runed is store-stateless. Every admission decision reads the `spawns` rows,
  under one mutex for the check-and-claim. An in-memory map would lie after a
  restart: a folder would stay wedged, or the cap would reset. At boot
  `ExpireOrphans` ends every row a dead runed left live, as `exited` with
  outcome `error`, never `killed` (`runstore/spawns.go`).
- `StartSpawn` takes the claim with a condition on `state='queued'`. A
  `DELETE` that lands between the claim and the launch wins, and the run
  answers `busy`. A `killed` row makes the folder read free, so a launch
  without this guard would put two guests on one mount.
- A new inbound clears a breaker streak at the threshold, so a retry after
  three failures does spawn. The run that reaches the threshold reports
  `breaker_open`.
- routd owns session lineage. It mints the id and sends it on the request.
  runed mints one only for a request that carries none, an isolated task, and
  `spawns.session_id` is an echo.
- Readiness is spawn success. There is no `/readyz` probe, because a probe on
  the spawn path adds a failure mode. The one deadline is armed in
  `Manager.spawn` from `RUNED_RUN_TIMEOUT`, lowered by the group's
  `container_config` timeout. The substrate keeps no timer of its own: the
  two-minute wrap-up warning is read off that deadline.

### The guest's turn, step by step

`qemuRuntime.spawn` (`qemu.go`) runs these steps in order. Every step appends
its pid to the on-disk VM record as it starts the process, so a runed that dies
at any step leaves a record naming what it started.

1. Allocate the CID, the turn's slot, its two vsock ports `ctl` and `mcp`, its
   tap and its `/30`, and write the record (`allocate`).
2. Build the staging tree and start two `virtiofsd`: `root` over the release
   rootfs through a read-only bind, and `turn` over the staging tree, run as
   the agent's uid (§ Mounts).
3. Bring up the tap and its rule set, then start this turn's `egress` proxy on
   the tap's host side (`vmnet.go`).
4. Bind both vsock listeners BEFORE QEMU starts. A bind error fails the turn,
   and runed never picks another port. A port a squatter already holds then
   fails with no guest in existence. A silent retry on another port would turn
   that denial into a guest talking to whoever answered.
5. Fork the MCP bridge as the agent's uid, holding the `mcp` listener.
6. Start QEMU: ACPI microvm, the kernel and initrd from `make kernel`, two
   `vhost-user-fs` devices, one `vhost-vsock` device with the CID, one
   `virtio-net` device on the tap, `-no-reboot`.
7. Accept `ctl` only from this guest's CID, then send the `turn` frame: the
   environment and the stdin payload.
8. `guest-init` (`kernel/guest-init/main.go`), PID 1 in the initrd, sends
   `ready`, assembles the root, starts `socat UNIX-LISTEN:/run/ipc/gated.sock`
   to `VSOCK-CONNECT:2:<mcp>`, and runs ant as uid 1000. ant's stderr comes
   back as `log` frames, and the guest's last frame is one `exit`.

The kernel console lands in `groups/<folder>/logs/container-<ts>.console.log`
beside the turn log `container-<ts>.log`.

### The peer credential

```
guest                                     │ host
ant ──► /run/ipc/gated.sock ──► socat ──► │ vsock port <mcp>
                                          ▼
                    MCP bridge: the agent's uid, accepts one CID
                                          │
                                          ▼
                    routd: ipc/<folder>/gated.sock, SO_PEERCRED check
```

The host assigns the guest's CID, and the host's vhost driver drops every
packet whose source is not that CID. A guest cannot claim another guest's CID.
The bridge accepts a connection only from its own turn's CID and dials one
path, that turn's folder socket. routd's check stays the `SO_PEERCRED` uid
check it runs on any socket peer. An agent in folder A would need B's CID or
A's bridge dialling B's socket to reach B's tools; the host kernel refuses the
first and the bridge holds one path. A unix socket does not cross virtio-fs,
so the guest's socket lives on a guest-local tmpfs, never on the share.

### Steer and stop

A follow-up for a folder with a live guest is a `steer` frame on `ctl`.
`guest-init` writes the batch under `/run/ipc/input`, signals the agent and
answers `steer_ack`. The closure returns true only on that ack. routd marks
the turn done on an accepted steer, so an unacked batch must answer false:
the guest tmpfs that held it is gone at power-off, and false makes routd
re-feed it.

The run deadline sends `stop`. `guest-init` sends the agent SIGTERM and
powers off after 10 seconds. runed waits `RUNED_REAP_GRACE` for the `exit`
frame, then SIGKILLs the QEMU pid and confirms it gone, because a guest
power-off does not prove QEMU exited. `DELETE /v1/runs/{run_id}` reaps the
same way, from the record on disk.

### How a turn ends

| How the guest ended                                          | Outcome routd reads                       |
| ------------------------------------------------------------ | ----------------------------------------- |
| `exit` frame, code 0                                         | `ok`; the reply already went out over MCP |
| `exit` frame, code 78 (`EX_CONFIG`, the agent's own verdict) | `error`, `terminal`                       |
| `exit` frame, any other code                                 | `error`, retryable                        |
| QEMU ended with no `exit` frame                              | `error`: no frame is no result            |
| the deadline                                                 | `error`, "Guest timed out"                |
| QEMU did not start; no renderer; a gateway and no key        | `error`, `terminal`                       |

runed decides terminal versus retryable, and the decision rides
`RunOutcome.Terminal`, whose zero value is retryable. routd skips the retry
and shows runed's error text. The raw exit code never leaves runed: routd
classifying runed's prose would be a second classification path on a format
string. `guest-init` reports its own failures as 71, never 78, so it cannot
impersonate the agent's verdict.

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
  `Turn` it carries
- `render.go` — the ONE renderer: a wire request in, one `Turn` out. With
  `mounts.go` (the mount table), `settings.go` (`settings.json` + the output
  style) and `secrets.go` (what credential a guest receives)
- `runtimes.go` — `FakeRuntime`, the test seam
- `qemu.go` — the KVM substrate, the only one: `NewQemuRuntime`
  builds it, checks its preconditions and reaps a previous runed's guests; then
  the argument list, the turn, the reap
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
