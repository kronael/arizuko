---
status: defected
defects: [L379]
depends:
  [
    24-crackbox-lightest,
    25-qm-adoptions,
    ../5/P-runed,
    ../5/14-credentials,
    ../5/33-paths-roles,
  ]
---

# specs/6/41 — the QEMU substrate: one turn, one guest kernel

Decision (maintainer): **the turn substrate is a KVM virtual machine, one per
turn, driven by `qemu-system-x86_64`, and it is the only substrate arizuko
supports.** runed boots the release's guest kernel by direct kernel boot, exports
the turn's files through one virtio-fs share, and reaches the guest over vsock.
The machine is ACPI microvm with modern virtio-MMIO devices.
§"The engine" states the selected configuration.
Docker leaves the turn path: no `docker run`, no per-folder docker network, no
proxy container, no `docker.sock` in runed. The daemon fleet's packaging
(compose) is outside this spec.

**Signed off 2026-09-23** for build, after a review that changed five
mechanisms: the uid clamp on `virtiofsd` (§"The peer credential"), the guest ack
on a steer (§"Steer and stop"), `-no-reboot` plus a confirmed pid in the reap
(§"The turn" step 9), the host boot timer (§"The image and the guest
kernel"), and `Turn.RegisterSteer` kept rather than shed (§"Order" step 4). The
day estimate is accepted only after step 0 reports.

The turn model of [`5/A`](../5/A-primitives-framing.md) holds unchanged: the guest is
stateless, the folder and the database carry every fact across turns, and the
container mount table in `runed/README.md` § Mounts is the
contract the agent sees — the same left column, delivered by a different
right-hand side. `ant/` changes by zero lines.

## What the design must answer — measured 2026-09-12 on hel1v5 (the krons host)

These are constraints, not a verdict. A design that does not meet the first row
is not shipped.

| Constraint                     | Measured                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Boot budget                    | Host QEMU process start to the first decoded ready frame must be **≤ 1.5 s p50** on krons. This frame precedes root assembly and agent startup. The [runed measurements](../../runed/README.md#boot-timing-measures-the-first-ready-frame) own the current results.                                                                                                                                                                                    |
| Untuned QEMU, distro kernel    | `-M q35,accel=kvm` direct kernel boot, Debian 6.1.0-43 kernel, static Go init: **2.43 / 2.69 / 2.89 s** wall, 0.7–0.8 s guest uptime at init. `-M microvm`: **3.75–4.06 s**, 1.9–2.2 s guest uptime.                                                                                                                                                                                                                                                   |
| The VMM itself                 | `qemu-system-x86_64 -M microvm -S` start and quit: **0.09–0.13 s**. A paused VM configures no virtio-fs share, vsock or tap. This measurement cannot attribute the rest of a boot.                                                                                                                                                                                                                                                                     |
| Tuned QEMU, literature         | Firecracker's NSDI'20 paper §5.1: Linux 4.14, no modules, qboot, 1 vCPU/256 MB — QEMU boots within ~50 ms of end-to-end Firecracker; pre-configured Firecracker p99 146 ms (usenix.org/system/files/nsdi20-paper-agache.pdf).                                                                                                                                                                                                                          |
| Turn volume                    | krons: 162 turns in 7 days, 15 in the last 24 h (`container-*.log` count under `groups/`). Boot cost is per-turn latency the user sees, not throughput.                                                                                                                                                                                                                                                                                                |
| Host                           | 2 vCPU, 15 GB RAM, QEMU 7.2.22 (Debian 12), `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun` present; kernel ships `fs/fuse/virtiofs.ko` and the vsock transports; `socat 1.7.4.4` built `WITH_VSOCK`.                                                                                                                                                                                                                                                   |
| QEMU 7.2 devices               | `-M microvm`, `vhost-user-fs-device`, `vhost-vsock-device`, `memory-backend-memfd`, `-sandbox on` all present (`-M help`, `-device help`, `-object help`).                                                                                                                                                                                                                                                                                             |
| Image                          | `arizuko-ant:latest` is **8.26 GB** (`node:22-slim` + ~40 tool installs, `ant/Dockerfile`). The rootfs pipeline moves this once per release, never per turn.                                                                                                                                                                                                                                                                                           |
| Packages the runed image needs | Alpine carries `qemu-system-x86_64`, `virtiofsd`, `socat` (`apk search` from `arizuko:latest`, 2026-09-12); none of the three is installed in `arizuko:latest` today, so the image gains them (§"The runed deploy topology"). `nftables`/`iptables` verified at build, §Open.                                                                                                                                                                          |
| Host disk                      | hel1v5: **9.4 GB free of 256 GB** after `docker builder prune -f` recovered 4.37 GB of layer cache (2026-09-23); before it, 5.2 GB. `arizuko-ant` holds `latest` at 7.85 GB and two release tags at 8.26 GB. One rootfs export fits, and the operator reclaims (§"The host budget").                                                                                                                                                                   |
| virtiofsd and the uid clamp    | The packaged daemon is QEMU's C build at `/usr/lib/qemu/virtiofsd` (not on `PATH`), with NO id-mapping flags, and it needs real root: `setgroups()` fails in a user namespace, unprivileged, and in both sandbox modes. `mount_setattr(MOUNT_ATTR_IDMAP)` works on this 6.1 kernel but translates the caller, so it refuses a root caller with `EOVERFLOW`. The clamp is therefore the Rust `virtiofsd` (§"The peer credential"), measured 2026-09-24. |
| Host memory and concurrency    | `MAX_CONCURRENT_CONTAINERS` defaults to **5** (`core/config.go:229`) and caps total live spawns (`runed/manager.go:47-53`). Five guests at `-m 4G` is 20 GB against 15 GB total and ~5 GB free, so the guest size and the cap are one budget (§"The host budget").                                                                                                                                                                                     |

## The engine: `qemu-system-x86_64`, owned by arizuko

The VMM is the packaged QEMU binary, driven by `exec.Command` from `runed/`.

- **QEMU (taken).** Packaged on every Debian and Alpine host arizuko runs
  on; arizuko drives it by `exec.Command` in `runed/qemu.go`. The guest kernel is
  arizuko's to build and pin, which is the cost §"The image and the guest
  kernel" prices.
- **libkrun (not taken).** Apache-2.0, Rust with a C API, virtio-fs, vsock and
  TSI networking built in, kernel supplied by `libkrunfw`
  (github.com/containers/libkrun). arizuko builds with `CGO_ENABLED=1`, so
  the API is reachable. It is not taken because it is not packaged for the
  hosts, it brings a Rust build into the image, and its kernel is a bundled
  blob arizuko cannot configure — the pipeline exists either way, and with
  QEMU it is arizuko's.
- **smolvm / smol machines (not taken).** Apache-2.0 Rust engine over libkrun
  (github.com/smol-machines/smolvm, v1.15.0 2026-09-11, repository created
  2025-12-18); CLI plus Node and Python SDKs, no Go SDK
  (github.com/smol-machines/smol); a 2026 YC company
  (ycombinator.com/companies/smol-machines). A CLI from a nine-month-old
  vendor is not a contract arizuko's turn path can rest on, and its
  exec and socket transports are undocumented. `CelestoAI/SmolVM` is an
  unrelated project with the same name.

The production renderer in `runed/qemu.go` selects
`-M microvm,acpi=on,pcie=off,usb=off,memory-backend=mem` with
`-global virtio-mmio.force-legacy=false`. The shared memfd backs guest memory.
Both virtio-fs shares, vsock and network use their MMIO device transports.
ACPI supplies discovery and guest poweroff. The kernel command line retains
warnings and errors with `loglevel=5`.

The paired boot test compares microvm with q35 through that renderer.
Both variants use the same kernel, rootfs, memory, CPU count and isolation checks.
The [runed measurements](../../runed/README.md#boot-timing-measures-the-first-ready-frame)
support microvm and meet the accepted budget. Neither guessed MMIO addresses nor
per-turn machine selection enters the runtime port.

**Reopen trigger, one:** if NEITHER machine type on the pinned kernel meets the
boot budget on krons within the pipeline's costed budget (§"The image and the
guest kernel"), the engine question reopens for libkrun, whose bundled kernel is
the thing the pipeline would have failed to produce.

## The seam: one interface, one production type, one instance one substrate

`runed.Runtime` is the port: two methods, one production implementation
chosen at startup (`NewDockerRuntime` in `runed/docker.go`; `runed/qemu.go`
is the second), one fake for tests (`runed/runtimes.go`) and `holdRuntime`
for a run that starts nothing. The port and its one value are declared
together in `runed/runtime.go`:

```go
// Runtime starts one guest and stops it. ctx carries the deadline.
type Runtime interface {
	Run(ctx context.Context, t Turn) RunResult
	Kill(handle string) error
}

// Turn is the envelope: one guest, rendered. A substrate delivers every
// field and interprets none.
type Turn struct {
	Handle        string            // the substrate starts, steers and kills by it
	Folder        string            // the one path the substrate resolves
	Mounts        []core.Mount      // the mount table's right-hand side; Container is the guest path
	Env           map[string]string // the agent's process environment
	Stdin         []byte            // the agent payload: ant/src/index.ts ContainerInput
	Egress        []string          // the proxy allowlist, grants applied
	RegisterSteer func(steer func(batch string) bool)
}
```

`RunResult` does not change. `Handle` is what `steerInto`, `armCancel`,
`Kill` and `holdRuntime`'s slot act on, and what step 1 of §"The turn"
records the VM under. `Folder` is read for paths only: the ipc dir of the
Docker steer, the `logs/` dir of the turn log, the name of the Docker egress
network. `Mounts`, `Env`, `Stdin` and `Egress` reach Docker as `-v`, `-e`,
the stdin pipe and the proxy container, and QEMU as the staging tree, the
`{env, stdin}` frame on `ctl` and the proxy on the tap; a substrate adds the
proxy variables (`HTTP_PROXY` and its three companions) because their value
is its own address. `RegisterSteer`'s closure is what each substrate makes of
it (§"Steer and stop").

The deadline is `ctx`. `Manager.spawn` arms it from `RunTTL`, and the group's
`container_config` `Timeout` is an input to that one deadline, never a
substrate's timer. A substrate with a timer of its own reads `ctx.Deadline()`.

**The renderer.** The envelope half of `container.Run` — `prepareInput`,
`buildMounts`, `seedSettings`, `spawnSecrets`, `pickOutputStyle`, the
payload marshal — becomes one function in `runed` that takes the wire request
(`runedv1.RunRequest`), the handle and the deadline and returns a `Turn`.
`Manager.spawn` calls it, then `Run`. The `docker run` half — `buildArgs`,
the process start, its stderr reader and its timers — becomes the body of
`runed/docker.go`; `container/` keeps group setup, seeding, snapshots and the
egress proxy engine. `container.Runner`, `container.Input`,
`container.Output` and `RunSpec` are deleted: the facts they copied between
them are declared once, on the wire request, and rendered once.

**Where `Turn` lives, and why.** In `runed`, beside the interface. Its only
readers are runed's: the manager fills it, the two runtimes and the fake
consume it. `container/` then declares no turn type, so the port imports
nothing named for a substrate. `substrate/` cannot hold it — its own rule
refuses a field that holds a folder or an allowlist — and `runed/api/v1` is
routd's format, not the port's.

**What leaves the port, and who takes it.**

- `RunID`, `Kind`, `Isolated` → the manager: `SetSteer(runID, …)`,
  `executor(kind)`, the session-log decision in `spawn`. No runtime reads them.
- `RunTTL` → `ctx`; the renderer writes `ARIZUKO_QUERY_TIMEOUT_MS` into
  `Env` from the deadline.
- `ContainerName` → `Handle`. `ContainerConfig.Mounts` → `Mounts`, through
  `mountsec`; `ContainerConfig.Timeout` → the deadline.
- `MessageBatch`, `SessionID`, `ChatJID`, `Topic`, `TurnID`, `TriggerSender`,
  `Secrets`, `Channel` → `Stdin`.
- `Model`, `Effort`, `GHWrite`, `ModelGatewayKey`, `Elevated` → `Env`.
  `Elevated`, `ShareReadOnly`, `WebPublish` → `Mounts`. `Channel`, `ChatJID`,
  `Topic`, `WebPublish`, `Elevated` → `settings.json`.
- `EgressAllowlist` and the `Egress` grant → `Egress`.
- `CallerSub`: nothing in runed reads it. It stays on the pinned wire body
  and goes nowhere.
- `Input.MsgCount`, `GroupPath`, `PaneLookup`: no production setter; they
  leave with `Input`.

**What this forecloses.** A substrate cannot read a grant, a persona or a
session id: it holds names, rows and bytes. A new per-turn fact changes the
renderer and never the port. A substrate's own state — a CID, a tap, a
network — is keyed by `Handle` and is never a `Turn` field, so `Kill(handle)`
finds all of it. One payload format serves both substrates; `ant/` changes by
zero lines. A third method on `Runtime`, or a `Turn` field a substrate must
interpret, needs an approved plan first (root `CLAUDE.md`, "A port is two
methods and stays two").

Two answers the port does not change:

- **Does this make the runner multi-machine? No.** runed spawns one VM per
  turn on its own host, exactly as it spawns one container per turn on its own
  host. The seam gains no scheduler and no notion of a second host; a second
  host is a second instance, as today.
- **What selects it, and whether one instance may run both.** During the
  transition, `RUNED_SUBSTRATE=docker|qemu` in the instance `.env`, read once
  at runed start and logged with the substrate line runed already prints
  (`runed/cmd/runed/main.go:67`, `runed/dash.go:84`). It is instance-wide:
  never per folder, never per turn, and an instance runs exactly one
  substrate. The seam IS the transition. The last step of §"Order" deletes
  the Docker runtime, the env var and `substrate.Docker`; `Runtime` survives
  as the test seam with one production type, which is what "one backend"
  means for the interface.

  **The env var, not a compose fragment.** A cutover at fragment granularity
  — an old runed service beside a new one — is refused on three measurements
  (2026-09-23). `<dataDir>/services/` holds package fragments only; on krons
  it holds `extenddb`, `kokoro`, `teled`, `teled-rhias`, `ttsd` and `whapd`,
  and runed is not one of them (`compose/compose.go:582` `readFragments`).
  `runedService` writes runed's block in Go (`compose/compose.go:1106`), so a
  change to it reaches every instance at the next `arizuko generate`, and
  that is how the `/dev/kvm` device line arrives — `compose/compose.go`
  writes no `devices:` and no `cap_add:` today. Two runed services would both
  serve `POST /v1/runs` (`runed/server.go:56`) and write the same spawn rows,
  which is one wire name with two owners (root `CLAUDE.md`, "A resource's
  name IS its wire identity"). The substrate is a file, not a daemon:
  `runed/docker.go` is 259 lines of runed's 3361 non-test lines.

## The turn

routd binds the folder's `gated.sock` before the spawn, as today
(`routd/mcp.go:578-591`). runed then:

1. Allocates the VM's CID (`≥ 3`, unique among live VMs on the host) and two
   vsock ports on the host side, `ctl` and `mcp`, and records all three in
   the spawn's on-disk VM record (§"What moves into `runed`") under the
   `Handle` the Manager pinned. Every later step appends its pid to that
   record as it starts the process, so a runed that dies at any point leaves a
   record naming what it started.
2. Builds the turn's staging tree (§"The files") and starts two `virtiofsd`
   processes: `root` over the release's unpacked rootfs, read-only, and
   `turn` over the staging tree.
3. Brings up the tap with its rule set and starts the `egress` proxy on the
   tap's host side (§"Egress").
4. Binds the `ctl` and `mcp` listeners on `AF_VSOCK` (`unix.SockaddrVM`,
   `golang.org/x/sys/unix`), each accepting only a peer whose CID is this
   VM's.
5. Starts QEMU on the measured argument list (§"The engine"; `-sandbox on` is NOT
   in it — it was never measured against the `vhost-user-fs` plus `vhost-vsock`
   set, and the list that works omits it):
   `-M microvm,acpi=on,pcie=off,usb=off,memory-backend=mem`
   and `-enable-kvm -cpu host -smp 2 -m <RUNED_GUEST_MEM>` with
   `-object memory-backend-memfd,share=on` (virtio-fs requires the shared
   backend: virtio-fs.gitlab.io/howto-qemu.html), `-kernel`/`-initrd` from
   the release, two `vhost-user-fs-device`s, one `vhost-vsock-device` with
   the CID, one `virtio-net-device` on the tap, `-serial file:<turn log>`,
   `-pidfile`, `-no-reboot`, and `-global virtio-mmio.force-legacy=false`.
   Four virtio-mmio devices of the eight microvm allows.
6. The guest's init (`guest-init`, a static Go binary in the initrd) mounts
   the base filesystems and both shares, dials `ctl`, and sends the ready frame.
   It then assembles `root` read-only with a tmpfs overlay upper, bind-mounts each
   family onto its canonical path, prepares guest-local IPC, and receives one
   JSON frame `{env, stdin}`: `Turn.Env` and `Turn.Stdin`.
7. `guest-init` starts `socat UNIX-LISTEN:/run/ipc/gated.sock,fork
VSOCK-CONNECT:2:<mcp>` on a guest-local tmpfs and execs ant as uid 1000
   with the environment and the envelope on stdin. `settings.json`'s MCP
   entry (`runner.go:912-915`) and `ant/src/mcp.ts:3` see the same path.
8. Tool calls: ant → `gated.sock` → socat → vsock → runed's `mcp` bridge →
   routd's `gated.sock`. Steer and stop arrive on `ctl` (§"Steer and stop").
   ant's stderr streams over `ctl` as `{log}` frames into the turn log.
9. ant exits; `guest-init` sends `{exit: code}` on `ctl` and powers the
   guest off; QEMU exits; runed reaps in order: QEMU first and alone, then the
   tap rules and the tap, then the `egress` proxy, the MCP bridge and the two
   `virtiofsd`, then the staging tree, then the record. QEMU goes first because
   taking away a running guest's filter, its proxy or its share leaves it running
   with a route nobody watches. A `virtiofsd`
   normally exits when its socket closes; the reap does not rely on that — it
   signals the recorded pid and waits, because the record is what the disk
   scan replays after a crash. `ctx` expiry is `SIGKILL` on the QEMU pid, then
   the same reap.

   **A guest power-off does not prove QEMU exited.** The argument list carries
   `-no-reboot` and the guest command line carries `reboot=t`. The reap waits
   for the QEMU pid with a bounded grace, uses `SIGKILL` if needed, and confirms
   the pid is gone before it removes the record.

   The boot test requires natural poweroff for both machine variants and both
   malformed-control cases. The turn proof requires cleanup after normal
   completion, Kill and cancellation. The bounded grace and `SIGKILL` remain
   independent of the guest's shutdown path.

`RunResult.ExitCode` is the `{exit}` frame; `Outcome` and `NewSessionID`
arrive by `submit_turn` over MCP, unchanged.

## The mechanisms, one by one

| Docker fact today                                                                                        | QEMU answer                                                                                                                                                                                                                                                                              |
| -------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `docker run -i --rm --name … --shm-size=1g` (`runner.go:680-682`)                                        | one QEMU process per turn (§"The turn" step 5); `/dev/shm` sized by `guest-init`                                                                                                                                                                                                         |
| nine mount families as `-v` (`runner.go:539-669`, `741-744`)                                             | one staging tree, one `turn` share (§"The files")                                                                                                                                                                                                                                        |
| `-e KEY=VALUE` and the stdin pipe, from `Turn.Env` and `Turn.Stdin`                                      | the `{env, stdin}` frame on `ctl`; nothing touches the kernel command line or a file                                                                                                                                                                                                     |
| `--user uid:gid` when the host uid is not 1000 (`runner.go:731-737`)                                     | the guest agent is always uid 1000; `virtiofsd` runs in a single-id user namespace, so every ownership the guest asks for lands as host uid 1000 or fails (§"The peer credential")                                                                                                       |
| routd's socket visible through the bind-mounted `ipc/` dir (`ipc/ipc.go:361-363`, `runner.go:912-915`)   | vsock, CID-pinned, bridged by runed (§"The peer credential"); a unix socket does not cross virtio-fs — a bind-mounted socket answers "Operation not supported" (github.com/abiosoft/colima/issues/997)                                                                                   |
| SO_PEERCRED uid check (`ipc/ipc.go:397-410`)                                                             | unchanged in routd; the bridge child runs as uid 1000 and the guest is bound by CID (§"The peer credential")                                                                                                                                                                             |
| `docker kill --signal=SIGUSR1` (`runed/docker.go:47-49`, `runner.go:378`)                                | a `{steer}` frame on `ctl` (§"Steer and stop")                                                                                                                                                                                                                                           |
| stop → `docker kill` → `rm -f` (`runner.go:288-299`, `runed/docker.go:56-58`)                            | `{stop}` frame → `SIGTERM` to ant → power-off; `SIGKILL` QEMU on the deadline; reap                                                                                                                                                                                                      |
| `docker network create --internal` + `--ip` (`container/network.go:157-163`, `runner.go:724-725`)        | one tap per turn; the guest's only route is the host side of its tap (§"Egress")                                                                                                                                                                                                         |
| the egress proxy as a sidecar container on two networks (`container/egress.go:238-274`)                  | the `egress` binary as a process on the tap's host side, `--client <guest ip>`; `EGRESS_IMAGE`, `EGRESS_UPLINK_NETWORK`, `EGRESS_NETWORK_PREFIX/_SUBNET` retire                                                                                                                          |
| `docker ps --filter=name=arizuko-` orphan sweep (`container/runtime.go:100`)                             | runed's own disk scan: every on-disk VM record, plus a check of the recorded pid (§"What moves into `runed`")                                                                                                                                                                            |
| `docker info` liveness (`container/runtime.go:124-131`)                                                  | `/dev/kvm` opens and `qemu-system-x86_64 -version` runs, checked once at runed start, refused loudly                                                                                                                                                                                     |
| stderr over the CLI's pipe, `docker logs` unused (`runner.go:245-253`)                                   | `{log}` frames on `ctl`; kernel console on `-serial file:` into `container-<ts>.console.log` BESIDE the turn log — never the turn log itself, which `writeLog` truncates with `os.WriteFile` at turn end (`runed/turnlog.go:67`) and would destroy the console exactly when it is wanted |
| session copy is host-FS (`runner.go:1330-1360`); `docker cp`/`exec` unused                               | unchanged                                                                                                                                                                                                                                                                                |
| runed in a container with `/var/run/docker.sock` (`compose/compose.go:1115-1118`)                        | §"The runed deploy topology"                                                                                                                                                                                                                                                             |
| `arizuko-ant` built by `docker build`, distributed by being built on the deploy box (`Makefile:147-161`) | built the same way, then unpacked once per release to a host directory (§"The image and the guest kernel")                                                                                                                                                                               |

## The peer credential

Today the turn's credential is the host kernel's `SO_PEERCRED`: routd rejects
any peer whose uid is not the agent's (`ipc/ipc.go:397-410`, expected uid at
`routd/mcp.go:589`), and the _folder_ binding is the mount — only the folder's
container has the folder's `ipc/` dir. Across a VM boundary the uid is a guest
fact and proves nothing, so the design moves the folder binding to a host
kernel fact of the same weight.

**The binding is the CID.** The host assigns the guest's CID
(`VHOST_VSOCK_SET_GUEST_CID`) and the host's vhost driver drops every packet
whose source is not that CID — "Only accept correctly addressed packets"
(github.com/torvalds/linux/blob/master/drivers/vhost/vsock.c,
`vhost_vsock_handle_tx_kick`). A guest cannot claim another guest's CID any
more than a container can claim another uid. `vsock(7)` fixes the host at CID 2
(man7.org/linux/man-pages/man7/vsock.7.html).

**The bridge is a uid-1000 child of runed, one per turn.** It holds the `mcp`
listener for exactly one VM, accepts a connection only from that CID
(`getpeername` on the accepted socket), and dials exactly one path: that
turn's folder socket. routd's check is byte-for-byte the check it runs today —
the peer is uid 1000 — and `ipc.ServeMCP` does not change.

**The hole is real and measured, not reasoned.** 2026-09-24 on hel1v5: a guest
booted as **uid 0**, mounted the host's share over virtio-fs, read the host's file
(`turn_read: "hello-from-host"`) and wrote a new one. On the host that file is
owned by **`root root`** — created by a guest, owned by host root, and still there
after the guest is gone. `virtiofsd` ran as root with `-o sandbox=chroot`, which is
the deploy shape §"The runed deploy topology" describes, and that was enough. So
the clamp below is not defence in depth; without it the substrate hands every
folder a host-root write.

**The CID identifies the VM, not the process inside it.** That is the limit of
the binding, and it matters for the file path rather than the tool path: a guest
kernel under the agent's control states the uid and gid that `virtiofsd` applies
to a host file. A compromised guest can therefore ask for host-root ownership or
a setuid mode on any WRITABLE export, and a `chown` or a `chmod` on the folder
outlives the power-off that ends the turn — a restart does not undo it. So the
uid is clamped, and **the clamp costs one package, not a Rust build.** Measured
2026-09-24: Alpine 3.20 packages the RUST `virtiofsd` as `virtiofsd` (1.10.1),
installed at `/usr/libexec/virtiofsd`, so `apk add virtiofsd` in the `arizuko`
image is the whole cost. The plan for a Rust build stage
(gitlab.com/virtio-fs/virtiofsd) is dropped.

**The flags are `--uid-map` and `--gid-map`, not `--translate-uid`.** That name
was a guess and it was wrong: 1.10.1's own `--help` has no `translate` of any
kind. Each takes a RANGE, `:namespace_id:host_id:count:` — its help says
"`:0:100000:65536:` will map the 65536 host UIDs [100000, 165535] into the
namespace as [0, 65535]" — so the map does NOT collapse many guest ids onto one
host id, as this section first claimed. It states which guest ids exist on the
host, and an id outside the range has none. The turn runs ant as uid 1000, so the
map the operator sets is that one id — `:1000:1000:1:` — and a compromised guest
running as uid 0 falls outside it.

**THE MAP CANNOT RUN IN A CONTAINER, and the clamp is the daemon's UID instead.**
Measured three ways on krons 2026-09-25 (BUGS `L372`), after this section had
specified `--uid-map` with `--sandbox=none`:

| what runed does                                | virtiofsd's answer                                                                                                            |
| ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| root + `--uid-map`                             | "uid_map can only be used by unprivileged user where sandbox mod is namespace"                                                |
| uid 1000 + `--sandbox namespace` + `--uid-map` | `failed to execute newuidmap`, and with `shadow-uidmap` present, `Error entering sandbox: MountProc(Operation not permitted)` |
| uid 1000 + `--sandbox=none`, no map            | **serves**                                                                                                                    |

The namespace sandbox mounts a fresh `/proc`; docker's own `/proc` carries locked
masked paths, so the nested mount is `EPERM` however the map is written. Neither
half of the map's precondition can hold for a daemon inside runed's container.

So the writable share is exported by a child running as **the agent's own uid**,
and that IS the control the map was for: a daemon with no privilege cannot create
a host file owned by anyone else, whatever uid the guest states. It is simpler than
the map and needs nothing of the host — no `newuidmap`, no user namespace, no
unmasked `/proc`. The read-only rootfs share stays root's, because it is read-only.
`RUNED_VIRTIOFSD_UID_MAP` and `RUNED_VIRTIOFSD_GID_MAP` are deleted; the
`--uid-map` probe in `--help` stays, because it is how the precondition tells the
Rust build from QEMU's C one, which needs real root and therefore cannot be
clamped at all. So the acceptance is two writes, not one: from
the guest as uid 1000 the host file carries host uid 1000, and from the guest as
uid 0 the write is REFUSED, because an unmapped id has no host identity to write
under.

**runed sends the flags the check requires, from one declaration each**
(`clampUIDFlag`, `clampGIDFlag`). The substrate shipped 2026-09-24 with
`--translate-uid` in its argument list while the check looked for `--uid-map`, so
every VM turn would have failed its share with "unexpected argument" and no test
saw it — the test read its expectation off the code (BUGS `L367`, fixed
2026-09-25). The map's SHAPE is checked at runed start for the same reason: the
daemon parses the range itself and exits on a bad one.

**Three cheaper mechanisms were measured on hel1v5 2026-09-24 and all three
refuse.** They are written down so nobody pays to learn it twice.

1. **The packaged daemon has no id mapping.** It is QEMU's C `virtiofsd` at
   `/usr/lib/qemu/virtiofsd` (`qemu-system-common`, QEMU 7.2, not on `PATH`),
   and its `--help` lists `sandbox`, `xattrmap`, cache, lock and thread options
   with no `translate` of any kind.
2. **A single-id user namespace clamps, but the C daemon cannot run in one.**
   The clamp itself is real: inside `unshare -U --map-user=1001
--map-group=1001`, `chown 0:0` and `chown 1002:1002` both return `EINVAL`
   and the host file keeps the mapped id. The daemon does not get that far — it
   calls `setgroups()` at startup and fails
   `setgroups() failed with error=1:Operation not permitted` in a namespace,
   unprivileged, and under `sandbox=chroot` and `sandbox=namespace` alike. A
   namespace that maps one id writes `deny` to `/proc/self/setgroups`, so the
   call cannot succeed there even when root creates the namespace. The C daemon
   therefore requires real root, and real root is what the clamp must remove.
3. **An idmapped mount translates the CALLER, so it cannot redirect root.**
   `mount_setattr(MOUNT_ATTR_IDMAP)` IS available on this 6.1 kernel — the
   probe created an idmapped clone of the share successfully. It maps the
   caller's identity through the namespace, so a root caller, whose id the
   single-id map does not contain, gets `EOVERFLOW` on the first `open` rather
   than a translated write. It clamps a mapped caller and refuses an unmapped
   one; it cannot make a root daemon's writes land as a non-root owner.

So the Rust daemon is not a preference, it is the only mechanism left, and the
Rust build it brings is the price of the one control that closes this hole. That
is a different trade from §"The engine"'s refusal of libkrun: there a Rust build
bought a bundled kernel arizuko could not configure, here it buys a security
boundary with no alternative.

One residue the clamp does not cover: `chmod`. Measured in the namespace probe,
`chmod 4755` succeeds and the mode reaches the host. That costs nothing, because
a setuid bit on a file owned by the agent's own uid yields the uid the agent
already has, and setuid root needs owner root, which the translation refuses.

This is the one place where the VM boundary is weaker than the container
boundary, which stamps `--user uid:gid` from the host side, and the translation
is what restores it.

Why this is at least as hard to forge as the mount: both links are host-kernel
facts a guest cannot influence and a host process needs root to change. What
moves is where the folder binding is stated — from a Docker mount set fixed at
`docker create` to a CID fixed at QEMU start and a bridge whose dial target is
fixed at fork. The threat the check exists for — an agent in folder A reaching
folder B's tools (`SECURITY.md` §Model) — needs either B's CID (the host kernel
denies) or A's bridge dialing B's socket (the bridge holds one path).

**A port squat is a denial, and the bind order is what makes it one.**
Measured 2026-09-23 on hel1v5: an UNPRIVILEGED host process binds a host-side
vsock port, and a sibling compose container given `--device /dev/vhost-vsock`
binds one too — the host's second bind of that port then fails with
`EADDRINUSE` (`socat VSOCK-LISTEN:7777` in an `alpine` container, then the same
port from the host). The host has one vsock port namespace and every holder of
that device shares it.

That cannot become a hijack, because runed binds `ctl` and `mcp` at step 4 and
starts QEMU at step 5. A squatter that binds first makes runed's bind fail
before any guest exists, so there is no traffic to receive and no secret to
take. **runed therefore fails the turn on a bind error and never picks another
port**: a silent retry is what would turn a denial into a guest talking to
whoever answered. The residual is a neighbour on the operator's own host
denying turns, which is the trust that host already carries.

## Steer and stop

A follow-up for a folder with a live guest is refused: `Manager.Run` answers
`busy`, routd keeps the batch and feeds it again after the run ends, with its
own turn context and MCP binding. runed never writes a follow-up into a live
guest.

The `{steer}` frame on `ctl` remains for one use, the deadline warning: runed
sends `{steer: batch}`; `guest-init` writes the batch into the guest's
`/run/ipc/input/` and sends `SIGUSR1` to ant's pid. The frame is acknowledged by
the guest, never by the socket write: `guest-init` answers with `{steer_ack: id}`
AFTER it writes the batch and delivers `SIGUSR1`, and the closure
`RegisterSteer` hands back returns true ONLY on that ack, inside the grace runed
already waits. An ack that arrives after the guest starts power-off counts as no
ack. A write to `ctl` that returns no error proves only that the host queued
bytes.

`{stop}` sends ant `SIGTERM`; after the grace runed already uses for
`docker stop`, `guest-init` powers off regardless. The deadline kill is
`SIGKILL` on the QEMU pid from `-pidfile`. Only the host dials the guest's
control path and only the guest dials the host's; there is no third party on
either.

## Egress by name, sealed below the kernel

[`6/24`](24-crackbox-lightest.md) §"Two substrates, one design" states the VM
case: put the proxy on the host side of the tap and the guest's only route is
through it. This spec builds exactly that.

- The tap carries a `/30`; the guest gets the low address, the host side the
  high one. The rule set on the tap is **static**: accept guest → host-side
  `:3128`; drop everything else, both directions, UDP/53 included.
- runed starts `egress --listen <host side>:3128 --client <guest> --allow …`
  as a process, from the binary the `arizuko` image carries. The proxy's
  uplink is runed's own network. `--client` keeps its meaning: one proxy, one
  guest.
- The guest environment carries `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` as
  today (`runner.go:725-728`) and **no resolver**: a name is meaningful only
  in a `CONNECT` line. This settles `L136` on the VM path — the DNS side
  channel the `--internal` network leaves open does not exist here, because
  no packet leaves the tap except to the proxy.
- IP literals in `network_rules` go to the proxy too (`egress/match` already
  matches them), so netfilter carries no per-destination rule at all
  (`runed/vmnet.go` `tapRules`), and `L189` closes by construction: a name never
  reaches netfilter, and the proxy is the one matcher for names and addresses
  alike.
- runed's substrate profile delivers `domain`, and `Effective` says so from
  the same one named function shape `6/25` §2 requires: the proxy binary
  missing is an error at runed start, never a quiet open spawn.

Cost accepted: a tool inside the guest that resolves a name itself before
connecting fails. Which tools those are is measured, not guessed (§Open).

## The files: one staging tree, one share

`-M microvm` allows eight virtio-mmio devices, so nine families cannot be nine
shares; and a share is a directory, so single-file read-only overmounts
(`runner.go:601-611`) are not a share either. runed builds **one staging tree
per turn** with bind mounts in its own mount namespace, and one `virtiofsd`
exports it:

| Family (`runner.go`)                  | Tree path               | Mode                  |
| ------------------------------------- | ----------------------- | --------------------- |
| group home `:539-542`                 | `home/`                 | rw                    |
| staged `ant/` release `:548-552`      | `opt-arizuko/`          | ro                    |
| world `share/` `:555-565`             | `share/`                | rw, ro on the grant   |
| group `.codex/` `:601-611`            | `home/.codex/`          | rw                    |
| `auth.json`, `config.toml` `:601-611` | `codex-ro/<file>`       | ro, one bind per file |
| operator `/mnt/<name>` `:613-623`     | `mnt/<name>/`           | as declared           |
| `/var/lib/www` + web slots `:625-645` | `www/`, `pub/`, `priv/` | ro, rw, rw            |
| all groups, elevated only `:665-669`  | `groups/`               | rw                    |
| `ipc/` `:574-578`                     | —                       | guest-local tmpfs     |

**The tree MIRRORS the guest's paths, and a manifest names them.** A flattened
name collides — `/mnt/a-b` and `/mnt/a/b` both render `mnt-a-b` — so the staging
tree holds `tree/home/node`, `tree/var/lib/www` and so on, and runed writes
`mounts.json` at the share root. `guest-init` reads that file rather than knowing
the table, so a new mount family changes the renderer and never the guest.

`guest-init` bind-mounts each tree path onto the canonical container path
and the two `codex-ro` files over their `home/.codex` names. Read-only is
enforced on the host: a bind mount made `ro` before `virtiofsd` starts is
`EROFS` to every write the daemon attempts, and the acceptance test writes to
each ro family from the guest and expects `EROFS`. `mountsec.
ValidateAdditionalMounts` runs where it runs today; the tree adds no path
it did not validate. The `--sandbox=chroot` mode of `virtiofsd` is "intended
for container environments where the container runtime has already set up
the namespaces" (gitlab.com/virtio-fs/virtiofsd README), which is runed's
situation. Host-side changes reach the guest as they happen — the
`RegisterSteer` contract ("once the container + IPC dir are up") is satisfied
by the `ctl` handshake, not by a mount becoming visible.

`Persistence` stays `mounted_home`: the guest's own filesystem is a tmpfs
overlay discarded at power-off; what the folder keeps, it keeps because it is
a share. That overlay is RAM inside `-m`, so `guest-init` mounts it with an
explicit `size=` (§"The host budget").

## The image and the guest kernel

**A release is still an image.** `ant/Dockerfile` builds `arizuko-ant` as it
does today; the turn substrate consumes it as a directory. `make images`
gains one step: `docker create` + `docker export` of the image into
`<data>/images/ant-<version>/rootfs/`, once per release on the deploy host,
and `arizuko run` (initial setup) does the same. The 8.26 GB moves once. The
`root` share is that directory; nothing per turn copies or converts it.

**The guest kernel is its own artifact, not a stage of the ant image.** It is
built by `make kernel` into `<data>/images/kernel-<version>-<confighash>/`, and
`make images` depends on that path existing rather than producing it. Two
measurements force the split. `ant/` took **130 commits in 60 days** (~2 a day,
`git log --since` 2026-09-24) while the kernel changes when its pinned version or
its config fragment changes, which is close to never — so a stage inside
`ant/Dockerfile` risks a 30–60 minute compile against every skill-text edit. And
the disk lever of §"The host budget" is `docker builder prune -f`, which deletes
the build cache such a stage would live in: the spec would tell the operator to
throw away the kernel. A directory keyed by version and config hash is rebuilt
only when its key changes and survives every prune.

`make kernel` builds a pinned Linux 6.1 from Firecracker's
`microvm-kernel-ci-x86_64-6.1.config`
(github.com/firecracker-microvm/firecracker/tree/main/resources/guest_configs)
plus arizuko's fragment: `VIRTIO_FS`, `FUSE_FS`, `VIRTIO_VSOCKETS`,
`OVERLAY_FS`, `VIRTIO_MMIO`, `VIRTIO_NET`, built in, no modules.
`CONFIG_KERNEL_LZ4=y` selects kernel compression. The build validates the resolved
config against `kernel/arizuko.fragment` with `kernel/check-config.sh` before
compilation. The artifact holds `bzImage`, `kernel.config`, and a gzip initrd
containing the static Go `guest-init` binary and device nodes. The release rootfs
retains Debian/Node and the agent tools. Nothing per turn copies either artifact.

**The literature number is not this design's number.** The paper measures a
minimal init with no virtio-fs share to mount, no overlay to assemble and no
control handshake, so it bounds the kernel's own boot and nothing else.
Production and `make bench-boot` measure host QEMU process start to the first
decoded ready frame with a monotonic clock. Guest uptime and console markers
describe different intervals. The ready frame precedes root assembly and agent
startup. The separate probe-agent marker observes host `Run` to probe entrypoint
with file polling. It does not measure model response latency.

`bench-boot` compiles the native tests and runs them with the release image's
QEMU, Rust virtiofsd and egress binary. It owns an internal network, a test
database, and private process and mount namespaces. The tests use the production
argument renderer and explicit immutable kernel/rootfs paths. Missing prerequisites,
skips or an incomplete sample count fail the target. Traced comparisons remain
diagnostic and do not establish the performance gate.

Current results, the exact command and toolchain, pressure snapshots and evidence
paths have one owner: the
[runed measurements](../../runed/README.md#boot-timing-measures-the-first-ready-frame).

Owner: arizuko. Cost: **2–4 engineer-days** for the target, the fragment and
the bench target, plus one 30–60 minute build per kernel change — not per
release. The gate it must pass is the boot budget in the first table.

## The host budget

Three host resources bound this substrate, and the guest size is not free in
any of them. Measured on hel1v5 (the krons host) 2026-09-23.

- **Memory is the guest size times the concurrency cap.** `-m 4G` with
  `MAX_CONCURRENT_CONTAINERS` at its default 5 (`core/config.go:229`,
  `runed/manager.go:47-53`) is a 20 GB ceiling on a 15 GB host with ~5 GB free.
  `memory-backend-memfd` commits pages on touch, so the ceiling is not a
  reservation — but two guests that each touch 2 GB already exceed what is
  free, and the host has no swap headroom to hide it. **`-m` and the cap are
  one setting**: runed refuses to start when `guest_mem × MAX_CONCURRENT_CONTAINERS`
  exceeds the host's `MemTotal`, and the pair is set from Open question 4's
  peak-RSS measurement rather than from the 4 GB placeholder.
- **The guest returns freed pages during a turn.** The machine has a
  `virtio-balloon-device,free-page-reporting=on` (MMIO, as the other devices
  are). When the guest frees a page, the guest kernel reports it and QEMU
  discards it from the memfd, so the host stops charging it to runed's cgroup.
  Without the device, a page that the guest touched stays charged until QEMU
  exits. Only freed pages return (JS garbage collection, deleted tmpfs files):
  the guest page cache stays resident, and virtiofsd runs `--cache=always`.
  The pinned kernel needs `CONFIG_VIRTIO_BALLOON=y` and `CONFIG_PAGE_REPORTING=y`.
  Both come from Firecracker's base config; `kernel/arizuko.fragment` names
  neither, so a base-config bump can remove them without a failed build. The
  size of the saving is not measured: compare `Peak guest memory` of the same
  turn before and after. A page reported while virtiofsd maps the same memfd
  is not yet tested under load; the boot tests pass with the device.
- **A pty works, and it rests on an INHERITED config value.** The guest mounts
  `devpts` at `/dev/pts`, because a pty is allocated by opening `/dev/ptmx` and
  then the numbered slave, and without the mount that second open is `ENOENT`.
  Measured 2026-09-24 in the BUILT artifact rather than read off the fragment:
  `kernel.config` carries `CONFIG_UNIX98_PTYS=y` and `CONFIG_TTY=y`, inherited
  from Firecracker's base config, and `kernel/arizuko.fragment` names neither. So
  a base-config bump can take ptys away silently. Both are pinned in the fragment
  at the next kernel rebuild — not now, because pinning a value that is already
  set changes the `KERNEL_KEY` and costs the operator 30–60 minutes of both vCPUs
  for no change in the artifact.
- **The tmpfs overlay upper is inside `-m`, and it is capped.** The guest's
  writable layer is RAM, so an agent that unpacks a large dependency tree
  consumes the guest's memory rather than disk. `guest-init` mounts the upper
  with an explicit `size=`, and a write past it is `ENOSPC` inside the guest —
  a failed turn, never a host that reclaims under pressure. **The numbers, set
  2026-09-24: `size=512m` for the upper and `size=16m` for each manifest tmpfs
  entry**, against the 2 GB `RUNED_GUEST_MEM` default and leaving `/dev/shm` the
  gigabyte `runner.go:682` gives it. They are consts in `guest-init`, not
  per-instance tunables, so an instance that raises `-m` does not raise them;
  Open question 4's peak-RSS measurement is what says whether they should move
  onto the command line beside `ARIZUKO_CTL` and `ARIZUKO_MCP`.
- **Disk is one rootfs per release, and a release deletes.** The export is
  8.26 GB uncompressed. Free space is the operator's, not a guard in the build:
  `docker builder prune -f` is the lever and it recovered 4.37 GB on krons
  (`docker system df` reported the layer cache 100 % reclaimable, 2026-09-23),
  taking free space from 5.2 GB to 9.4 GB. `make images` carries no free-space
  check — a build that cannot write fails on the write, which is loud enough. **Never `docker image prune -a` on an
  arizuko host**: it deletes `arizuko-ant` and every spawn then fails. What the
  export DOES own is not growing without bound: it keeps exactly two unpacked
  trees — the running release and one previous, so a rollback needs no
  re-export — and deletes every older one by name.

## The runed deploy topology

runed stays a compose service, generated by `compose/compose.go` as today.
Its service gains what a VMM needs and loses what Docker needed:

- devices: `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun`;
- `cap_add: [NET_ADMIN, SYS_ADMIN]` — taps and the tap rule set; the staging
  tree's bind mounts in runed's own mount namespace;
- `security_opt: [apparmor=unconfined]`, because **the capability is not
  sufficient**: AppArmor's `docker-default` denies `mount(2)` for every container
  whatever its capabilities, so every turn failed to stage its tree with
  `permission denied`. Measured both ways on krons 2026-09-25 — denied under the
  profile, `bind OK` with the same image and the same one capability when it is
  off (BUGS `L371`). Seccomp needs nothing: docker's default profile already
  allows `mount` to a holder of `CAP_SYS_ADMIN`. A custom AppArmor profile that
  permits this one mount is tighter and is the operator's artifact, not the
  generator's;
- **a mount for every host path a turn will show its guest**, at the same
  absolute path, read-only: `HOST_CODEX_DIR` and each root in
  `MOUNT_ALLOWED_ROOTS`. virtiofsd is runed's own child, so a guest can be shown
  only what runed can open — the Docker substrate could name any host path
  because the docker daemon opened it. Every turn in a folder with codex
  credentials failed until the generator wrote these (BUGS `L370`), and the
  in-data-dir half of the same rule is why `hp()` is gone (BUGS `L368`);
- runs as root inside its container so `virtiofsd` can carry guest
  credentials to host files and the bind mounts can be made; `group_add`
  of the docker gid and the `docker.sock` volume go;
- the `arizuko` image carries `qemu-system-x86_64`, the Rust `virtiofsd`, `socat`,
  `iptables`, `iproute2` and the `egress` binary — one `apk add` line plus one
  `COPY`, because Alpine packages the Rust daemon (§"The peer credential"). The
  last two were missing until 2026-09-25 and each cost a deploy: with no
  `iptables` the tap rule set cannot be written, and Alpine's `ip` is BusyBox's
  applet, which has no `tuntap` subcommand at all — so the precondition's
  `LookPath` passed and no guest got an interface (BUGS `L369`). A precondition on
  a binary that has a capable and an incapable build under one name must run the
  subcommand, the way the clamp check greps virtiofsd's `--help`. The
  image's QEMU is **9.0.2**. The selected modern-MMIO ACPI microvm configuration
  passes the native device, control and poweroff checks.

The privilege runed holds does not rise: the docker socket it holds today is
root-equivalent on the host. The privilege runed holds becomes explicit —
three device nodes and two capabilities, listed in one generator.

## The warm pool: not taken, and the gate that reopens it

Each turn starts a fresh guest and discards it after completion. The accepted
budget is ≤ 1.5 s p50 from host QEMU process start to the first decoded ready frame.
The [runed measurements](../../runed/README.md#boot-timing-measures-the-first-ready-frame)
meet this budget. A warm pool requires a separate design decision under
`ARCHITECTURE.md` § Decided, and why (no warm pool).

## What moves into `runed`

arizuko publishes no Go library for KVM sandboxing. The substrate is
**unexported code inside `runed/`**, the shippable set stays `egress`,
`substrate` and `mcpfw` (`tests/components/imports_test.go:28`), and `qemubox`
(`/home/onvos/app/tools/qemubox`, a 35 KB shell script) is the standalone tool.
`Runtime`'s two methods are the whole public seam: nothing outside `runed` names
a guest, a tap or a pid. Six files hold it, one concern each.

- **`runed/vmrecord.go` — the record, the allocator and the reap.** One record
  file per guest under the record root, written before anything starts and
  removed only when every pid it names is confirmed gone. It names **every
  process the turn starts, not only QEMU** — `QemuPid`, `VirtiofsdPids`,
  `EgressPid`, `BridgePid` — plus the tap, its two addresses and the proxy
  port, because the disk scan is the only thing that reaps after a runed crash
  and it reaps only what the record names. The Docker substrate finds a turn's
  second process by container label (`container.SweepEgressProxies`); with four
  host processes and no label, the record IS the label. `listVMRecords` scans the
  root and `liveCIDs` derives the CID set from that scan on every allocation, so
  runed **keeps no index of guests in memory**: the disk and the pid table are
  the one source of truth, and two readers of the same instance see the same set.
  `reapTargets`
  fixes the order §"The turn" step 9 sets, and it signals a pid only when that
  pid's own cmdline carries this turn's token — a record outlives its processes,
  and the kernel hands pid numbers out again.
- **`runed/vmnet.go` — the tap, the rule set and the proxy.** `tapUp` calls
  `ip tuntap add` for one tap per turn, and `tapNet` carves that turn's `/30`
  out of the configured subnet: a guest has no neighbour on its own link and
  both addresses are fixed before it boots, so there is no shared bridge and no
  address discovery. `tapRules` renders the whole set statically, one accept to
  `HostIP:ProxyPort` above the drops, because the proxy is the one matcher for
  names and addresses alike (§"Egress by name, sealed below the kernel").
  `egressArgs` reads `container.EgressRules`, the same translation the Docker
  substrate's proxy gets, so the two substrates cannot disagree about what a
  folder may reach.
- **`runed/vmtree.go` — the staging tree.** `treePlan` is pure, so one `Turn`
  always renders one tree. `teardown` reads `/proc/self/mountinfo` rather than
  the plan, so a disk scan after a crash unmounts a tree whose plan died with
  the process that built it, and `remove` refuses anything the staging step did
  not create — a recursive delete over a live bind would delete the group's own
  files (§"The files: one staging tree, one share").
- **`runed/vmctl.go` — the control channel.** The frame codec, the host-side
  vsock bind, the MCP bridge, and `steer`, which returns false unless the GUEST
  acknowledged the batch (§"Steer and stop"). A bind failure fails the turn:
  runed never moves to another port, because a silent retry turns a denial into
  a guest talking to whoever answered.
- **`runed/qemuhost.go` — what this host can run.** `qemuPreconditions` refuses
  at runed START, naming every missing input at once, rather than per spawn, for
  the reason `container.EnsureRunning` does: an input found missing per spawn is
  a fleet that looks healthy and answers nothing. `checkClamp` refuses while the
  uid clamp is absent, because a guest as uid 0 writes host-root-owned files
  into the folder (§"The peer credential"). `memoryFits` multiplies
  `RUNED_GUEST_MEM` by `MAX_CONCURRENT_CONTAINERS` against this host's
  `MemTotal`, because the guest size and the cap are one setting (§"The host
  budget").
- **`runed/qemu.go` — the machine and the turn.** `qemuArgs` is pure, so a test
  names every value it expects. `qemuProfile` is the substrate declaration —
  `KVM`, `MountedHome`, `Sessions: false`, `EgressDomain` — and `Delivered`
  equals `Declared` or runed does not start, so there is no half configuration
  to report (`6/25` §2).

## Order

The day estimate below is **not accepted until step 0 reports**. Four of this
design's load-bearing numbers are inferred rather than measured — the boot
split (§"The image and the guest kernel"), the peak guest memory
(§"The host budget"), the cost of assembling nine bind targets, and whether the
vsock bridge survives a real guest. A step-1 commitment made before those is a
commitment to an estimate, not to a design.

0. **The bounded prototype, throwaway, in `tmp/_qemuprobe/`.** One shell
   script and one static Go init, no arizuko code touched, on this host's
   `/dev/kvm`. **The kernel stage comes FIRST inside this step**, and that is
   measured, not a preference: the distro kernel ships `VIRTIO_VSOCKETS`,
   `VIRTIO_FS` and `OVERLAY_FS` as modules (`/boot/config-6.1.0-43-amd64`,
   2026-09-24) and the initrd carries `guest-init` and nothing else, so a guest
   on the distro kernel answers `AF_VSOCK` with "address family not supported"
   and can mount neither the share nor the overlay. Only the boot split can be
   read without the pinned kernel, and step 0 has read it (§"The engine").
   The rest, in order, once `bzImage` exists: host first-ready timing and guest
   probe uptimes on both machine types; one virtio-fs share plus nine bind targets, timed;
   one `ctl` round trip over a CID-pinned vsock; a write from the guest as uid 0
   through the single-id namespace, `stat`ed on the host; a power-off with the
   QEMU pid confirmed gone. It answers the boot gate, the uid clamp and the
   reap, and it is deleted after. **2–3 days**, and a miss here changes the
   design rather than the schedule.
1. **Land behind the selector, default `docker`.** `runed/qemu.go`, the
   `container.Run` split, the substrate's six files (§"What moves into
   `runed`"), `guest-init`, the kernel stage, the compose generator's
   device and capability lines, the `virtiofsd` package
   (§"The peer credential"), and the two host-budget guards
   (§"The host budget"): the memory-times-cap refusal at runed start and the
   overlay `size=`, plus the rootfs export in `make images` and its retention
   in `arizuko images <instance> [--prune]`.
   **Retention is an operator command, not a build target**: a tree is
   ~7 GB in an instance's data dir, a turn may have it mounted through
   virtio-fs at that moment, and a delete of a wrong path removes a
   release. `make images` removes nothing. `arizuko images <instance>` lists
   every `rootfs-*` and `kernel-*` tree with its size. `--prune` removes every
   tree that `RUNED_ROOTFS_DIR` and `RUNED_KERNEL_DIR` in the instance `.env`
   do not name. It refuses when either key is unset or names a tree that is not
   in `<data_dir>/images/`, and it removes nothing while a mount exists at or
   under a tree it would remove (every process's `mountinfo`, read as root). It
   touches only direct children of `<data_dir>/images/`, never a symlink. No
   file under `runed/runstore`, `store/` or
   `objstore/` is touched. Tests without a hypervisor cover the record, the
   argument rendering, the frame codec and the tree builder; `make
bench-boot` and the end-to-end turn run on a host with `/dev/kvm`.
   **8–12 days.**
2. **The kernel pipeline and the gate.** `bench-boot` on krons; the number
   goes into the changelog. If it misses 1.5 s, §"The warm pool" decides
   before step 3. **2–4 days.**
3. **Flip krons**: `RUNED_SUBSTRATE=qemu`, one restart, nothing else in that
   deploy. `anteval --smoke` on the VM substrate — the five post-deploy gates
   apply, and a fleet whose agents answer nothing passes the first four.
   **2–3 days, including the `SECURITY.md`, `ARCHITECTURE.md`,
   `EXTENDING.md` and `runed/README.md` edits, the web docs, and the
   migration file with the `MIGRATION_VERSION` bump.**
4. **Delete Docker from the turn path** — **DONE 2026-09-25**, and the soak the
   step asked for was WAIVED by the owner rather than served. The deletion landed
   with the flip on krons in one session (`2a42a02a`, `23925150`, `1a7f1fed`,
   `1e729401`, `244829b3`, `790d4389`), so the first instance to run the VM
   substrate is also the first to have no Docker runtime to fall back to. Rolling
   back is a git revert and a rebuild, not a setting — which is what the soak was
   protecting and is the cost the owner accepted.

   **Five defects were measured on the flip's own path**, four of them invisible
   to every test because no test runs a real guest: the clamp sent a flag
   virtiofsd does not have (`L367`), the image lacked `iptables` and `iproute2`
   (`L369`), the mount table carried translated host paths (`L368`), the operator's
   host paths had no mount into runed (`L370`), and `CAP_SYS_ADMIN` alone cannot
   make a bind mount under docker-default (`L371`). Each was found by pointing the
   new binary at a live instance's own configuration before or during the restart.

   The original list, for the record: `dockerRuntime`,
   `container/network.go`, the container half of `container/egress.go`,
   `EGRESS_IMAGE`, `EGRESS_UPLINK_NETWORK`, `EGRESS_NETWORK_PREFIX`,
   `EGRESS_SUBNET`, and the `docker.sock` and `docker-cli` lines. **2–3
   days.**

   **What the port sheds in the same step.** §"The seam" carries two
   substrates on purpose. One substrate leaves each item below with one
   reader or none, and an unread field on a port is read by the next author
   as a contract:
   - **`Turn.RegisterSteer` stays, and this is a stated trade, not an
     oversight.** It is a function-typed field, which the code baseline calls
     a jump rather than an abstraction, and shedding it looks free because it
     has one assignment (`runed/manager.go:329`) and one call site
     (`runed/docker.go:66-68`). It is not free. Steering is a third operation
     whatever carries it: as a method on `Runtime` it widens the port, which
     needs the maintainer's approval first; as a concrete `steer(handle,
batch)` the manager calls, it makes the manager depend on the production
     type, so `FakeRuntime` and `holdRuntime` stop serving the contract and
     the port stops being a port. The field is the only shape that keeps
     `Runtime` at two methods AND keeps every implementation substitutable.
     What the field costs — one assignment a reader must search for — is
     paid down by naming it here and keeping the assignment count at one:
     a second assignment is a review-blocker.
   - **`Turn.Egress` stays.** The tap rule set names no host, so the
     allowlist is the proxy's argument, and the substrate starts the proxy
     because the listen address is the tap's host side — substrate state
     keyed by `Handle` (§"Egress by name, sealed below the kernel").
   - **`substrate.Docker` goes.** Its one reader is the Docker declaration
     `container/egress.go:96`, which this step deletes, so `substrate.Backend`
     then names `KVM` only. `substrate.ResidentDisk` and
     `substrate.EgressAddress` lose their one reader at step 1 and stay:
     `substrate/` ships alone, so its enums name what any substrate can be,
     not what arizuko calls.
   - **`RUNED_SUBSTRATE` goes.** One legal value is left, and a setting with
     one legal value is a setting that stops applying without saying so.
   - **`container.Runner`, `container.Input`, `container.Output` and
     `container.DockerRunner` are already gone.** Step 1's renderer split
     deletes them with `RunSpec` (§"The seam"), so they are not this step's
     to shed.
   - **`holdRuntime` stays.** It is a run that starts nothing
     (`runed/hold.go:25`), the production executor for `KindHold`, and it
     needs the interface exactly as much as a substrate does. **`FakeRuntime`
     stays** (`runed/runtimes.go:11`) as the test seam `Runtime` exists for.

Total **16–25 engineer-days** including step 0, plus what §"The warm pool" costs
if the gate fails: the pool is a separate costing when and if its trigger fires.

## Acceptance — MET on krons 2026-09-26

The turn that closes it, in the store: `web:krons/content` carries the agent's
reply `VM TURN OK` at `2026-09-26T00:40:48`, and the operator's prompt asked for
exactly that string. Around it, from one turn:

- `booting guest cid=4 tap=arz-77a1-t01 mounts=7`, `egress proxy up
listen=10.97.0.6 guest=10.97.0.5`, and the guest's own probe
  `{"virtiofs":"mounted","root_write":"refused","turn_write":"ok"}` — **the uid
  clamp measured from inside the guest, both directions**.
- `agent self-reported model usage model=claude-haiku… model=claude-opus…` — the
  agent reached the model THROUGH the tap's proxy, with no resolver in the guest.
- `guest exited code=0 duration=29.9s timedOut=false`, and the turn log's exit code
  carries no "(QEMU's…)" qualifier, so the `{exit}` frame arrived.

Eleven defects were measured on this path and each is recorded: `L367`–`L377`. Four
of them (`L372`, `L373`, `L375`, `L377`) contradicted what this spec said, and the
sections above are corrected rather than annotated.

## Acceptance — the criteria, and which of them an artifact answers

Boot timing, live agent delivery and crash recovery have separate acceptance
checks.

- ✅ Host QEMU process start to the first decoded ready frame meets the accepted
  budget. The [runed measurements](../../runed/README.md#boot-timing-measures-the-first-ready-frame)
  contain the alternating-pair results and the passed native behavior checks.
  Production logs the same interval as `boot_ms`. Live rollout and
  guest-observable smoke remain separate checks.
- ⚠ A turn on krons completes end to end over the VM substrate: prompt in,
  `submit_turn` out, `RunResult.ExitCode` from the `{exit}` frame — **met**, see
  the MET record above. `anteval --smoke` green — **not met**: the harness
  cannot observe a guest (BUGS `L379`), so its score says nothing about the
  substrate.
- A guest whose bridge is pinned to CID _N_ cannot reach folder _B_'s socket
  from a guest with CID _M_; the test is a second VM dialing the first VM's
  `mcp` port and being refused at `accept`. **Not met:** the real vsock loopback
  fixture `TestMCPBridgeServesOnlyItsOwnGuestCID` passes, but it uses no second VM.
- A write to every `ro` family from the guest returns `EROFS`; a write to
  `home/` lands on the host as uid 1000.
- From the guest: `curl` to an allowlisted name succeeds through the proxy;
  `curl` to a listed-out name is refused by the proxy; `getent hosts
example.com` fails; a raw `nc` to any address off the tap times out.
- The QEMU code is unexported inside `runed/`, so no other package can reach
  it, and `tests/components/imports_test.go:28` names `egress`, `substrate`
  and `mcpfw`.
- ✅ Nothing under `runed/` or `container/` CALLS docker: no
  `exec.Command("docker"…)`, no docker client import, and
  `compose/compose_test.go:95-98` asserts that no service mounts `docker.sock`
  (all three measured 2026-09-29). The criterion first said "no `docker` string
  remains", which the code contradicts and should: ~19 comments name the Docker
  substrate to record WHY a rule exists, and this project keeps a line that
  documents a past failure. A string ban would have deleted the reasons along
  with the code.
- **runed refuses to start** when `guest_mem × MAX_CONCURRENT_CONTAINERS`
  exceeds the host's `MemTotal`, and the refusal names both settings
  (§"The host budget").
- ❌ **A `SIGKILL` of runed mid-turn leaves nothing running.** Start a turn, kill
  runed with `-9`, restart it, and the disk scan reaps the QEMU process, both
  `virtiofsd`, the `egress` proxy, the tap and the staging tree — the test
  asserts each pid is gone by name, not that the scan returned. **No such test
  exists**: `q.sweep()` has no test caller (measured 2026-09-29), and
  `TestReapReleasesTheCIDWhenOnlyTheTreeIsStuck` covers the record half only.
  The sweep is the one path that runs after a crash, so it is the one with no
  operator watching it.
- **A squatted vsock port fails the turn and never moves.** Bind the port runed
  would take, start a turn, and assert the turn fails with the bind error and
  that runed binds no other port (§"The peer credential").
- **After three releases exactly two unpacked rootfs trees remain**, named by
  version, and the third-oldest is gone (§"The host budget").

## Open questions — each with the measurement that settles it

1. **Which guest tools resolve names locally** and therefore fail without a
   resolver: run `anteval --smoke` on the VM substrate with the proxy-only
   tap and read the failures. If a required tool resolves locally, the
   proxy gains a `/etc/resolv.conf`-facing UDP/53 answerer that returns
   `NXDOMAIN` for a name off the allowlist — the shape `L136` names.
2. **What the Rust `virtiofsd` costs to build and carry.** The clamp needs it
   and three cheaper mechanisms are measured out (§"The peer credential"), so
   the question is its price: the Rust stage in the `arizuko` image, the build
   time per image, and whether `--sandbox=none` with `--translate-uid` lands a
   guest-uid-0 write on the host id. Measure by adding the stage and running the
   acceptance test.
3. **Peak guest memory, which sets `-m` AND the concurrency cap.** Sample
   `docker stats` on krons over seven days for the ant container's peak RSS.
   `-m 4G` is a placeholder and the pair must satisfy
   `guest_mem × MAX_CONCURRENT_CONTAINERS ≤ MemTotal` (§"The host budget"), so
   this measurement decides two settings, not one.
4. **`nftables` versus `iptables` in the Alpine image.** `apk search`
   listed `qemu-system-x86_64`, `virtiofsd`, `socat`; the rule renderer
   uses `iptables` today. Verify the package at the first image build.
5. **Device discovery and poweroff pass together.** The selected ACPI microvm
   configuration on the release image mounts both shares, opens control and MCP
   channels, completes a turn, enforces proxy policy and powers off.
   The [runed measurements](../../runed/README.md#boot-timing-measures-the-first-ready-frame)
   own the paired host-clock results.
6. **PID 1's stdio — ANSWERED 2026-09-24, and it is `/dev/kmsg`.** A guest wrote
   nothing while its own mount syscalls reached the kernel log from the same
   function. `/dev/ttyS0` and `/dev/console` both OPEN and then fail every write
   with `EIO`: a 16550A with no carrier rejects writes unless `CLOCAL` is set, and
   `-serial file:` never asserts the modem lines. `guest-init` writes to
   `/dev/kmsg`, which needs no termios, cannot be silenced by carrier state, and
   lands in the same stream the kernel console already writes to — the turn log —
   so ordering with kernel messages is free. The diagnosis itself came through a
   failed `virtiofs` mount whose SOURCE NAME carried the message, which is the one
   channel that works while the console does not; a guest that cannot say what is
   wrong needs a channel that is not the thing under test.
7. **Natural poweroff passes for both machine variants.** The boot test waits
   for QEMU to exit without host termination after normal and malformed-control
   boots. Runtime cleanup retains its bounded termination path.

## Ties

`ARCHITECTURE.md` § Decided, and why (no warm pool: the trigger and the pool's
consequences) · [`6/24`](24-crackbox-lightest.md) (the VM case of the
proxy, and the docker-only egress env vars step 4 deletes) ·
[`6/25`](25-qm-adoptions.md) §2 (the declared substrate) ·
[`5/P`](../5/P-runed.md) (the envelope) · [`5/14`](../5/14-credentials.md)
(env-profile keys are the only credential in the `{env}` frame; capability
credentials stay brokered) · [`5/33`](../5/33-paths-roles.md) (the grant
decisions the tree renders) · BUGS `L136`, `L137`, `L189`.
