---
status: draft
aka: antbox
---

# Antbox (crackbox-lineage) QEMU Sandboxing for Arizuko Agents

> **Note (2026-04-11):** this is the open "antbox" spec the user wants
> kept around for later. It captures the crackbox→antbox rebrand path
> and the gaps to close. No work scheduled yet — see `bugs.md`
> "antbox decision" entry. Do not start implementation without
> user direction on the six open questions at the bottom.

## 1. Crackbox Summary

Crackbox is a QEMU/KVM VM platform for running Claude safely in isolated
environments. It lives at `/home/onvos/app/crackbox` and is a fork/rename of
the older **Hive** platform (at `/home/onvos/app/hive`). The two projects are
structurally identical — crackbox is Hive with branding cleaned up and
default-deny networking added.

### What it is

A Go daemon (`crackbox`) that:

- Spawns QEMU/KVM virtual machines on-demand
- Provisions them with Alpine Linux 3.21 via cloud-init ISO
- Manages per-VM TAP networking on a host bridge (`br-crackbox`, 10.1.0.1/16)
- Enforces default-deny network filtering per VM via iptables
- Runs a guest agent (`crackbox-agent`) inside each VM at `:11435`
- Exposes an HTTP API at `:49160` and a web dashboard

### QEMU VM lifecycle

1. **Create** — allocate NetIndex + SSHPort, write YAML metadata to disk
2. **Provision** (first start only):
   - Create `disk.qcow2` as qcow2 overlay on shared Alpine base image
   - Generate cloud-init ISO containing: agent binary, SSH keys, credentials,
     init script (`crackbox-init.sh`), skills tarball
3. **Start**:
   - Create TAP interface via `crackbox-tap` script, attach to `br-crackbox`
   - Launch QEMU with `-daemonize`, write PID file
   - Wait for DHCP lease in dnsmasq lease file (30s timeout)
   - Grant full internet for first boot (provisioning); lock down after
     `crackbox-init.sh` signals `/provisioning/complete`
   - Poll agent `/status` endpoint until `200 OK` (60s timeout)
4. **Running** — agent at `http://<vm-ip>:11435` handles config push,
   WebSocket PTY, Claude stream
5. **Stop** — send `system_powerdown` via QEMU monitor socket; SIGKILL on
   timeout; tear down TAP + iptables
6. **Destroy** — soft delete, clean up network rules; disk retained until GC

### QEMU invocation (from `internal/vm/qemu.go`)

```
qemu-system-x86_64
  -machine type=q35,accel=kvm
  -cpu Nehalem,+ssse3,...
  -smp 2
  -m 2G
  -drive disk.qcow2,format=qcow2,if=virtio
  -drive cloud-init.iso,format=raw,if=virtio
  -netdev tap,id=net0,ifname=tap-{vmid[:8]},script=no,downscript=no
  -device virtio-net-pci,netdev=net0,mac=52:54:XX:XX:XX:XX
  -display none
  -serial file:serial.log
  -monitor unix:qemu.mon,server,nowait
  -pidfile qemu.pid
  -daemonize
```

No 9p/virtiofs mounts in the current crackbox implementation (the ARCHITECTURE.md
example shows `-fsdev local ... -device virtio-9p-pci` but the actual `qemu.go`
does not include this — files go in via cloud-init ISO only).

### Network isolation model

- Default: DROP all traffic in `CRACKBOX_VM_FILTER` iptables chain
- Access granted per-VM via:
  - Domains: HTTP proxy at `10.1.0.1:3128` reads VM allowlist from metadata
  - IPs/CIDRs: direct iptables INSERT rules
- First boot: `allow_all` iptables rule, revoked after cloud-init signals completion
- Rules persist in YAML metadata, restored on VM restart

### Guest agent (`crackbox-agent` at `:11435` in VM)

