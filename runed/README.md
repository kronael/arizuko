# runed

runed implements the execution plane of an arizuko instance, and routd calls it over REST to run each turn. It admits a turn from routd, runs the turn in one KVM guest and ends the guest at the run deadline.

## What runed does

runed carries the opaque group harness document as `ARIZUKO_HARNESS`.
Instance defaults arrive through `Config.AgentEnv`; ant owns their selection
and validation. Settings affect the next run. Before switching backends, end
active runs and use `/new` on affected topics.

Model API keys use the env-profile policy. Direct mode also stages the
operator's `HOST_CODEX_DIR` login read-only for each turn, without a capability
grant. Gateway mode withholds both real model keys and the host login.
Backend selection confers no network access; configure egress separately.

runed never appends a message, because routd is the only appender. runed never signs or brokers a token. The credential of a turn is the vsock CID of its guest, which the host kernel stamps on every packet. routd hosts the per-turn MCP socket in its own process, and runed only bridges the guest to that socket. The section "The peer credential" describes the bridge.

runed does these jobs:

- It serves `POST /v1/runs`, the contract between routd and runed: claim, spawn, return.
- It serves `POST /v1/holds`. A hold claims the run slot of a folder for an external job that needs the folder alone. Examples are a filesystem restore, a vacuum and a migration. No agent turn starts in that folder until the hold ends.
- It runs the lifecycle of each KVM guest: one `qemu-system-x86_64` process, its virtio-fs shares, its tap and its egress proxy. A ctl frame over vsock carries the stop. A kill reaps every pid that the record of the turn names. The section "The guest's turn, step by step" describes the steps.
- It enforces `RUNED_RUN_TIMEOUT` per run. An hourly sweep deletes spawns older than seven days. The retention bounds the one partition that all spawns share.
- On graceful shutdown it drains HTTP handlers for RUNED_SHUTDOWN_GRACE. Request cancellation can end a guest; disk records support startup reaping.

## Entities runed owns

A live folder returns `busy=true`; routd retains the batch for refeed after
the active run ends, then dispatches it with its own turn identity.

The `runed` owner is separate from routd. It owns `spawns`, `session_log` and `audit_log`. These tables hold runtime state that has no home in routd. They are not configuration that a manifest addresses, so `/openapi.json` exposes only the read path of `audit`.

Each table is an objstore entity, declared beside the code that reads it: `runed/runstore` for the first two and `audit` for the third. The driver that the opener holds creates the tables, so a second engine is a second driver and no handler changes. The `runed` package holds no SQL statement, and `runstore` holds none either.

The file of each entity states its own coordinate and what that choice costs.

- `spawns` takes one partition and the run id as sort key. Six operations address a spawn by run id with no folder in hand, and three of them are guarded transitions.
- `session_log` takes the folder and a time key, because every read asks for the n newest rows of one folder and nothing sweeps the table.

| Entity        | Coordinate                                                          | Attributes                                                                                                                          |
| ------------- | ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `spawns`      | one constant partition, then the run id                             | folder, topic, container name, `session_log_id`, `session_id`, kind, state, outcome, exit code, created, started and ended instants |
| `session_log` | the folder, then the start instant at `core.TS` width plus a suffix | `session_id`, ended instant, result, error, message count                                                                           |

## Endpoints

The binary is `runed/cmd/runed/main.go`. It listens on `LISTEN_ADDR`, default `:8080`. The routes are in `server.go`, and the scope in brackets is the one that the route requires.

