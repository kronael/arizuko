# runed

runed implements the execution plane of an arizuko instance, and routd calls it over REST to run each turn. It admits a turn from routd, runs the turn in one KVM guest and ends the guest at the run deadline.

## What runed does

runed never appends a message, because routd is the only appender. runed never signs or brokers a token. The credential of a turn is the vsock CID of its guest, which the host kernel stamps on every packet. routd hosts the per-turn MCP socket in its own process, and runed only bridges the guest to that socket. The section "The peer credential" describes the bridge.

runed does these jobs:

- It serves `POST /v1/runs`, the contract between routd and runed: claim, spawn, return.
- It serves `POST /v1/holds`. A hold claims the run slot of a folder for an external job that needs the folder alone. Examples are a filesystem restore, a vacuum and a migration. No agent turn starts in that folder until the hold ends.
- It runs the lifecycle of each KVM guest: one `qemu-system-x86_64` process, its virtio-fs shares, its tap and its egress proxy. A ctl frame over vsock carries the stop and the deadline warning. A kill reaps every pid that the record of the turn names. The section "The guest's turn, step by step" describes the steps.
- It enforces `RUNED_RUN_TIMEOUT` per run. An hourly sweep deletes spawns older than seven days. The retention bounds the one partition that all spawns share.
- On graceful shutdown it detaches the runs in flight. A guest outlives the daemon, and the next runed reaps it from the record on disk.

## Entities runed owns

A live folder returns `busy=true`; routd retains the batch for refeed after
the active run ends, then dispatches it with its own turn identity.

The `runed` owner is separate from routd. It owns `spawns`, `session_log`, `circuit_breaker` and `audit_log`. These tables hold runtime state that has no home in routd. They are not configuration that a manifest addresses, so `/openapi.json` exposes only the read path of `audit`.

Each table is an objstore entity, declared beside the code that reads it: `runed/runstore` for the first three and `audit` for the fourth. The driver that the opener holds creates the tables, so a second engine is a second driver and no handler changes. The `runed` package holds no SQL statement, and `runstore` holds none either.

The file of each entity states its own coordinate and what that choice costs.

- `spawns` takes one partition and the run id as sort key. Six operations address a spawn by run id with no folder in hand, and three of them are guarded transitions.
- `session_log` takes the folder and a time key, because every read asks for the n newest rows of one folder and nothing sweeps the table.
- `circuit_breaker` takes the folder as the whole coordinate.

| Entity            | Coordinate                                                          | Attributes                                                                                                                          |
| ----------------- | ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `spawns`          | one constant partition, then the run id                             | folder, topic, container name, `session_log_id`, `session_id`, kind, state, outcome, exit code, created, started and ended instants |
| `session_log`     | the folder, then the start instant at `core.TS` width plus a suffix | `session_id`, ended instant, result, error, message count                                                                           |
| `circuit_breaker` | the folder, as both halves                                          | consecutive failures, the last failure; it survives a restart                                                                       |

## Endpoints

The binary is `runed/cmd/runed/main.go`. It listens on `LISTEN_ADDR`, default `:8080`. The routes are in `server.go`, and the scope in brackets is the one that the route requires.

- `POST /v1/runs` starts a run [`runs:run`].
- `POST /v1/holds` claims the run slot of a folder and returns the handle at once [`runs:run`]. `DELETE /v1/runs/{run_id}` releases it.
- `POST /v1/runs/stop` is the operator kill by folder [`runs:kill`].
- `GET /v1/runs/{run_id}` returns the status of a run. `DELETE /v1/runs/{run_id}` kills it [`runs:kill`].
- `GET /v1/spawn_sessions` returns the `session_log` history, one row per agent spawn [`sessions:read`, bound to a folder]. The path is not `sessions`, because that wire identity belongs to the refresh-token families of authd.
- `GET /v1/spawn_sessions/recent` returns the n newest `session_log` rows with all fields [`sessions:read`]. routd uses it as a federation read.
- `GET /v1/audit` returns the `audit_log` of runed [`audit:read`, bound to a folder]. It is the only surface that answers "who killed that run". A kill leaves a `spawns` row that looks like a clean stop, and a busy hold leaves no row. `/dash/audit/` federates this path.
- `GET /dash/runed/` and `POST /dash/runed/kill` are the operator page of runed: active runs with a kill button per folder, and recent spawns. The identity that proxyd stamps for the operator gates them, and a bearer does not.
- `GET /openapi.json` and `GET /health` are public. With `METRICS_ENABLED=true`, `GET /metrics` serves the metrics.