```
GET  /status                   health check (running, configured)
POST /config                   receive credentials push from host
POST /restart                  restart takopi service
POST /provisioning/complete    signal init done (host removes boot internet)
WS   /v1/shell                 WebSocket PTY
WS   /v1/claude-stream         Claude CLI stream
```

### Storage layout

```
/srv/data/crackbox/qemu/
  base/alpine-3.21-x86_64.qcow2    shared base image
  {vm-id}/
    meta                            YAML metadata + allowlist
    disk.qcow2                      overlay disk (10G)
    cloud-init.iso                  regenerated on each start
    serial.log                      console output
    qemu.pid                        QEMU PID
    qemu.mon                        QEMU monitor socket (unix)
```

### Config

TOML config file or env vars. Key vars:

```
ADDRESS=0.0.0.0:49160
DATA_DIR=/srv/data/crackbox
SPOOL_DIR=/srv/spool/crackbox
PROVISION_ADDRESS=10.1.0.1:1789
PROXY_ADDRESS=10.1.0.1:3128
```

### Host requirements

- KVM support (`/dev/kvm`)
- Root or CAP_NET_ADMIN (TAP networking, iptables)
- `qemu-system-x86_64`, `qemu-img`, `genisoimage` installed
- `dnsmasq` running (set up by `crackbox-setup`)
- Bridge `br-crackbox` + iptables `CRACKBOX_VM_FILTER` chain (set up by
  `crackbox-setup`)

---

## 2. Dockbox vs Crackbox Comparison

**Dockbox** (`/home/onvos/app/tools/dockbox`) is a shell script that wraps
`docker run` for interactive Claude Code sessions. It is a developer
convenience tool, not a multi-tenant platform.

| Dimension          | Dockbox                                                | Crackbox                                                          |
| ------------------ | ------------------------------------------------------ | ----------------------------------------------------------------- |
| Purpose            | Personal dev ergonomics                                | Multi-tenant agent isolation                                      |
| Isolation model    | Docker namespace (shared kernel)                       | QEMU/KVM hypervisor (separate kernel)                             |
| Security           | Explicitly NOT a sandbox — full host credential access | Default-deny network, separate kernel                             |
| Network            | Host networking or bridge                              | Per-VM TAP, iptables default-deny                                 |
| Startup time       | ~1-3s (container start)                                | ~15-30s (VM boot + agent ready)                                   |
| Persistence        | Volumes mounted from host                              | Persistent qcow2 overlay disk                                     |
| File access        | `-v` mounts at exact host paths                        | cloud-init ISO on first boot; push via agent `/config` on restart |
| Credentials        | `~/.claude` mounted rw                                 | Baked into cloud-init ISO; pushed via `/config` endpoint          |
| Control plane      | Shell script, no daemon                                | HTTP API + web dashboard                                          |
| Multiple instances | `docker ps` / manual                                   | Managed lifecycle (create/start/stop/archive)                     |
| Config format      | `~/.dockboxrc`, `.dockboxrc` (shell vars)              | TOML config file                                                  |
| Resource limits    | None                                                   | Can add cgroups (not currently wired)                             |

Dockbox is for one developer running one agent at a time, trusting the
agent completely. Crackbox is for running untrusted agents with VM-level
isolation and network control.

---

## 3. Hive and Qant

### Hive

**Hive** (`/home/onvos/app/hive`) is the direct predecessor of crackbox.
The two codebases are structurally identical — same `internal/vm/` package
layout, same agent architecture, same bridge/TAP/dnsmasq setup, same guest
agent at `:11435`. Key differences:

- Hive: `br-hive`, `HIVE_VM_FILTER` chain, `hive-tap`, `hive-setup`
- Crackbox: `br-crackbox`, `CRACKBOX_VM_FILTER` chain, `crackbox-tap`,
  `crackbox-setup`
- Crackbox adds default-deny network filtering; Hive allows full internet
  by default
- Crackbox config.example.toml still references `hive` data/spool dirs
  (stale leftover from the fork)