- `POST /v1/runs` starts a run [`service:routd` with `runs:run`].
- `POST /v1/holds` claims the run slot of a folder and returns the handle at once [`runs:run`]. `DELETE /v1/runs/{run_id}` releases it.
- `POST /v1/runs/stop` is the operator kill by folder [`runs:kill`].
- `GET /v1/runs/{run_id}` returns the status of a run. `DELETE /v1/runs/{run_id}` kills it [`runs:kill`].
- `GET /v1/spawn_sessions` returns the `session_log` history, one row per agent spawn [`sessions:read`, bound to a folder]. The path is not `sessions`, because that wire identity belongs to the refresh-token families of authd.
- `GET /v1/spawn_sessions/recent` returns the n newest `session_log` rows with all fields [`sessions:read`]. routd uses it as a federation read.
- `GET /v1/audit` returns the `audit_log` of runed [`audit:read`, bound to a folder]. It is the only surface that answers "who killed that run". A kill leaves a `spawns` row that looks like a clean stop, and a busy hold leaves no row. `/dash/audit/` federates this path.
- `GET /dash/runed/` and `POST /dash/runed/kill` are the operator page of runed: active runs with a kill button per folder, and recent spawns. The identity that proxyd stamps for the operator gates them, and a bearer does not.
- `GET /openapi.json` and `GET /health` are public. With `METRICS_ENABLED=true`, `GET /metrics` serves the metrics.

A kill is the one revoke that runed has, because runed mints nothing. `Manager.Kill` (`manager.go`) is idempotent. A kill on a run that already exited answers 200 and changes no row. The state of a killed run is `killed`. It carries no `error` outcome, because a deliberate stop is not a failure.

## Dependencies

- `auth` verifies tokens offline against the authd JWKS.
- `container` reads package assets and holds the egress rules. The other packages are `core`, `groupfolder`, `obs`, `resreg` and `types`.
- At run time runed depends on `authd` for the JWKS. It depends on `routd`, which hosts the per-turn agent MCP socket.

## Configuration

