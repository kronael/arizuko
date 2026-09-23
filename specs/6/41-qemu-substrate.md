---
status: draft
depends:
  [
    20-sandbox-lifecycle-decline,
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
supports.** runed boots the release's guest kernel on `-M microvm`, exports the
turn's files through one virtio-fs share, and reaches the guest over vsock.
Docker leaves the turn path: no `docker run`, no per-folder docker network, no
proxy container, no `docker.sock` in runed. The daemon fleet's packaging
(compose) is outside this spec.

The turn model of [`5/A`](../5/A-primitives-framing.md) holds unchanged: the guest is
stateless, the folder and the database carry every fact across turns, and the
container mount table in `ARCHITECTURE.md` §"Container mount layout" is the
contract the agent sees — the same left column, delivered by a different
right-hand side. `ant/` changes by zero lines.

## What the design must answer — measured 2026-09-12 on hel1v5 (the krons host)

These are constraints, not a verdict. A design that does not meet the first row
is not shipped.

| Constraint                     | Measured                                                                                                                                                                                                                      |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Boot budget                    | Docker `run --rm --entrypoint /bin/true arizuko-ant:latest`: **0.62 / 0.82 / 0.93 s** wall. The VM turn must reach `init` in **≤ 1.0 s p50** on the same host, measured the same way (§"The warm pool").                      |
| Untuned QEMU, distro kernel    | `-M q35,accel=kvm` direct kernel boot, Debian 6.1.0-43 kernel, static Go init: **2.43 / 2.69 / 2.89 s** wall, 0.7–0.8 s guest uptime at init. `-M microvm`: **3.75–4.06 s**, 1.9–2.2 s guest uptime.                          |
| The VMM itself                 | `qemu-system-x86_64 -M microvm -S` start and quit: **0.09–0.13 s**. The cost is the guest kernel, not QEMU.                                                                                                                   |
| Tuned QEMU, literature         | Firecracker's NSDI'20 paper §5.1: Linux 4.14, no modules, qboot, 1 vCPU/256 MB — QEMU boots within ~50 ms of end-to-end Firecracker; pre-configured Firecracker p99 146 ms (usenix.org/system/files/nsdi20-paper-agache.pdf). |
| Turn volume                    | krons: 162 turns in 7 days, 15 in the last 24 h (`container-*.log` count under `groups/`). Boot cost is per-turn latency the user sees, not throughput.                                                                       |
| Host                           | 2 vCPU, 15 GB RAM, QEMU 7.2.22 (Debian 12), `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun` present; kernel ships `fs/fuse/virtiofs.ko` and the vsock transports; `socat 1.7.4.4` built `WITH_VSOCK`.                          |
| QEMU 7.2 devices               | `-M microvm`, `vhost-user-fs-device`, `vhost-vsock-device`, `memory-backend-memfd`, `-sandbox on` all present (`-M help`, `-device help`, `-object help`).                                                                    |
| Image                          | `arizuko-ant:latest` is **8.26 GB** (`node:22-slim` + ~40 tool installs, `ant/Dockerfile`). The rootfs pipeline moves this once per release, never per turn.                                                                  |
| Packages the runed image needs | Alpine carries `qemu-system-x86_64`, `virtiofsd`, `socat` (`apk search` from `arizuko:latest`, 2026-09-12). `nftables`/`iptables` verified at build, §Open.                                                                   |

## The engine: `qemu-system-x86_64`, owned by arizuko

The VMM is the distro's QEMU binary, driven by `exec.Command` from `runed/`
— the same process shape the turn path has today (`container/runtime.go:17`
`const Bin = "docker"`), with a different binary. Three engines were read; one
is taken.

- **QEMU (taken).** Packaged on every Debian and Alpine host arizuko runs
  on; arizuko already drives it by `exec.Command`
  (`sandbox/internal/qemu.go:94`); `-M microvm` is "a minimalist machine type
  without PCI nor ACPI support, designed for short-lived guests … optimized
  for both boot time and footprint"
  (qemu.org/docs/master/system/i386/microvm.html). The guest kernel is
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

**Reopen trigger, one:** if the guest-kernel pipeline cannot deliver the boot
budget on krons within its costed budget (§"The image and the guest kernel"),
the engine question reopens for libkrun, whose bundled kernel is the thing the
pipeline would have failed to produce.

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
   `Handle` the Manager pinned.
2. Builds the turn's staging tree (§"The files") and starts two `virtiofsd`
   processes: `root` over the release's unpacked rootfs, read-only, and
   `turn` over the staging tree.
3. Brings up the tap with its rule set and starts the `egress` proxy on the
   tap's host side (§"Egress").
4. Binds the `ctl` and `mcp` listeners on `AF_VSOCK` (`unix.SockaddrVM`,
   `golang.org/x/sys/unix`), each accepting only a peer whose CID is this
   VM's.
5. Starts QEMU: `-M microvm -enable-kvm -cpu host -smp 2 -m 4G` with
   `-object memory-backend-memfd,share=on` (virtio-fs requires the shared
   backend: virtio-fs.gitlab.io/howto-qemu.html), `-kernel`/`-initrd` from
   the release, two `vhost-user-fs-device`s, one `vhost-vsock-device` with
   the CID, one `virtio-net-device` on the tap, `-serial file:<turn log>`,
   `-sandbox on`, `-pidfile`, `-no-reboot`. Four virtio-mmio devices of the
   eight microvm allows.
6. The guest's init (`guest-init`, a static Go binary in the initrd) mounts
   `root` read-only with a tmpfs overlay upper (a virtio-fs share as
   overlayfs **lower** is supported; as upper it is not —
   github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-run-docker-with-kata.md),
   mounts `turn`, bind-mounts each family onto its canonical path, mounts a
   1 GiB `/dev/shm` (the `--shm-size=1g` of `runner.go:682`), dials
   `ctl`, and receives one JSON frame `{env, stdin}`: `Turn.Env` and
   `Turn.Stdin`.
7. `guest-init` starts `socat UNIX-LISTEN:/run/ipc/gated.sock,fork
VSOCK-CONNECT:2:<mcp>` on a guest-local tmpfs and execs ant as uid 1000
   with the environment and the envelope on stdin. `settings.json`'s MCP
   entry (`runner.go:912-915`) and `ant/src/mcp.ts:3` see the same path.
8. Tool calls: ant → `gated.sock` → socat → vsock → runed's `mcp` bridge →
   routd's `gated.sock`. Steer and stop arrive on `ctl` (§"Steer and stop").
   ant's stderr streams over `ctl` as `{log}` frames into the turn log.
9. ant exits; `guest-init` sends `{exit: code}` on `ctl` and powers the
   guest off; QEMU exits; runed reaps in order: tap rules, tap, the two
   `virtiofsd` (they exit with their socket), the staging tree, the record.
   `ctx` expiry is `SIGKILL` on the QEMU pid, then the same reap.

`RunResult.ExitCode` is the `{exit}` frame; `Outcome` and `NewSessionID`
arrive by `submit_turn` over MCP, unchanged.

## The mechanisms, one by one

| Docker fact today                                                                                        | QEMU answer                                                                                                                                                                                            |
| -------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `docker run -i --rm --name … --shm-size=1g` (`runner.go:680-682`)                                        | one QEMU process per turn (§"The turn" step 5); `/dev/shm` sized by `guest-init`                                                                                                                       |
| nine mount families as `-v` (`runner.go:539-669`, `741-744`)                                             | one staging tree, one `turn` share (§"The files")                                                                                                                                                      |
| `-e KEY=VALUE` and the stdin pipe, from `Turn.Env` and `Turn.Stdin`                                      | the `{env, stdin}` frame on `ctl`; nothing touches the kernel command line or a file                                                                                                                   |
| `--user uid:gid` when the host uid is not 1000 (`runner.go:731-737`)                                     | the guest agent is always uid 1000; `virtiofsd` carries the guest's credentials to the host, `--translate-uid` where a data dir is owned otherwise (§Open)                                             |
| routd's socket visible through the bind-mounted `ipc/` dir (`ipc/ipc.go:361-363`, `runner.go:912-915`)   | vsock, CID-pinned, bridged by runed (§"The peer credential"); a unix socket does not cross virtio-fs — a bind-mounted socket answers "Operation not supported" (github.com/abiosoft/colima/issues/997) |
| SO_PEERCRED uid check (`ipc/ipc.go:397-410`)                                                             | unchanged in routd; the bridge child runs as uid 1000 and the guest is bound by CID (§"The peer credential")                                                                                           |
| `docker kill --signal=SIGUSR1` (`runed/docker.go:47-49`, `runner.go:378`)                                | a `{steer}` frame on `ctl` (§"Steer and stop")                                                                                                                                                         |
| stop → `docker kill` → `rm -f` (`runner.go:288-299`, `runed/docker.go:56-58`)                            | `{stop}` frame → `SIGTERM` to ant → power-off; `SIGKILL` QEMU on the deadline; reap                                                                                                                    |
| `docker network create --internal` + `--ip` (`container/network.go:157-163`, `runner.go:724-725`)        | one tap per turn; the guest's only route is the host side of its tap (§"Egress")                                                                                                                       |
| the egress proxy as a sidecar container on two networks (`container/egress.go:238-274`)                  | the `egress` binary as a process on the tap's host side, `--client <guest ip>`; `EGRESS_IMAGE`, `EGRESS_UPLINK_NETWORK`, `EGRESS_NETWORK_PREFIX/_SUBNET` retire                                        |
| `docker ps --filter=name=arizuko-` orphan sweep (`container/runtime.go:138`)                             | runed's own disk scan: every on-disk VM record, plus a check of the recorded pid (§"What moves into `runed`")                                                                                          |
| `docker info` liveness (`container/runtime.go:124-131`)                                                  | `/dev/kvm` opens and `qemu-system-x86_64 -version` runs, checked once at runed start, refused loudly                                                                                                   |
| stderr over the CLI's pipe, `docker logs` unused (`runner.go:245-253`)                                   | `{log}` frames on `ctl`; kernel console on `-serial file:` into the same `groups/<folder>/logs/container-<ts>.log`                                                                                     |
| session copy is host-FS (`runner.go:1330-1360`); `docker cp`/`exec` unused                               | unchanged                                                                                                                                                                                              |
| runed in a container with `/var/run/docker.sock` (`compose/compose.go:1115-1118`)                        | §"The runed deploy topology"                                                                                                                                                                           |
| `arizuko-ant` built by `docker build`, distributed by being built on the deploy box (`Makefile:147-161`) | built the same way, then unpacked once per release to a host directory (§"The image and the guest kernel")                                                                                             |

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

Why this is at least as hard to forge as the mount: both links are host-kernel
facts a guest cannot influence and a host process needs root to change. What
moves is where the folder binding is stated — from a Docker mount set fixed at
`docker create` to a CID fixed at QEMU start and a bridge whose dial target is
fixed at fork. The threat the check exists for — an agent in folder A reaching
folder B's tools (`SECURITY.md` §Model) — needs either B's CID (the host kernel
denies) or A's bridge dialing B's socket (the bridge holds one path).

What it does not defend: a host process that binds a vsock port before runed
does receives that guest's traffic. The guest holds no secret, so the outcome
is a failed turn, not a leak — the same trust the operator's host already
carries. Whether a compose container can bind host vsock ports at all is an
open question with a measurement (§Open).

## Steer and stop

The steer is a write to `/run/ipc/input` plus a signal (`runed/docker.go:202-214`,
`ant/src/index.ts:39,93`). In the VM, `/run/ipc` is guest-local, so both halves
travel on `ctl`: runed sends `{steer: batch}`; `guest-init` writes the batch
into the guest's `/run/ipc/input/` and sends `SIGUSR1` to ant's pid. The
closure `RegisterSteer` hands back returns false when `ctl` is closed — the
same documented race, the same fall-through to a fresh spawn.

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
  matches them), so the allow-by-address rules go
  (`sandbox/internal/netfilter.go:77` `allowIP`), and `L189` closes by
  construction: a name never reaches netfilter, and the proxy is the one
  matcher for names and addresses alike.
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
a share.