- The `nanosrv/SPEC.md` in crackbox describes a future evolution where VMs
  get per-user subdomains (`*.alice.hive.io`) and shared Postgres/Redis
  — this is aspirational, not implemented

Hive is used at `REDACTED.cx/hive` (production deployment visible in
`hive/README.md`). Crackbox is the local-sandbox variant.

### Qant

No files containing "qant" were found anywhere under `/home/onvos/app`. The
term does not exist in any Go, TypeScript, Markdown, or TOML file in the
repository tree. It is not a known component of this ecosystem.

---

## 4. Current Arizuko Docker Usage

Arizuko's container system lives in `container/` (`runner.go`, `runtime.go`,
`sidecar.go`).

### What Docker APIs are used

All Docker interaction is through the `docker` CLI subprocess — no Docker SDK,
no Docker API socket. The binary name is the constant `Bin = "docker"`.

Operations used:

- `docker run -i --rm --name <n> [mounts] [envs] <image>` — spawn agent
- `docker stop <n>` — graceful stop on timeout
- `docker ps --filter=name=arizuko- --format={{.Names}}` — list orphans
- `docker stop <n>` — kill orphans (in `CleanupOrphans`)
- Sidecars: `docker run -d --rm --name <n> [limits] [mounts] <image>`
- Sidecars stop: `docker stop <n>`; `docker rm -f <n>` on failure

### Container lifecycle (per agent run)

```
Run(cfg, folders, in Input) Output
  1. Prepare group dir, write .gateway-caps
  2. BuildMounts — construct []VolumeMount list
  3. Start MCP unix socket server (ipc.ServeMCP → router.sock in ipcDir)
  4. buildArgs → docker run -i --rm ...
  5. cmd.Start() — docker starts container, agent-runner entrypoint
  6. Write JSON Input to stdin, close stdin
  7. Read stdout for ARIZUKO_OUTPUT_START/END delimited JSON chunks
     - Each chunk resets idle timeout timer
     - Calls in.OnOutput for streaming
  8. Timeout goroutine: docker stop → cmd.Process.Kill
  9. cmd.Wait() → collect exit code
  10. Stop MCP server, stop sidecars
  11. Write container log to groups/<folder>/logs/
  12. Return Output{status, result, newSessionId}
```

### Volume mounts (from `BuildMounts`)

Each agent container gets these mounts:

| Host path                                     | Container path           | RW                       |
| --------------------------------------------- | ------------------------ | ------------------------ |
| `groups/<folder>/`                            | `/home/node`             | rw                       |
| `<app-dir>/`                                  | `/workspace/self`        | ro                       |
| `groups/<world>/share/`                       | `/workspace/share`       | rw (root only), ro (sub) |
| `ipc/<folder>/`                               | `/workspace/ipc`         | rw                       |
| `groups/`                                     | `/workspace/data/groups` | rw (root group only)     |
| Optional: `cfg.WebDir`                        | `/workspace/web`         | rw                       |
| Optional: additional mounts from group config | various                  | as configured            |

### MCP IPC

- Host creates a unix socket at `ipc/<folder>/gated.sock`
- `ipc.ServeMCP()` listens on it before container start
- Container's `~/.claude/settings.json` is seeded with:
  ```json
  "arizuko": {
    "command": "socat",
    "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/router.sock"]
  }
  ```
- The container accesses the socket at `/workspace/ipc/router.sock` via the
  mounted ipc volume
- `socat STDIO UNIX-CONNECT:...` bridges MCP over stdio to the unix socket
- Host stops the MCP listener after `cmd.Wait()` returns

### Sidecars

Additional Docker containers launched before the main agent run. Each sidecar:

- Gets a named unix socket in `/workspace/ipc/sidecars/<name>.sock`
- Is exposed to the agent via socat MCP bridge in settings.json
- Runs with `--network=none` (or configured net), memory/CPU limits
- Stopped after main container exits

### docker-in-docker concern