| Variable                                                                                           | Default                                 | Meaning                                                                                                           |
| -------------------------------------------------------------------------------------------------- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `RUNED_KERNEL_DIR`                                                                                 | `required`                              | Directory containing bzImage and initrd.gz.                                                                       |
| `RUNED_ROOTFS_DIR`                                                                                 | `required`                              | Exported release rootfs tree.                                                                                     |
| `RUNED_QEMU`                                                                                       | `qemu-system-x86_64`                    | QEMU executable.                                                                                                  |
| `RUNED_VIRTIOFSD`                                                                                  | `/usr/libexec/virtiofsd`                | Rust virtiofsd executable with required mapping support.                                                          |
| `RUNED_EGRESS_BIN`                                                                                 | `egress`                                | Per-turn forward proxy executable.                                                                                |
| `RUNED_FIREWALL_BIN`                                                                               | `iptables`                              | Host firewall executable.                                                                                         |
| `RUNED_IP_BIN`                                                                                     | `ip`                                    | Host link/address configuration executable.                                                                       |
| `RUNED_GUEST_MEM`                                                                                  | `2G`                                    | Memory per guest; host preflight multiplies by concurrency cap.                                                   |
| `LISTEN_ADDR`                                                                                      | `:8080`                                 | HTTP listener.                                                                                                    |
| `AUTHD_URL`                                                                                        | unset                                   | JWKS source; unset opens local development API and dashboard gates.                                               |
| `RUNED_RUN_TIMEOUT`                                                                                | `60m`                                   | Run ceiling; ant query timeout is 30 s shorter with a 60 s floor.                                                 |
| `RUNED_SHUTDOWN_GRACE`                                                                             | run timeout                             | Drain time for active HTTP handlers.                                                                              |
| `MAX_CONCURRENT_AGENTS`                                                                            | `5`                                     | Live guest limit and host memory preflight multiplier.                                                            |
| `DATA_DIR`                                                                                         | working directory                       | Instance root; groups, ipc, web, store-path label, and VM default.                                                |
| `HOST_DATA_DIR`                                                                                    | DATA_DIR                                | Host view of the instance root.                                                                                   |
| `APP_SRC_DIR`                                                                                      | HOST_APP_DIR, then executable directory | Release assets visible to runed.                                                                                  |
| `HOST_APP_DIR`                                                                                     | executable directory                    | Host source path and asset fallback.                                                                              |
| `HOST_CODEX_DIR`                                                                                   | unset                                   | Host Codex login tree staged read-only in direct model mode.                                                      |
| `MOUNT_ALLOWED_ROOTS`                                                                              | empty                                   | CSV roots permitted for extra host mounts.                                                                        |
| `ARIZUKO_DEV`                                                                                      | false                                   | Boolean parsed by strconv.ParseBool; enables development mount handling.                                          |
| `ARIZUKO_BACKEND`                                                                                  | unset                                   | Instance harness backend default sent to ant.                                                                     |
| `ARIZUKO_DEFAULT_MODEL`                                                                            | unset                                   | Instance model default sent as ARIZUKO_MODEL.                                                                     |
| `ARIZUKO_DEFAULT_EFFORT`                                                                           | unset                                   | Instance effort default sent as ARIZUKO_EFFORT.                                                                   |
| `MODEL_GATEWAY_URL`                                                                                | unset                                   | Gateway base; withholds operator model credentials and host login.                                                |
| `MODEL_GATEWAY_KEY`                                                                                | unset                                   | Parsed by shared config; runed supplies the per-turn request key to the guest.                                    |
| `ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_OAUTH_TOKEN, OPENAI_API_KEY, CODEX_API_KEY` | unset                                   | Operator model anchors read by spawnSecrets; triggering-user profile values overlay them.                         |
| `TZ`                                                                                               | `UTC` when unset or invalid             | Timezone from core.Timezone.                                                                                      |
| `ASSISTANT_NAME`                                                                                   | `arizuko`                               | Instance display name in the rendered payload.                                                                    |
| `WEB_HOST`                                                                                         | unset                                   | Public web origin in agent settings.                                                                              |
| `DYNAMODB_URL`                                                                                     | required                                | Store endpoint.                                                                                                   |
| `AWS_REGION`                                                                                       | `us-east-1`                             | Store signing region.                                                                                             |
| `AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY`                                                         | required                                | Daemon store identity.                                                                                            |
| `STORE_CA`                                                                                         | unset                                   | Base64 PEM certificate; unset uses system trust.                                                                  |
| `ARIZUKO_INSTANCE`                                                                                 | unset                                   | Audit and observability instance label.                                                                           |
| `LOG_LEVEL`                                                                                        | `info`                                  | JSON stderr verbosity, including turn logs.                                                                       |
| `OTEL_EXPORTER_OTLP_ENDPOINT`                                                                      | unset                                   | Optional OTLP logs.                                                                                               |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`                                                               | unset                                   | Optional OTLP trace export; independent of the log endpoint.                                                      |
| `MAX_CONCURRENT_CONTAINERS`                                                                        | absent                                  | Shared config rejects this variable whenever it is present; configure MAX_CONCURRENT_AGENTS for execution limits. |
| `METRICS_ENABLED`                                                                                  | `false`                                 | Literal true mounts public GET /metrics.                                                                          |

`core.LoadConfig` also parses the shared instance fields in [core/config.go](../core/config.go).
The table lists runed inputs that affect execution or its process surfaces.

### The turn substrate

One substrate serves the whole instance and nothing selects it: one KVM guest per turn. The `substrate` log line at startup and the `/dash/runed/` page show what the substrate delivers.

runed refuses to start unless every precondition below holds. The refusal names all failed preconditions at once, because an input that is missing only at turn time gives a fleet that looks healthy and answers nothing.

- runed runs as root, and `/dev/kvm`, `/dev/vhost-vsock` and `/dev/net/tun` open.
- `RUNED_QEMU` runs `-version`. The default is `qemu-system-x86_64`.
- `RUNED_KERNEL_DIR` holds `bzImage` and `initrd.gz` from `make kernel`.
- `RUNED_ROOTFS_DIR` is the release rootfs that `make images` exports once per release.
- `RUNED_VIRTIOFSD` advertises `--uid-map` and `--gid-map`. The default is `/usr/libexec/virtiofsd`, where the Rust daemon of Alpine lands. runed does not send those flags. It uses them to tell the Rust build from the C build of QEMU. The C build needs real root, and runed refuses it.
- `RUNED_EGRESS_BIN`, `RUNED_FIREWALL_BIN` and `RUNED_IP_BIN` are executable. Their defaults are `egress`, `iptables` and `ip`.
- `RUNED_GUEST_MEM` times `MAX_CONCURRENT_AGENTS` fits the `MemTotal` of the host. The default guest size is `2G`. The guest size and the cap are one setting.
- `RUNED_TAP_SUBNET` holds one `/30` for every slot that the cap allows.

**The clamp is the uid of the daemon.** A child that runs as the uid of the agent exports the writable share. So the daemon cannot create a host file owned by anyone else, whatever uid the guest states. Without the clamp, a guest as uid 0 writes files owned by host root into the folder, and they outlive the turn. An id map cannot replace the clamp here. It is legal only under `--sandbox namespace`, and docker refuses the `/proc` mount of that sandbox.

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

`guest ready` logs `boot_ms` from QEMU start to the first decoded ready frame.
The frame precedes root assembly and agent startup; it does not measure model response latency.
`make bench-boot` exercises the production renderer, guest control, mount behavior, egress policy, and cleanup.
[Boot tests](qemu_boot_test.go), [turn proof](qemu_turn_boot_test.go), and [control tests](vmctl_test.go) hold the assertions.
Local documentation checks do not execute those KVM tests or reproduce a performance result.
Two integration limits remain: a live guest-observable smoke test and recovery after an abrupt daemon crash.

## Health signal

`GET /health` returns 200 once the process is up. It does not test the store, capacity, model, or guest prerequisites again.
Inspect a stalled run through `/v1/runs/{run_id}`, its turn log, and its VM record.

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

The run request's `agent` field names the handler. `turnEnv` passes a nonempty
name as `ARIZUKO_AGENT`. Guest init executes `/usr/local/bin/<name>`;
an absent value selects `/usr/local/bin/ant`, the Bun executable.
The group's `agent` setting selects this name; see
[EXTENDING.md](../EXTENDING.md#swapping-the-agent-harness).

The stdin frame of the guest carries model credentials only. They come from two sources. The first is the operator anchors in the host `.env`, the keys of `store.EnvProfileKeys`: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN`, `OPENAI_API_KEY` and `CODEX_API_KEY`. The second is the own rows of the triggering user for the same keys, which let a user bring an own account (`routd.DB.EnvProfileSecrets`). The user rows overlay the anchors.