## The image and the guest kernel

**A release is still an image.** `ant/Dockerfile` builds `arizuko-ant` as it
does today; the turn substrate consumes it as a directory. `make images`
gains one step: `docker create` + `docker export` of the image into
`<data>/images/ant-<version>/rootfs/`, once per release on the deploy host,
and `arizuko run` (initial setup) does the same. The 8.26 GB moves once. The
`root` share is that directory; nothing per turn copies or converts it.

**The guest kernel is part of the image.** A `kernel` stage in `ant/Dockerfile`
builds a pinned Linux 6.1 from Firecracker's
`microvm-kernel-ci-x86_64-6.1.config`
(github.com/firecracker-microvm/firecracker/tree/main/resources/guest_configs)
plus arizuko's fragment: `VIRTIO_FS`, `FUSE_FS`, `VIRTIO_VSOCKETS`,
`OVERLAY_FS`, `VIRTIO_MMIO`, `VIRTIO_NET`, built in, no modules. The stage
writes `vmlinux` and the initrd — `guest-init` and nothing else — to
`/opt/arizuko/guest/` inside the image, so they come out of the same export
and QEMU reads them from the unpacked directory. The paper's kernel is 4.0 MB
compressed with no modules against a distro kernel's 6.7 MB plus 44 MB of
modules, and its command line "disables the standard logging to the serial
console (saving up to 70ms of boot time)" (NSDI'20 §5.1); the fragment and
the command line follow it.

