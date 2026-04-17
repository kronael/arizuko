---
status: draft
aka: antbox
---

# Antbox (crackbox-lineage) QEMU Sandboxing for Arizuko Agents

> No work scheduled. Do not start implementation without user direction
> on the open questions at the bottom.

## Concept

Replace Docker containers with QEMU/KVM VMs for agent isolation.
Crackbox (`/home/onvos/app/crackbox`) is a Go daemon that spawns
on-demand Alpine Linux VMs with default-deny networking and a guest
agent at `:11435`.

Key properties: separate kernel (not shared like Docker), per-VM TAP
networking with iptables default-deny, HTTP proxy for domain-level
allowlists, persistent qcow2 overlay disks.

## Crackbox vs Docker (arizuko today)

| Dimension     | Docker (current)      | QEMU/Crackbox            |
| ------------- | --------------------- | ------------------------ |
| Isolation     | Shared kernel         | Separate kernel          |
| Network       | Host/bridge           | Per-VM TAP, default-deny |
| Startup       | ~1-3s                 | ~15-30s                  |
| File access   | Bind mounts           | 9p/virtio-fs or ISO      |
| Credentials   | JSON on stdin         | cloud-init ISO + /config |
| Control plane | docker CLI subprocess | HTTP API + web dashboard |

## Viable model: Resident VM per group

Ephemeral VM per run (~30s boot) is unacceptable. Instead: each group
gets a persistent VM that stays running. Agents run as processes inside
it. The VM persists between messages; session state lives on the VM
filesystem or via 9p mount.

## Key migration gaps

**Backend abstraction**: extract `Backend` interface from
`container/runtime.go`. Docker wraps existing code; QEMU backend talks
to crackbox HTTP API.

**File mounts**: Docker bind mounts don't exist in VMs. **9p/virtio-fs**
is the natural fit — QEMU supports it natively, no extra host services.
Crackbox ARCHITECTURE.md already notes `virtio-9p-pci` as planned.

**MCP IPC across VM boundary**: unix sockets don't cross VM boundaries.
**socat TCP bridge** is the simplest path: host listens on per-group TCP
port, VM connects via bridge IP `10.1.0.1`. Port allocation similar to
how crackbox already allocates SSH ports.

**Execution primitive**: need a new `POST /v1/run` endpoint in
crackbox-agent that accepts arizuko Input JSON, runs `claude` CLI,
streams ARIZUKO_OUTPUT markers back. Existing `/v1/claude-stream` is
designed for interactive use, not one-shot stdin->stdout.

## Migration phases

1. Extract `Backend` interface in `container/runtime.go` (no behavior change)
2. Implement crackbox HTTP client library
3. `QemuBackend.Run()` via new `/v1/run` agent endpoint
4. Switch group state to 9p mounts
5. MCP bridge via socat TCP

## Open questions

1. **Execution primitive**: `/v1/run` (new), `/v1/claude-stream` (existing
   but interactive), or direct SSH?

2. **virtio-9p unix socket support**: can unix domain sockets work over
   9p mounts? If yes, MCP socket shared directly without TCP bridge.

3. **VM startup latency**: first message to a group with no running VM
   takes ~30s. Pre-boot strategy needed?

4. **Sidecar architecture**: keep as Docker containers on host connected
   via TCP (path of least resistance), or move into VM?

5. **Host requirements**: crackbox needs root, `/dev/kvm`,
   `CAP_NET_ADMIN`. Arizuko currently runs in Docker via compose.
   Deployment model changes for the crackbox host.

6. **Multi-instance**: arizuko supports multiple named instances;
   crackbox has a flat VM namespace. VM naming/isolation across
   instances is undefined.