When `MODEL_GATEWAY_URL` configures a model gateway, `spawnSecrets` withholds those keys from both sources. The guest then holds only `MODEL_GATEWAY_KEY`, the virtual key of the gateway. runed resolves capability credentials, scoped to a folder or a user, on the host at tool-call time through the broker, and they never enter the guest.

The agent model and the query timeout travel in the process environment as `ARIZUKO_MODEL` and `ARIZUKO_QUERY_TIMEOUT_MS`. ant reads them at module load in `index.ts` and `claude.ts`. `turnEnv` is the one place that decides the environment, and a key that it omits is silently dead in the guest.

### The port carries one rendered turn

`renderTurn` (`render.go`) is the one renderer. A wire request goes in and one `Turn` comes out: the mount table, the process environment, the stdin frame and the egress allowlist. The substrate delivers those bytes and names, and it reads no grant, persona or session id. A new per-turn fact therefore changes the renderer and never the port.

The port is `Runtime` (`runtime.go`), and it has two methods: `Run` and `Kill`. The own state of the substrate hangs off `Turn.Handle`: the CID, the tap and the pids. So `Kill(handle)` finds all of it. `Turn.RegisterSteer` is the one hook beside the two methods. The substrate calls it once the guest is up, and it hands back the steer closure.

The renderer also writes the per-turn files that the mounts point at:

- `.claude/settings.json` with the `arizuko` MCP server (`socat STDIO UNIX-CONNECT:/run/ipc/gated.sock`, in `settings.go`)
- the gateway caps file
- the context layers that `preparePayload` puts before the prompt: the episodes, the newest 14 diary entries, `work.md`, the `<user>` pointer, `PERSONA.md` and `SYSTEM.md`

routd supplies selected package names from system and exact-folder DB rows.
The renderer reads their installed directories; runed opens no package database.
Core is required. Package selection is an input to the renderer, outside the
`Runtime.Run` / `Kill` port. Startup and turns run no package build commands.