The container image (`cfg.Image`) contains the Claude Code agent-runner.
The host Docker daemon is not exposed to the container — the agent does not
spawn Docker containers itself.

---

## 5. Migration Design

### The core problem

Arizuko's agent lifecycle is **single-run**: one message in, one response out,
container starts fresh each time. This is fundamentally different from
crackbox's model where a VM boots once and stays resident for days.

To use crackbox for arizuko agents, there are two possible models:

**Model A: Ephemeral VM per run** — start a VM, run the agent, stop the VM.
This would take 15-30s per message — unacceptable for interactive use.

**Model B: Resident VM per group** — each group gets a persistent VM that
stays running, agents run inside it as processes. The VM is the "container"
that persists between messages. This matches how arizuko actually uses Docker:
the container is ephemeral but the session state (`.claude/`) is on a
persistent volume.

Model B is the viable path. It's essentially what crackbox already supports:
a long-lived Alpine VM with the agent binary + Claude Code installed. The
difference is that arizuko needs to run `claude` CLI as a subprocess inside
the VM, passing the prompt via stdin and reading the output, rather than
running Claude as a persistent service.

### What needs to change

#### A. Abstract the execution backend

Currently `container/runtime.go` hardcodes `const Bin = "docker"` and
`container/runner.go` builds docker-specific CLI args via `buildArgs`. A clean
abstraction would be:

```go
// container/backend.go
type Backend interface {
    // Run executes a one-shot agent process with the given config.
    // Input is written to stdin; output is read from stdout/stderr.
    // Returns when the process exits or timeout fires.
    Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

type RunSpec struct {
    Name    string
    Mounts  []VolumeMount
    Env     []string
    Image   string       // docker: image name; qemu: ignored (uses VM)
    Timeout time.Duration
    Stdin   io.Reader
    Stdout  io.Writer
    Stderr  io.Writer
}
```

The Docker backend wraps the current `buildArgs` + `exec.Command("docker", ...)`.
The QEMU backend talks to crackbox via its HTTP API.

#### B. QEMU backend via crackbox API

The crackbox HTTP API at `:49160` exposes VM lifecycle. The QEMU backend would:

1. **Ensure VM exists** for the group folder — create if not, start if stopped
2. **Execute command in VM** — use one of:
   - `WS /v1/shell` — WebSocket PTY, but complex to drive programmatically
   - SSH to the VM's SSH port — `ssh root@localhost -p <sshPort> claude ...`
   - HTTP POST to a new agent endpoint — requires modifying crackbox-agent
3. **Stream output** back to arizuko's runner loop
4. **Stop VM** eventually — on idle timeout or explicit stop

**OPEN QUESTION**: The right execution primitive inside the VM is not yet
defined. Options:

- `WS /v1/claude-stream` endpoint in crackbox-agent: already exists, designed
  for interactive Claude use. But it runs Claude as a persistent session, not
  as a one-shot stdin→stdout process the way arizuko expects.
- New agent endpoint `POST /v1/run` that accepts JSON (arizuko Input), runs
  `claude` CLI via PTY, streams ARIZUKO_OUTPUT_START/END delimited JSON back.
  This would be the cleanest fit.
- Direct SSH + command execution: sidesteps agent, but credential management
  is messier.

#### C. File mounts: the biggest gap

Arizuko's Docker model relies heavily on bind mounts:

- Group directory at `/home/node` — agent's home, read+write
- Session `.claude/` at `/home/node/.claude` — session state
- App directory at `/workspace/self` (read-only) — skills, CLAUDE.md
- IPC directory at `/workspace/ipc` — MCP socket lives here

**In a QEMU VM, bind mounts do not exist**. The VM has its own filesystem.
Options:

| Option                                      | Description                                                      | Complexity                                                                                                                                                  |
| ------------------------------------------- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **SFTP/SCP on boot**                        | Copy group dir into VM before each run, copy back after          | High — two-way sync, large data                                                                                                                             |
| **9p/virtio-fs mount**                      | Mount host directory into VM via VirtFS                          | Medium — requires `-fsdev`/`-device virtio-9p-pci` in QEMU args and `mount -t 9p` inside VM. Already shown in crackbox ARCHITECTURE.md (commented example). |
| **NFS mount**                               | Host exports group dir via NFS; VM mounts it                     | Medium — needs NFS server on host                                                                                                                           |
| **Sidecar volume container**                | Docker volume container accessible to VM                         | Does not apply cleanly                                                                                                                                      |
| **Resident VM with home already populated** | VM's `/root/` IS the persistent group dir; managed by agent push | Low for writes, but loses fine-grained host control                                                                                                         |

**9p/virtio-fs is the natural fit** for arizuko's multi-mount model. QEMU
supports it natively. The host side needs no extra services. The crackbox
ARCHITECTURE.md already notes `virtio-9p-pci` as part of a planned QEMU
configuration. The main cost: 9p has non-trivial latency and requires the
`9p` kernel module in the Alpine guest (it's available but may need explicit
loading).

**OPEN QUESTION**: virtio-9p security model. 9p with `security_model=none`
means the VM has access to all files the QEMU process can read. If the group
dir contains credentials or other groups' files, this needs careful scoping —
mount only the specific group subdirectories, not the whole data dir.

#### D. MCP IPC: unix socket across VM boundary

The MCP unix socket (`router.sock`) is the most architecturally critical IPC
path. Currently:

1. Host creates unix socket at `ipc/<folder>/gated.sock`
2. Container mounts `ipc/<folder>/` at `/workspace/ipc`
3. Agent in container connects via `socat STDIO UNIX-CONNECT:/workspace/ipc/router.sock`

**In a VM, the socket does not cross the VM boundary automatically.** Options:

| Option                    | Description                                                                                                                                                                                                                            |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **socat TCP bridge**      | Host: `socat UNIX-LISTEN:router.sock TCP-LISTEN:PORT`; VM: `socat STDIO TCP:10.1.0.1:PORT`. No socket sharing needed.                                                                                                                  |
| **virtio-9p mount**       | Mount ipc dir via 9p into VM — unix sockets can be created/connected over 9p on Linux if the underlying filesystem supports it. **OPEN QUESTION**: does virtio-9p support unix socket creation and connection? This is not guaranteed. |
| **Modify crackbox-agent** | Add a proxy endpoint to crackbox-agent that forwards MCP calls to host API directly. Avoids the socket problem entirely.                                                                                                               |

**socat TCP bridge is the simplest path** that definitely works. The host
would listen on a per-group TCP port; the VM would connect via the bridge IP
`10.1.0.1`. The agent's settings.json socat args would change from
`UNIX-CONNECT:/workspace/ipc/router.sock` to `TCP:10.1.0.1:<port>`.

Downside: requires port allocation per group (similar to how crackbox already
allocates SSH ports). This is tractable.

#### E. What stays the same

- Input JSON schema (the `Input` struct) — same, just delivered differently
- Output parsing (ARIZUKO_OUTPUT_START/END markers) — same, agent-runner uses them
- Session state in `.claude/` — persisted in VM filesystem or via 9p mount
- Skills seeding — can be done via cloud-init ISO on VM creation
- Sidecar containers — sidecars could remain as Docker containers on host if
  the VM connects out to them via TCP, or be replaced with processes inside the VM
- Group-level isolation — each group maps to one VM; isolation is now VM-level
  rather than container-level

### Migration surface in `container/`

| File                | Change needed                                                          |
| ------------------- | ---------------------------------------------------------------------- |
| `runtime.go`        | Extract to `DockerBackend`; add `QemuBackend` that wraps crackbox API  |
| `runner.go`         | Remove `buildArgs` / `docker run` call; use Backend interface          |
| `runner.go`         | MCP socket setup: add TCP-to-unix bridge for QEMU path                 |
| `runner.go`         | Mount setup: replace `-v` flags with 9p mount specs for QEMU path      |
| `sidecar.go`        | Sidecars remain Docker for now; may stay separate from VM              |
| `container_test.go` | Tests mock the docker CLI; would need updating for backend abstraction |