A kill is the one revoke that runed has, because runed mints nothing. `Manager.Kill` (`manager.go`) is idempotent. A kill on a run that already exited answers 200 and changes no row. The state of a killed run is `killed`. It carries no `error` outcome and does not count toward the breaker of the folder, because a deliberate stop is not a failure.

## Dependencies

- `auth` verifies tokens offline against the authd JWKS.
- `container` renders the mounts, stages the app source and holds the egress rules. The other packages are `core`, `groupfolder`, `obs`, `resreg` and `types`.
- At run time runed depends on `authd` for the JWKS. It depends on `routd`, which hosts the per-turn agent MCP socket.

## Configuration

- `AUTHD_URL` is the authd base URL. runed fetches the JWKS once at boot and only verifies tokens. It holds no signing key and exchanges no token, because it makes no outbound call that needs one. When the variable is unset, the `/v1` verifier and the `/dash/runed/` gate stay open, which is the shape for local development. A failed fetch is fatal.
- `DYNAMODB_URL` and the store credentials name the store server that holds the entities of runed. `runed.Open` dials it, and `INSTALL.md` section "The store server holds every table of the instance" describes it.
- `RUNED_RUN_TIMEOUT` is the run ceiling. The default is 20m. It bounds the hard kill of the guest and the query timeout of the agent inside the guest. The query timeout is `ARIZUKO_QUERY_TIMEOUT_MS`, which equals `RUNED_RUN_TIMEOUT` minus 30 s with a floor of 60 s (`queryTimeoutMs` in `render.go`). So the agent aborts and delivers a summary before runed kills the guest.
- `RUNED_SHUTDOWN_GRACE` is the wait for in-flight handlers on graceful shutdown. The default is the value of `RUNED_RUN_TIMEOUT`.
- `LISTEN_ADDR` is the HTTP listener. The default is `:8080`.
- `MAX_CONCURRENT_CONTAINERS` is the cap on live guests. The default is 5. It comes from `core.LoadConfig`, together with the directory settings `GroupsDir`, `IpcDir`, `StoreDir` and others.

### The turn substrate

One substrate serves the whole instance and nothing selects it: one KVM guest per turn. The `substrate` log line at startup and the `/dash/runed/` page show what the substrate delivers.

runed refuses to start unless every precondition below holds. The refusal names all failed preconditions at once, because an input that is missing only at turn time gives a fleet that looks healthy and answers nothing.

- runed runs as root, and `/dev/kvm`, `/dev/vhost-vsock` and `/dev/net/tun` open.
- `RUNED_QEMU` runs `-version`. The default is `qemu-system-x86_64`.
- `RUNED_KERNEL_DIR` holds `bzImage` and `initrd.gz` from `make kernel`.
- `RUNED_ROOTFS_DIR` is the release rootfs that `make images` exports once per release.
- `RUNED_VIRTIOFSD` advertises `--uid-map` and `--gid-map`. The default is `/usr/libexec/virtiofsd`, where the Rust daemon of Alpine lands. runed does not send those flags. It uses them to tell the Rust build from the C build of QEMU. The C build needs real root, and runed refuses it.
- `RUNED_EGRESS_BIN`, `RUNED_FIREWALL_BIN` and `RUNED_IP_BIN` are executable. Their defaults are `egress`, `iptables` and `ip`.
- `RUNED_GUEST_MEM` times `MAX_CONCURRENT_CONTAINERS` fits the `MemTotal` of the host. The default guest size is `2G`. The guest size and the cap are one setting.
- `RUNED_TAP_SUBNET` holds one `/30` for every slot that the cap allows.