### Admission is claim or reject

`Manager.Run` (`manager.go`) answers in one of two ways and keeps no queue. A second queue inside runed would drift from the queue of routd, which already holds the batch.

| Folder state                     | Answer                                     | routd then                                  |
| -------------------------------- | ------------------------------------------ | ------------------------------------------- |
| a live guest or spawn row        | `busy`                                     | re-feeds the batch later, no cursor advance |
| cap `MAX_CONCURRENT_AGENTS` full | `busy`                                     | the same                                    |
| idle, under the cap              | a `spawns` row in `queued`, then the guest | waits for the outcome                       |

- Admission uses store records; per-guest process and network state lives in VM records on disk. Every admission decision reads the `spawns` rows under one mutex for check and claim. A map in memory would lie after a restart: a folder would stay wedged, or the cap would reset. At boot, `ExpireOrphans` ends every row that a dead runed left live, as `exited` with outcome `error` and never `killed` (`runstore/spawns.go`).
- `StartSpawn` takes the claim with a condition on `state='queued'`. A `DELETE` that lands between the claim and the launch wins, and the run answers `busy`. A `killed` row makes the folder read free, so a launch without this guard would put two guests on one mount.
- routd owns session lineage. It mints the id and sends it on the request. runed mints an id only for a request that carries none, which is an isolated task. `spawns.session_id` is an echo.
- Readiness is spawn success. There is no `/readyz` probe, because a probe on the spawn path adds a failure mode. `Manager.spawn` arms the one deadline from `RUNED_RUN_TIMEOUT`. The substrate keeps no timer. ant sends the wrap-up warning five minutes before its query deadline.

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

A follow-up for a folder with a live guest is refused with `busy`; routd keeps the batch and feeds it again after the run ends. ant sends the deadline warning through its steering spool.

The run deadline sends `stop`. `guest-init` sends SIGTERM to the agent and powers off after 10 seconds. runed waits `RUNED_REAP_GRACE` for the `exit` frame. Then it sends SIGKILL to the QEMU pid and confirms that the process is gone, because a guest power-off does not prove that QEMU exited. `DELETE /v1/runs/{run_id}` reaps the same way, from the record on disk.

### How a turn ends