### Suggested migration path (phased)

**Phase 1**: Extract a `Backend` interface in `runtime.go`. Docker
implementation wraps existing code. No behavior change.

**Phase 2**: Implement a crackbox HTTP client library. Thin wrapper around
crackbox's REST API for VM lifecycle.

**Phase 3**: Implement `QemuBackend.Run()` using SSH + new crackbox-agent
endpoint. Start with a new `POST /v1/run` in crackbox-agent that accepts
the arizuko `Input` JSON and returns streamed output. This avoids
virtio-9p complexity initially — pass the prompt and session state
over HTTP, have the VM write outputs to stdout.

**Phase 4**: Switch group state to virtio-9p mounts. VM boots with group dir
mounted; agent reads/writes as normal. Eliminates the state-copy overhead.

**Phase 5**: MCP bridge via socat TCP. Per-group port allocation, host
listens, VM connects. Agent settings.json updated by crackbox-agent at
container start.

---

## 6. Open Questions

1. **Execution primitive**: What is the right way to run `claude` CLI inside a
   crackbox VM and stream output back? Options: new `/v1/run` HTTP endpoint
   in crackbox-agent, WebSocket claude-stream, or direct SSH. The
   `/v1/claude-stream` endpoint exists but is designed for interactive use,
   not arizuko's one-shot stdin→stdout model.

2. **virtio-9p unix socket support**: Can unix domain sockets be created and
   connected over a virtio-9p mount? If yes, the MCP socket could be shared
   directly without a TCP bridge. Needs testing.

3. **VM startup latency per group**: First message to a group with no running
   VM will take ~30s (VM boot + agent ready). Is a warm-up strategy needed
   (pre-boot on first group message received, keep alive for X minutes)?

4. **Sidecar architecture**: Sidecars are currently Docker containers on the
   host. In a QEMU model, should they be processes inside the VM, separate
   Docker containers connected via TCP, or something else? Docker on host
   connecting to VM via TCP port is the path of least resistance.

5. **Session state migration**: If a group currently has Docker-resident
   session state in `groups/<folder>/.claude/`, how does it transfer to
   the VM filesystem on first QEMU run?

6. **crackbox host requirements vs arizuko host**: Arizuko runs in Docker
   itself (via `docker compose up`). Crackbox requires root-level TAP/iptables
   setup. Running crackbox inside a Docker container is non-trivial (requires
   `--privileged`, `/dev/kvm`, CAP_NET_ADMIN). The arizuko deployment model
   would need to change for the host that runs crackbox VMs.

7. **Multiple arizuko instances vs one crackbox**: Arizuko supports multiple
   named instances (each with their own data dir and groups). Crackbox is a
   single instance with a flat VM namespace. How does VM naming/isolation work
   when multiple arizuko instances share one crackbox host?

8. **Qant**: The term "qant" does not appear anywhere in the codebase. It is
   not a known component. The user may be referring to something not yet
   built, or a misremembered name. Clarify before proceeding.

---

## Appendix: File Paths

- Crackbox source: `/home/onvos/app/crackbox/`
- Hive (predecessor): `/home/onvos/app/hive/`
- Dockbox: `/home/onvos/app/tools/dockbox/`
- Arizuko container package: `/home/onvos/app/arizuko/container/`
- Crackbox VM core: `/home/onvos/app/crackbox/internal/vm/`
- Crackbox guest agent: `/home/onvos/app/crackbox/agent/cmd/agent/main.go`
- Crackbox init script: `/home/onvos/app/crackbox/crackbox-init.sh`
- Arizuko IPC/MCP: `/home/onvos/app/arizuko/ipc/ipc.go`