Owner: arizuko, in `ant/`. Cost: **2–4 engineer-days** for the stage, the
fragment and the bench target, plus the kernel build time on every image
build. The gate it must pass is the boot budget in the first table.

## The runed deploy topology

runed stays a compose service, generated by `compose/compose.go` as today.
Its service gains what a VMM needs and loses what Docker needed:

- devices: `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun`;
- `cap_add: [NET_ADMIN, SYS_ADMIN]` — taps and the tap rule set; the staging
  tree's bind mounts in runed's own mount namespace;
- runs as root inside its container so `virtiofsd` can carry guest
  credentials to host files and the bind mounts can be made; `group_add`
  of the docker gid and the `docker.sock` volume go;
- the `arizuko` image carries `qemu-system-x86_64`, `virtiofsd`, `socat`
  and the `egress` binary; `docker-cli` (`Dockerfile:14`) goes.

The privilege runed holds does not rise: the docker socket it holds today is
root-equivalent on the host. The privilege runed holds becomes explicit —
three device nodes and two capabilities, listed in one generator.

## The warm pool: not taken, and the gate that reopens it

[`6/20`](20-sandbox-lifecycle-decline.md) declines the warm pool and names
its reopen trigger: "a runtime whose boot cost is genuinely turn-dominant
…". This spec's runtime is that case. The untuned measurement (2.43–2.89 s
q35, 3.75–4.06 s microvm, against 0.62–0.93 s) trips it. The design's answer
is **the kernel pipeline, not the pool**, and it is conditional on a number:

- `make bench-boot` runs the measurement from the first table — QEMU
  process start to `guest-init`'s first frame, ten runs, on the deploy host
  — and the result is written into the release's `CHANGELOG.md` entry.
- **≤ 1.0 s p50 on krons keeps the pool declined.** The literature puts a
  purpose-built kernel on QEMU within ~50 ms of Firecracker's class; the
  bare VMM costs ~0.1 s here.
- **Above 1.0 s after the pipeline lands, `6/20` reopens for this runtime,
  and the pool comes as one unit with its consequences**, none inherited
  quietly: the **claim seam** (a pooled guest boots with `root` only and is
  bound to a folder afterwards, which means attaching `turn`, the tap and the
  CID-pinned bridge to a running guest — `microvm` devices are configured at
  start (qemu.org microvm doc), so a pool forces `q35` and PCI hot-plug,
  which is unverified on QEMU 7.2 and would be measured first); the
  **idle policy** (a claimed guest that outlives a turn is stopped, not
  paused — nothing may accumulate across turns, `6/20` §"Ephemerality");
  the **reaper** (a bounded pool size, an age cap, and the disk scan as the
  observed state the reconcile function reads). `6/20`'s two surviving
  shapes — the one-boolean I/O gate and the total desired×observed plan —
  are the shapes it would take.

## What moves into `runed`

arizuko publishes no Go library for KVM sandboxing. `qemubox`
(`/home/onvos/app/tools/qemubox`, a 35 KB shell script) is the standalone
tool, and the shippable set is `egress`, `substrate` and `mcpfw`
(`tests/components/imports_test.go:28`). The mechanisms below become
**unexported code inside `runed/`**, under the names §"The turn" uses. A
mechanism that no step of §"The turn" calls is deleted, not carried.
Measured 2026-09-23:

**Moves.**

- The disk-scan reconcile: `List()` over the record directory, `detectState`
  on the recorded pid, `readPID` (`sandbox/internal/manager.go:235`,
  `sandbox/internal/metadata.go:65,76`). It answers the orphan sweep of
  §"The mechanisms", and it keeps the property that makes it worth moving:
  runed **keeps no index of guests in memory**, so the disk and the pid table
  are the one source of truth, and two readers of the same instance see the
  same set.