| How the guest ended                                          | Outcome routd reads                       |
| ------------------------------------------------------------ | ----------------------------------------- |
| `exit` frame, code 0                                         | `ok`; the reply already went out over MCP |
| `exit` frame, code 78 (`EX_CONFIG`, the agent's own verdict) | `error`, `terminal`                       |
| `exit` frame, any other code                                 | `error`, retryable                        |
| QEMU ended with no `exit` frame                              | `error`: no frame is no result            |
| the deadline                                                 | `error`, `terminal`, "Guest timed out"    |
| QEMU did not start                                           | `error`, retryable                        |
| no renderer; a gateway and no key                            | `error`, `terminal`                       |

runed decides whether a failure is terminal or retryable. The decision rides `RunOutcome.Terminal`, and its zero value is retryable. routd then skips the retry and shows a short cause from its own list. The raw exit code never leaves runed. If routd classified the prose of runed, it would need a second classification path on a format string. `guest-init` reports its own failures as 71 and never as 78, so it cannot impersonate the verdict of the agent.

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

| Guest path                                        | Host path                                                                           | Mode                                             | When                                                |
| ------------------------------------------------- | ----------------------------------------------------------------------------------- | ------------------------------------------------ | --------------------------------------------------- |
| `/home/node`                                      | `<data>/groups/<folder>/`                                                           | RW                                               | every turn                                          |
| `~/.codex`                                        | `<data>/groups/<folder>/.codex/`                                                    | RW                                               | every turn: codex reads its rollouts here to resume |
| `~/.codex/auth.json`, `~/.codex/config.toml`      | per-turn copy of `HOST_CODEX_DIR/<file>` in the ipc dir, owned by the run uid, 0400 | RO                                               | `HOST_CODEX_DIR` set, no model gateway              |
| `/opt/arizuko/ant`                                | `<data>/packages/core/`                                                             | RO                                               | every turn                                          |
| `/opt/arizuko/packages/<name>`                    | `<data>/packages/<name>/`                                                           | RO                                               | selected package                                    |
| `~/.claude/skills/<name>`                         | effective selected skill                                                            | RO                                               | no local `.disabled` marker                         |
| `~/.claude/hooks/<name>`                          | selected `hooks/<name>`                                                             | RO                                               | package supplies it                                 |
| `/usr/local/bin/<name>`                           | selected `bin/<name>`                                                               | RO                                               | package supplies it; guest PATH already includes it |
| `~/.claude/CLAUDE.md`                             | combined selected manuals in folder IPC scratch                                     | RO                                               | every turn                                          |
| `/etc/codex/config.toml`, `/etc/codex/hooks.json` | selected native hook configuration in folder IPC scratch                            | RO                                               | every turn, including an empty hook map             |
| `/var/lib/share`                                  | `<data>/groups/<world>/share/`; `groups/share/` for an elevated turn                | RW, RO with a `share_mount(readonly=true)` grant | every turn                                          |
| `/run/ipc`                                        | none: a guest tmpfs                                                                 | RW                                               | every turn                                          |
| `/var/lib/www/arizuko`                            | `<data>/web/pub/arizuko/`                                                           | RO                                               | no `web:publish` grant                              |
| `/var/lib/www`                                    | `<data>/web/pub/`                                                                   | RO                                               | `web:publish`                                       |
| `~/public_html`                                   | `<data>/web/pub/<folder>/`, served at `/pub/<folder>/`                              | RW                                               | `web:publish`                                       |
| `~/private_html`                                  | `<data>/web/priv/<folder>/`, served at `/priv/<folder>/` behind login               | RW                                               | `web:publish`                                       |
| `/var/lib/groups`                                 | `<data>/groups/`                                                                    | RW                                               | elevated `/root` turn                               |
| `/mnt/<name>`                                     | an operator extra in the group's `container_config`                                 | RO                                               | the path lies under `MOUNT_ALLOWED_ROOTS`           |
| `/app/src`                                        | `HOST_APP_DIR/ant/src`                                                              | RW                                               | `ARIZUKO_DEV` set                                   |

Core, system packages and exact-folder packages form an ordered skill overlay.
The later provider of a skill wins. A `.disabled` marker suppresses that skill's
mount. Custom skills and the group runbook stay writable.

The renderer combines package manuals once and preserves native imports.
It adds selected Claude hooks to the native settings while retaining platform
auth, MCP and sandbox settings. Codex loads selected hooks from its system
configuration layer alongside the harness policy hooks in `$CODEX_HOME`.

**The web view is narrow for a reason.** The tree `/var/lib/www` holds the published content of every group. A group may gate part of its own content with an `auth` or `deny` `web_routes` row. proxyd applies that row at the edge, and a bind mount cannot apply it, so the whole tree stays a grant for publishers. `pub/arizuko` is the platform doc site and not content of a group. Every folder ships a skill that reads it, so every folder sees that one subtree. The two slots are views into the canonical web tree: a write appears at the home path and at the URL path at once. Without a grant the guest has no writable web surface.

**Operator extras pass `mountsec`.** `ValidateAdditionalMounts` (`mountsec/mountsec.go`) refuses every extra when `MOUNT_ALLOWED_ROOTS` is empty. It refuses a relative path and a path outside an allowed root, and it puts the guest path under `/mnt/`. An extra is read-only. A read-write extra needs an elevated turn and a root that allows RW, and the roots that `MOUNT_ALLOWED_ROOTS` names never do. `ValidateFilePath` is the other half. It guards a path that an MCP tool receives against a symlink escape and a blocklist: `.ssh`, `.gnupg`, `.aws`, `.env`, `credentials`, private keys and similar names.

The substrate adds `HTTP_PROXY` and its five companion variables to the environment, because their value is the address of the substrate itself.

## Files

- `cmd/runed/main.go` wires the daemon: verifier bootstrap and shutdown.
- `server.go` is the HTTP surface and the bearer and scope gate.
- `audit.go` renders the run-slot audit rows: what runed records, and why it does not record turns.
- `manager.go` is the Manager: run lifecycle, folder exclusion and concurrency.
- `runtime.go` is the port: the `Runtime` interface (`Run`, `Kill`) and the `Turn` that it carries.
- `render.go` is the one renderer: a wire request in, one `Turn` out. It works with `mounts.go` for the mount table, `settings.go` for `settings.json`, and `secrets.go` for the credential that a guest receives.
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
- `runstore/` holds the two entities and their coordinates, with no SQL.

## HTTP route and resource map

| Method and path                                             | Caller and behavior                                               |
| ----------------------------------------------------------- | ----------------------------------------------------------------- |
| `POST /v1/runs`                                             | service:routd with runs:run; synchronous turn execution.          |
| `POST /v1/holds`                                            | runs:run; exclusive folder hold without a guest.                  |
| `POST /v1/runs/stop`                                        | runs:kill; stop by folder.                                        |
| `GET /v1/runs/{run_id}`, `DELETE /v1/runs/{run_id}`         | Run status or runs:kill; folder-bound where the caller is scoped. |
| `GET /v1/spawn_sessions`, `GET /v1/spawn_sessions/recent`   | sessions:read; local spawn history and federation reads.          |
| `GET /v1/audit`                                             | audit:read; resreg resource audit, no local MCP socket.           |
| `GET /dash/runed/`, `POST /dash/runed/kill`                 | Proxyd-attested operator page and kill form.                      |
| `GET /health`, `GET /openapi.json`, optional `GET /metrics` | Public liveness, mounted API, and metrics.                        |

routd dispatches runs; dashd reads history/audit and stops runs; the CLI can claim holds and stop folders.
runed fetches authd keys and bridges MCP to routd's Unix socket. It exchanges no service token.
The catalog audit action is `query_audit`; runed hosts no agent MCP server or derived run-management tools.
The guest sees `/run/ipc/gated.sock`; the bridge ends at routd's per-turn host socket.

## Persistent files and recovery

`StoreReach["runed"]` reaches only `runed`.
`runed.spawns` holds claims and outcomes, `runed.session_log` holds session attempts, and `runed.audit_log` holds operator mutations.
`PrepareOwner` also creates `resreg_scopes` for owner versioning.
The hourly spawn sweep deletes completed records older than seven days; session_log has no corresponding retention sweep.
Startup expires orphan spawn rows and reaps VM records before accepting work.

VM records and staging trees live under `RUNED_VM_DIR` (default `DATA_DIR/vms`).
The rootfs uses `RUNED_ROOTFS_DIR`; the kernel directory holds `bzImage` and `initrd.gz`.
Group homes and `.claude/settings.json` persist under `DATA_DIR/groups`.
Per-group turn logs live in `groups/<folder>/logs`; guest overlay changes outside shared mounts are transient.
Store failure can prevent a run claim or completion record; no local run queue takes over.
A routd request cancellation ends the run context. A guest cannot keep using MCP after routd exits.
Authd availability matters for key discovery; verification uses the cached public keyset.
The boot-ready timeout is 30 s, reap grace is 5 s, and the guest stop path allows 10 s before poweroff.
Grep `substrate`, `open runed's store`, `guest ready`, `expire orphan spawns`,
`runed: sweep aged spawns`, and `group home not writable`.
`/health` is process liveness; it does not rerun host preflight or establish model/provider readiness.

Host memory preflight considers this instance rather than all instances ([BUGS L513](../BUGS.md)).
Without a model gateway, model credentials enter the guest ([BUGS L131](../BUGS.md)).
Stopping routd and runed can end a live turn ([BUGS L161](../BUGS.md)).

Sources: [entry point](cmd/runed/main.go), [HTTP gates](server.go), [renderer](render.go),
[host configuration](qemuhost.go), [VM records](vmrecord.go), [owner reach](../store/owner_reach.go).

JWKS keys load lazily during verification through auth.FetchKeys; there is no fixed background JWKS ticker.