**The clamp is the uid of the daemon.** A child that runs as the uid of the agent exports the writable share. So the daemon cannot create a host file owned by anyone else, whatever uid the guest states. Without the clamp, a guest as uid 0 writes files owned by host root into the folder, and they outlive the turn. This was measured on 2026-09-24. An id map cannot replace the clamp here. It is legal only under `--sandbox namespace`, and docker refuses the `/proc` mount of that sandbox.

Tunables, with their defaults:

| Variable                | Default              | Meaning                                          |
| ----------------------- | -------------------- | ------------------------------------------------ |
| `RUNED_GUEST_SMP`       | 2                    | CPUs per guest                                   |
| `RUNED_VSOCK_PORT_BASE` | 5555                 | first host vsock port; a slot uses two ports     |
| `RUNED_VSOCK_CID_BASE`  | 3                    | first guest CID                                  |
| `RUNED_TAP_SUBNET`      | `10.97.0.0/24`       | subnet for the taps                              |
| `RUNED_PROXY_PORT`      | 3128                 | port of the egress proxy on the tap              |
| `RUNED_VM_DIR`          | `<project root>/vms` | directory of the VM records                      |
| `RUNED_BOOT_TIMEOUT`    | 30s                  | wait for the ready frame                         |
| `RUNED_REAP_GRACE`      | 5s                   | wait for the `exit` frame before SIGKILL to QEMU |

The CID space and the vsock port space belong to the host, not to the instance. Two instances on one host that both start at CID 3 and port 5555 ask for the same values. The second turn then fails with "address already in use". So each instance needs its own `RUNED_VSOCK_CID_BASE` and `RUNED_VSOCK_PORT_BASE`.

The machine is an ACPI microvm with modern virtio-MMIO transports and a shared memfd bound through `memory-backend=mem`. A `virtio-balloon-device` with `free-page-reporting=on` gives pages that the guest frees back to the host during the turn. It does not return the page cache of the guest. The pinned kernel must set `CONFIG_VIRTIO_BALLOON` and `CONFIG_PAGE_REPORTING`; the Firecracker base config sets both. ACPI supplies device discovery and poweroff. The kernel command line uses `loglevel=5`, which keeps warnings and errors. The pinned kernel uses LZ4 compression.

PID 1 is the static Go binary `guest-init` in the initrd. The release rootfs supplies the Debian and Node userspace and the agent tools. Each turn uses that rootfs read-only with a temporary writable overlay.

### Boot timing measures the first ready frame

**The budget is 1.5 s at p50**, from QEMU start to the first decoded ready frame. There is no warm pool: every turn boots a fresh guest. A warm guest could never become the guest of a folder. The identity of a guest is its mount set, and QEMU fixes that set in the argument list at start. The decision reopens when boot cost dominates a turn. Then the pool, its claim of a booted guest, its idle policy and its reaper change together as one unit. The measurement below meets the budget.

Production logs `guest ready` once per turn with `boot_ms` and the probe map of the guest. A monotonic host clock starts immediately before the start of the QEMU process and stops at the first decoded ready frame. This frame comes before root assembly and agent startup. Guest uptime excludes QEMU startup and measures a different interval.

`make bench-boot` compares q35 and microvm in alternating pairs through the production argument renderer. It uses the tools of the release image, an owned internal network, a test database, and private process and mount namespaces. It checks boot poweroff, both visible pre-control failures, and normal, Kill and cancelled turns. The turn proof checks these items:

- two CPUs, stdin, steering and MCP
- UID 1000 writes and a read-only root
- the final 1 MiB shared write
- the proxy policy and blocked direct traffic
- zero guest DNS packets received by the host
- cleanup

The untraced run on 2026-10-18 used source `8649d24d0c257079112fd913eb1d00bc48de9f4a`, QEMU 9.0.2, virtiofsd 1.10.1, two CPUs and 2 GiB per guest. It produced all 40 samples with `make bench-boot` in `BENCH_MODE=microvm` with `BENCH_PAIRS=20`. The variables `DATA_DIR`, `KERNEL_DIR` and `ROOTFS_DIR` name the instance data, the kernel directory and the rootfs directory of the release.

| Machine                   | Samples | First ready p50 | First ready p90 |
| ------------------------- | ------- | --------------- | --------------- |
| q35                       | 20      | 1263 ms         | 1354 ms         |
| ACPI microvm, modern MMIO | 20      | 1085 ms         | 1157 ms         |