- The on-disk VM record: one file per guest under
  `<dataDir>/<instance>/vms/<id>` (`sandbox/internal/metadata.go:16-29,30`),
  which is the record §"The turn" step 1 writes. Its fields become
  `{Handle, CID, CtlPort, McpPort, Pid, TurnDir}`; `SSHPort`, `SSHKeys`,
  `SSHKeyPath`, `AllowList` and `AllowAll` go with the mechanisms that wrote
  them.
- The tap helper: `sandbox/libexec/crackbox-tap` and its callers
  (`sandbox/internal/network.go:41-84`), cut to one tap per turn. The shared
  bridge (`network.go:24` `BridgeName`) and the DHCP address discovery
  (`network.go:86` `discoverVMIP`) go: §"Egress" gives each guest a `/30`
  with no neighbour and both addresses fixed.
- The netfilter renderer's skeleton: the insert-if-absent and delete helpers
  (`sandbox/internal/netfilter.go:104,117`), which render the static
  proxy-only rule set of §"Egress".
- The qemu exec shape: `exec.Command("qemu-system-x86_64", …)`, the pid-file
  wait and the stop path (`sandbox/internal/qemu.go:94,107,122`).
- The substrate declaration: `Profile` (`sandbox/sandbox.go:28-33`) becomes
  runed's, and reads `substrate.KVM`, `MountedHome` (§"The files"),
  `Sessions: false` and `EgressDomain` (§"Egress").

**Rewritten.** The QEMU argument list (`sandbox/internal/qemu.go:77-92` —
`q35`, a qcow2 disk, a cloud-init ISO, `virtio-net-pci`, `-daemonize`)
becomes the microvm line of §"The turn" step 5. `VMConfig`
(`sandbox/sandbox.go:52-58`) becomes
`{Rootfs, Kernel, Initrd, TurnDir, CID, Ports, TapRules}`: `Mounts` is
declared (`sandbox/sandbox.go:45,56`) and never read
(`sandbox/internal/manager.go:82-90` builds `Meta` without it, and the tree
holds no `vhost-user-fs` flag), and `AllowList` leaves because the proxy
holds it. The per-address accept rules
(`sandbox/internal/netfilter.go:48-88`) become the static proxy-only set.

**Deleted.** `sandbox/internal/cloudinit.go` and
`sandbox/libexec/crackbox-cloudinit`: `guest-init` receives `{env, stdin}` on
`ctl`. The Alpine download (`sandbox/internal/qemu.go:15-46`, the URL at
`sandbox/internal/vm.go:49-53`): the rootfs and the kernel come from the
release image (§"The image and the guest kernel"). SSH `Exec`
(`sandbox/sandbox.go:136,153`) and the ssh-key fields: `Profile.Sessions` is
false, so a workload is one process from boot to power-off. The exported
ceiling `New(instanceID, dataDir)`, `Spawn`, `Stop`, `List` and `Exec`
(`sandbox/sandbox.go:75,91,123,136,210`): runed calls its own code, so
`Runtime`'s two methods are the whole public seam.

## Order