The calculations use the logged integer milliseconds and the nearest-rank p90. Microvm was faster in 19 of 20 pairs. The median paired saving was 206.5 ms. The ratio of medians gives a reduction of 14.09%. All functional and cleanup assertions passed without skips. After the run, both owned containers and the owned network were absent.

Host load changed from 2.50/1.63/4.03 to 2.75/1.94/3.96 during the run. CPU pressure `some avg10` changed from 6.67% to 18.15%. Memory pressure `some avg10` changed from 0% to 0.09%, and full pressure stayed at 0%. I/O pressure `some avg10` stayed at 0%. The host was shared, so these figures include its load.

Three separate turn observations detected the entrypoint marker of the probe agent at 1300, 1200 and 1100 ms from host `Run`. They cover the normal, Kill and cancel turns. The host polls every 50 ms. These observations measure probe startup and not the latency of a model response. They do not form a latency distribution.

The raw samples are not in the repository. The tests behind the proof are [boot test](qemu_boot_test.go), [turn proof](qemu_turn_boot_test.go), [argument checks](qemu_boot_profile_test.go) and [control tests](vmctl_test.go). The vsock loopback CID test does not prove isolation between two real VMs. Two acceptance gaps remain: a live guest-observable smoke test, and recovery after an abrupt runed crash.

## Health signal

`GET /health` returns 200 once the process is up. A red flag is a spawn that stays in `state=queued`. The concurrency cap is full, or a precondition that the guest needs went away under a running daemon.

## Observability

runed emits these metrics when `METRICS_ENABLED=true`:

- `arizuko_container_spawns_total` counts spawn attempts, by folder and outcome.
- `arizuko_container_active` is a gauge of running guests.
- `arizuko_container_duration_seconds` is the guest run time, by folder and outcome.

runed also emits the two HTTP metrics that every daemon emits (`obs/README.md`). Its spans are `container_spawn` and `cross_daemon`.

### Audit trail

runed writes `audit_log` rows into its own tables. Each daemon owns its audit trail, and `turn_id` correlates the trails. runed records two actions, with category `agent` and surface `rest`:

- `run.hold`: `POST /v1/holds` claimed, or was refused, the run slot of a folder.
- `run.kill`: `DELETE /v1/runs/{run_id}` or `POST /v1/runs/stop`.

Both rows name the caller. `actor` is the JWT sub of the bearer, and `actor_sub` is the bare principal. **A turn dispatch writes no audit row.** The `spawns` row already carries kind, state, outcome, exit code and timings, and dashd renders it. A row per turn would duplicate it at turn volume. `spawns` never records who asked, and a busy hold or a kill that found nothing live writes no `spawns` row at all. The renderer is in `runed/audit.go`.

## The turn envelope

The stdin frame of the guest carries model credentials only. They come from two sources. The first is the operator anchors in the host `.env`, the keys of `store.EnvProfileKeys`: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN`, `OPENAI_API_KEY` and `CODEX_API_KEY`. The second is the own rows of the triggering user for the same keys, which let a user bring an own account (`routd.DB.EnvProfileSecrets`). The user rows overlay the anchors.

When `MODEL_GATEWAY_URL` configures a model gateway, `spawnSecrets` withholds those keys from both sources. The guest then holds only `MODEL_GATEWAY_KEY`, the virtual key of the gateway. runed resolves capability credentials, scoped to a folder or a user, on the host at tool-call time through the broker, and they never enter the guest.

The agent model and the query timeout travel in the process environment as `ARIZUKO_MODEL` and `ARIZUKO_QUERY_TIMEOUT_MS`. ant reads them at module load in `index.ts` and `claude.ts`. `turnEnv` is the one place that decides the environment, and a key that it omits is silently dead in the guest.

### The port carries one rendered turn

`renderTurn` (`render.go`) is the one renderer. A wire request goes in and one `Turn` comes out: the mount table, the process environment, the stdin frame and the egress allowlist. The substrate delivers those bytes and names, and it reads no grant, persona or session id. A new per-turn fact therefore changes the renderer and never the port.

The port is `Runtime` (`runtime.go`), and it has two methods: `Run` and `Kill`. The own state of the substrate hangs off `Turn.Handle`: the CID, the tap and the pids. So `Kill(handle)` finds all of it. `Turn.RegisterSteer` is the one hook beside the two methods. The substrate calls it once the guest is up, and it hands back the steer closure.

The renderer also writes the per-turn files that the mounts point at:

- `.claude/settings.json` with the `arizuko` MCP server (`socat STDIO UNIX-CONNECT:/run/ipc/gated.sock`, in `settings.go`)
- the gateway caps file
- the context layers that `preparePayload` puts before the prompt: the pending-migration note, the episodes, the newest 14 diary entries, `work.md`, the `<user>` pointer, `PERSONA.md` and `SYSTEM.md`

### Admission is claim or reject

`Manager.Run` (`manager.go`) answers in one of two ways and keeps no queue. A second queue inside runed would drift from the queue of routd, which already holds the batch.

| Folder state                         | Answer                                     | routd then                                                     |
| ------------------------------------ | ------------------------------------------ | -------------------------------------------------------------- |
| a live guest or spawn row            | `busy`                                     | re-feeds the batch later, no cursor advance, no breaker charge |
| cap `MAX_CONCURRENT_CONTAINERS` full | `busy`                                     | the same                                                       |
| idle, under the cap                  | a `spawns` row in `queued`, then the guest | waits for the outcome                                          |

- runed keeps no state outside the store. Every admission decision reads the `spawns` rows under one mutex for check and claim. A map in memory would lie after a restart: a folder would stay wedged, or the cap would reset. At boot, `ExpireOrphans` ends every row that a dead runed left live, as `exited` with outcome `error` and never `killed` (`runstore/spawns.go`).
- `StartSpawn` takes the claim with a condition on `state='queued'`. A `DELETE` that lands between the claim and the launch wins, and the run answers `busy`. A `killed` row makes the folder read free, so a launch without this guard would put two guests on one mount.
- A new inbound message clears a breaker streak at the threshold of 3, so a retry after three failures does spawn. The run that reaches the threshold reports `breaker_open`.
- routd owns session lineage. It mints the id and sends it on the request. runed mints an id only for a request that carries none, which is an isolated task. `spawns.session_id` is an echo.
- Readiness is spawn success. There is no `/readyz` probe, because a probe on the spawn path adds a failure mode. `Manager.spawn` arms the one deadline from `RUNED_RUN_TIMEOUT`, lowered by the `container_config` timeout of the group. The substrate keeps no timer. The wrap-up warning, two minutes before the end, comes from that deadline.

### The guest's turn, step by step

`qemuRuntime.spawn` (`qemu.go`) runs these steps in order. Every step appends its pid to the VM record on disk when it starts the process. So a runed that dies at any step leaves a record that names what it started.

1. Allocate the CID, the slot of the turn, its two vsock ports `ctl` and `mcp`, its tap and its `/30`, and write the record (`allocate`).
2. Build the staging tree and start two `virtiofsd` processes. `root` serves the release rootfs through a read-only bind. `turn` serves the staging tree and runs as the uid of the agent. The section "Mounts" describes both shares.
3. Bring up the tap and its rule set. Then start the `egress` proxy of this turn on the host side of the tap (`vmnet.go`).
4. Bind both vsock listeners before QEMU starts. A bind error fails the turn, and runed never picks another port. A port that a squatter already holds then fails with no guest in existence. A silent retry on another port would turn that denial into a guest that talks to whoever answered.
5. Fork the MCP bridge as the uid of the agent. The bridge holds the `mcp` listener.
6. Start QEMU as an ACPI microvm with the kernel and initrd from `make kernel`. Give it two `vhost-user-fs` devices, one `vhost-vsock` device with the CID, one `virtio-net` device on the tap, and `-no-reboot`.
7. Accept `ctl` only from the CID of this guest. Then send the `turn` frame, which holds the environment and the stdin payload.
8. `guest-init` (`kernel/guest-init/main.go`) is PID 1 in the initrd. It sends `ready`, assembles the root, starts `socat UNIX-LISTEN:/run/ipc/gated.sock` toward `VSOCK-CONNECT:2:<mcp>`, and runs ant as uid 1000. The stderr of ant comes back as `log` frames, and the last frame of the guest is one `exit`.

The kernel console goes to `groups/<folder>/logs/container-<ts>.console.log`, beside the turn log `container-<ts>.log`.

The turn log header carries `Peak guest memory: <n> MiB`, the peak resident set of the QEMU process of this turn (`Rusage.Maxrss`; the touched pages of the guest memory count). The line `guest exited` carries the same value as `peakGuestKiB`. It reads `unknown` (and logs 0) when QEMU did not exit within `RUNED_REAP_GRACE` after its SIGTERM. Use it to set `RUNED_GUEST_MEM`: `grep 'Peak guest memory' groups/*/logs/container-*.log`. The value is in the logs only; `Runtime` and `RunResult` do not carry it.

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

The host assigns the CID of the guest, and the vhost driver of the host drops every packet whose source is not that CID. A guest cannot claim the CID of another guest. The bridge accepts a connection only from the CID of its own turn, and it dials one path, the folder socket of that turn. The check in routd stays the `SO_PEERCRED` uid check that it runs on any socket peer.

To reach the tools of folder B, an agent in folder A would need the CID of B. Or the bridge of A would have to dial the socket of B. The host kernel refuses the first, and the bridge holds one path, which refuses the second. A unix socket does not cross virtio-fs, so the socket of the guest lives on a tmpfs inside the guest and never on the share.

### Deadline warning and stop

A follow-up for a folder with a live guest is refused with `busy`; routd keeps the batch and feeds it again after the run ends. The run deadline sends a `steer` frame on `ctl` that warns the agent. `guest-init` writes it under `/run/ipc/input`, signals the agent and answers `steer_ack`.

The run deadline sends `stop`. `guest-init` sends SIGTERM to the agent and powers off after 10 seconds. runed waits `RUNED_REAP_GRACE` for the `exit` frame. Then it sends SIGKILL to the QEMU pid and confirms that the process is gone, because a guest power-off does not prove that QEMU exited. `DELETE /v1/runs/{run_id}` reaps the same way, from the record on disk.

### How a turn ends

| How the guest ended                                          | Outcome routd reads                       |
| ------------------------------------------------------------ | ----------------------------------------- |
| `exit` frame, code 0                                         | `ok`; the reply already went out over MCP |
| `exit` frame, code 78 (`EX_CONFIG`, the agent's own verdict) | `error`, `terminal`                       |
| `exit` frame, any other code                                 | `error`, retryable                        |
| QEMU ended with no `exit` frame                              | `error`: no frame is no result            |
| the deadline                                                 | `error`, "Guest timed out"                |
| QEMU did not start; no renderer; a gateway and no key        | `error`, `terminal`                       |

runed decides whether a failure is terminal or retryable. The decision rides `RunOutcome.Terminal`, and its zero value is retryable. routd then skips the retry and shows the error text of runed. The raw exit code never leaves runed. If routd classified the prose of runed, it would need a second classification path on a format string. `guest-init` reports its own failures as 71 and never as 78, so it cannot impersonate the verdict of the agent.

### Reaping a turn

The VM record on disk names every process that the turn started: QEMU, two `virtiofsd`, the `egress` proxy and the MCP bridge, plus the tap. runed keeps no index of guests in memory. So `Kill`, the teardown of the turn and the boot sweep after a crash all reap from the same record (`reapVM` in `vmrecord.go`).

1. QEMU first, and alone. If runed removed the filter, the proxy or the share first, the running guest would keep a route or a write path that nobody watches.
2. The rule set of the tap, then the tap.
3. The proxy, the bridge and the two `virtiofsd` processes.
4. The staging tree: unmount, then remove. A tree that will not go is reported by path and kept for an operator.
5. The record, only once every pid that it names is confirmed gone.

runed signals a pid only when its `/proc/<pid>/cmdline` carries the token of the turn. A stale record may name a pid that the kernel has reused. `-no-reboot` on QEMU and `reboot=t` on the guest keep a power-off final. The reap still sends SIGKILL to QEMU after the grace, because a guest power-off does not prove that QEMU exited.

## Mounts

A guest sees its files through two virtio-fs shares. ACPI microvm allows eight MMIO devices and a turn uses four, so every mount family rides one share and not one device each.

<!-- twin:mounts -->

```
┌──────────────────────────────┐  ┌──────────────────────────────┐
│ release rootfs               │  │ host paths, as runed sees    │
│ <data>/images/rootfs-<hash>/ │  │ them: the mount table below  │
└──────────────┬───────────────┘  └──────────────┬───────────────┘
               │ read-only bind                  │ one bind per entry;
               │                                 │ RO is a host remount
               ▼                                 ▼
┌──────────────────────────────┐  ┌──────────────────────────────┐
│ <turn dir>/rootfs            │  │ <turn dir>/tree: guest paths,│
│                              │  │ plus mounts.json             │
└──────────────┬───────────────┘  └──────────────┬───────────────┘
               │ virtiofsd as root               │ virtiofsd as the
               │ share "root"                    │ agent's uid, share
               │                                 │ "turn"
  host ────────┼─────────────────────────────────┼──────────── guest
               ▼                                 ▼
┌──────────────────────────────┐  ┌──────────────────────────────┐
│ overlay: rootfs lower,       │  │ /turn, then one bind per     │
│ 512m tmpfs upper: the        │  │ manifest entry onto its      │
│ agent's /, gone at power-off │  │ guest path                   │
└──────────────────────────────┘  └──────────────────────────────┘
/run/ipc: a 16m guest tmpfs, never on a share; /dev/shm: 1g tmpfs
```

<!-- /twin -->

`buildMounts` (`mounts.go`) renders the table below. `treePlan` (`vmtree.go`) turns it into the staging tree, sorted by guest path so a parent comes before its children. The tree mirrors the guest paths, so the host and the guest share one naming scheme. `mounts.json` at the root of the tree tells `guest-init` what to bind, and the guest reads the same declaration that the host wrote.

- **Read-only is enforced on the host.** A read-only entry is a bind plus a remount with `MS_RDONLY` before `virtiofsd` starts. So every write that the daemon attempts fails with `EROFS`. The release rootfs gets the same bind, because `virtiofsd` 1.10.1 has no read-only export flag. A guest that mounts the rootfs writable would change the release that every later turn boots from.
- **The writable share runs as the uid of the agent.** So it cannot create a host file owned by anyone else, whatever uid the guest states. The clamp in "The turn substrate" describes this.
- **Sizes are explicit.** The overlay upper and each guest tmpfs live in guest RAM inside `RUNED_GUEST_MEM`: 512m for the upper, 16m per manifest tmpfs, and 1g for `/dev/shm`. A write past a cap fails with `ENOSPC` in the guest and never causes memory pressure on the host.
- **`/run/ipc` is never shared.** A unix socket does not cross virtio-fs. So the socket directory of the agent is a guest tmpfs, and the socket is bridged over vsock. "The peer credential" describes the bridge.

Every host path is the path that runed itself sees. `virtiofsd` is a child of runed in the mount namespace of runed, so no source passes through `HOST_DATA_DIR`. runed does not read that variable, because it is a compose fact.

| Guest path                                   | Host path                                                             | Mode                                             | When                                                |
| -------------------------------------------- | --------------------------------------------------------------------- | ------------------------------------------------ | --------------------------------------------------- |
| `/home/node`                                 | `<data>/groups/<folder>/`                                             | RW                                               | every turn                                          |
| `~/.codex`                                   | `<data>/groups/<folder>/.codex/`                                      | RW                                               | every turn: codex reads its rollouts here to resume |
| `~/.codex/auth.json`, `~/.codex/config.toml` | `HOST_CODEX_DIR/<file>`                                               | RO                                               | `HOST_CODEX_DIR` set                                |
| `/opt/arizuko`                               | `<data>/app-src/`, the release's staged `ant/`                        | RO                                               | every turn                                          |
| `/var/lib/share`                             | `<data>/groups/<world>/share/`; `groups/share/` for an elevated turn  | RW, RO with a `share_mount(readonly=true)` grant | every turn                                          |
| `/run/ipc`                                   | none: a guest tmpfs                                                   | RW                                               | every turn                                          |
| `/var/lib/www/arizuko`                       | `<data>/web/pub/arizuko/`                                             | RO                                               | no `web:publish` grant                              |
| `/var/lib/www`                               | `<data>/web/pub/`                                                     | RO                                               | `web:publish`                                       |
| `~/public_html`                              | `<data>/web/pub/<folder>/`, served at `/pub/<folder>/`                | RW                                               | `web:publish`                                       |
| `~/private_html`                             | `<data>/web/priv/<folder>/`, served at `/priv/<folder>/` behind login | RW                                               | `web:publish`                                       |
| `/var/lib/groups`                            | `<data>/groups/`                                                      | RW                                               | elevated `/root` turn                               |
| `/mnt/<name>`                                | an operator extra in the group's `container_config`                   | RO                                               | the path lies under `MOUNT_ALLOWED_ROOTS`           |
| `/app/src`                                   | `HOST_APP_DIR/ant/src`                                                | RW                                               | `ARIZUKO_DEV` set                                   |

**The web view is narrow for a reason.** The tree `/var/lib/www` holds the published content of every group. A group may gate part of its own content with an `auth` or `deny` `web_routes` row. proxyd applies that row at the edge, and a bind mount cannot apply it, so the whole tree stays a grant for publishers. `pub/arizuko` is the platform doc site and not content of a group. Every folder ships a skill that reads it, so every folder sees that one subtree. The two slots are views into the canonical web tree: a write appears at the home path and at the URL path at once. Without a grant the guest has no writable web surface.

**Operator extras pass `mountsec`.** `ValidateAdditionalMounts` (`mountsec/mountsec.go`) refuses every extra when `MOUNT_ALLOWED_ROOTS` is empty. It refuses a relative path and a path outside an allowed root, and it puts the guest path under `/mnt/`. An extra is read-only. A read-write extra needs an elevated turn and a root that allows RW, and the roots that `MOUNT_ALLOWED_ROOTS` names never do. `ValidateFilePath` is the other half. It guards a path that an MCP tool receives against a symlink escape and a blocklist: `.ssh`, `.gnupg`, `.aws`, `.env`, `credentials`, private keys and similar names.

The substrate adds `HTTP_PROXY` and its three companion variables to the environment, because their value is the address of the substrate itself.

## Files

- `cmd/runed/main.go` wires the daemon: verifier bootstrap and shutdown.
- `server.go` is the HTTP surface and the bearer and scope gate.
- `audit.go` renders the run-slot audit rows: what runed records, and why it does not record turns.
- `manager.go` is the Manager: run lifecycle, concurrency, queue and circuit breaker.
- `runtime.go` is the port: the `Runtime` interface (`Run`, `Kill`) and the `Turn` that it carries.
- `render.go` is the one renderer: a wire request in, one `Turn` out. It works with `mounts.go` for the mount table, `settings.go` for `settings.json` and the output style, and `secrets.go` for the credential that a guest receives.
- `runtimes.go` holds `FakeRuntime`, the test seam.
- `qemu.go` is the KVM substrate, the only one. `NewQemuRuntime` builds it, checks its preconditions and reaps the guests of a previous runed. The file also holds the argument list, the turn and the reap.
- `qemuhost.go` states what the host can run. It checks every precondition once at start, including the uid clamp and the guest memory against the concurrency cap.
- `vmrecord.go` holds the VM record on disk, which names every pid the turn starts, and the disk scan that reaps what a dead runed left.
- `vmtree.go` builds the staging tree: one share that mirrors the guest paths.
- `vmctl.go` holds the `ctl` frame protocol, the vsock listeners and the MCP bridge for uid 1000 (`runed --mcp-bridge`).
- `vmnet.go` sets up the tap of the turn, its proxy-only rule set and the `egress` process on the host side of the tap.
- `turnlog.go` writes the per-turn log beside the files of the group, with secrets removed.
- `hold.go` serves `POST /v1/holds`, the claim on the run slot of a folder.
- `dash.go` serves `/dash/runed/` and its kill form.
- `db.go` persists spawns and sessions, and it holds the boot that dials the store endpoint.
- `runstore/` holds the three entities and their coordinates, with no SQL.