1. **Land behind the selector, default `docker`.** `runed/qemu.go`, the
   `container.Run` split, the fold of `sandbox/` into `runed/` (§"What moves
   into `runed`"), `guest-init`, the kernel stage, the compose generator's
   device and capability lines. No file under `runed/runstore`, `store/` or
   `objstore/` is touched. Tests without a hypervisor cover the record, the
   argument rendering, the frame codec and the tree builder; `make
bench-boot` and the end-to-end turn run on a host with `/dev/kvm`.
   **8–12 days.**
2. **The kernel pipeline and the gate.** `bench-boot` on krons; the number
   goes into the changelog. If it misses 1.0 s, §"The warm pool" decides
   before step 3. **2–4 days.**
3. **Flip krons**: `RUNED_SUBSTRATE=qemu`, one restart, nothing else in that
   deploy. `anteval --smoke` on the VM substrate — the five post-deploy gates
   apply, and a fleet whose agents answer nothing passes the first four.
   **2–3 days, including the `SECURITY.md`, `ARCHITECTURE.md`,
   `EXTENDING.md` and `runed/README.md` edits, the web docs, and the
   migration file with the `MIGRATION_VERSION` bump.**
4. **Delete Docker from the turn path**, after fourteen green days on krons
   and a green smoke on a second instance: `dockerRuntime`,
   `container/network.go`, the container half of `container/egress.go`,
   `EGRESS_IMAGE`, `EGRESS_UPLINK_NETWORK`, `EGRESS_NETWORK_PREFIX`,
   `EGRESS_SUBNET`, and the `docker.sock` and `docker-cli` lines. **2–3
   days.**

   **What the port sheds in the same step.** §"The seam" carries two
   substrates on purpose. One substrate leaves each item below with one
   reader or none, and an unread field on a port is read by the next author
   as a contract:
   - **`Turn.RegisterSteer` goes.** It is the one field a substrate
     interprets rather than delivers, against the `Turn` comment above it,
     and it holds one assignment (`runed/manager.go:329`) and one call site
     (`runed/docker.go:66-68`). The manager calls the production substrate's
     own `steer(handle, batch string) bool` instead, so every destination is
     a named method; `Runtime` keeps two methods because `steer` is not one
     of them. `Manager.steerFns` and `SetSteer` go with it
     (`runed/manager.go:45,406`), and `ActiveSpawn`
     (`runed/runstore/spawns.go:76-79`) gains the handle its own row already
     stores (`spawns.go:392`).
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

Total **14–22 engineer-days**, plus what §"The warm pool" costs if the gate
fails: the pool is a separate costing when and if its trigger fires.

## Acceptance

- `make bench-boot` on krons reports ≤ 1.0 s p50 to `guest-init`'s first
  frame, and the number is in the changelog.
- A turn on krons completes end to end over the VM substrate: prompt in,
  `submit_turn` out, `RunResult.ExitCode` from the `{exit}` frame,
  `anteval --smoke` green.
- A guest whose bridge is pinned to CID _N_ cannot reach folder _B_'s socket
  from a guest with CID _M_; the test is a second VM dialing the first VM's
  `mcp` port and being refused at `accept`.
- A write to every `ro` family from the guest returns `EROFS`; a write to
  `home/` lands on the host as uid 1000.
- From the guest: `curl` to an allowlisted name succeeds through the proxy;
  `curl` to a listed-out name is refused by the proxy; `getent hosts
example.com` fails; a raw `nc` to any address off the tap times out.
- The QEMU code is unexported inside `runed/`, so no other package can reach
  it, and `tests/components/imports_test.go:28` names `egress`, `substrate`
  and `mcpfw`.
- No `docker` string remains under `runed/` or `container/` after step 4, and
  `compose/compose_test.go:95-98` asserts that no service mounts
  `docker.sock`.

## Open questions — each with the measurement that settles it

1. **Which guest tools resolve names locally** and therefore fail without a
   resolver: run `anteval --smoke` on the VM substrate with the proxy-only
   tap and read the failures. If a required tool resolves locally, the
   proxy gains a `/etc/resolv.conf`-facing UDP/53 answerer that returns
   `NXDOMAIN` for a name off the allowlist — the shape `L136` names.
2. **Guest credentials on host files.** Write from the guest as uid 1000
   with `virtiofsd --sandbox=chroot` as root and `stat` the host file; if it
   is not uid 1000, `--translate-uid`/`--translate-gid` (virtiofsd README)
   is the fix and the compose line carries it.
3. **Is `AF_VSOCK` reachable from another compose container?** From a
   sibling container, `socat VSOCK-LISTEN:5555` then a guest `VSOCK-CONNECT
2:5555`. If it connects, the port squat is a local-host denial only, as
   §"The peer credential" states; the spec then records it as accepted.
4. **Peak guest memory.** Sample `docker stats` on krons over seven days for
   the ant container's peak RSS; `-m 4G` is the placeholder and the memfd
   backend commits pages on touch.
5. **`nftables` versus `iptables` in the Alpine image.** `apk search`
   listed `qemu-system-x86_64`, `virtiofsd`, `socat`; the rule renderer
   uses `iptables` today. Verify the package at the first image build.
6. **Boot with the pipeline's kernel on krons** — the whole gate; nothing
   in this spec assumes the number.

## Ties

[`6/20`](20-sandbox-lifecycle-decline.md) (the trigger and the pool's
consequences) · [`6/24`](24-crackbox-lightest.md) (the VM case of the
proxy, and the docker-only egress env vars step 4 deletes) ·
[`6/25`](25-qm-adoptions.md) §2 (the declared substrate) ·
[`5/P`](../5/P-runed.md) (the envelope) · [`5/14`](../5/14-credentials.md)
(env-profile keys are the only credential in the `{env}` frame; capability
credentials stay brokered) · [`5/33`](../5/33-paths-roles.md) (the grant
decisions the tree renders) · BUGS `L136`, `L137`, `L189`.
