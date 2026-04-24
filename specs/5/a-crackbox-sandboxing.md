---
status: planned
phase: next
aka: antbox
---

# Antbox — QEMU sandboxing via crackbox

> 2026-04-24: Planned as next-phase work. Not yet scheduled but
> committed in direction. Docker remains the current backend until
> this ships.

Replace Docker with QEMU/KVM VMs via crackbox
(`/home/onvos/app/crackbox`). Resident VM per group (ephemeral ~30s
boot unacceptable); agents run as processes inside. Extract `Backend`
interface; Docker = current, QEMU = new via crackbox HTTP API. 9p/
virtio-fs for file mounts, socat TCP bridge for MCP across VM
boundary.

Rationale: separate kernel isolation + default-deny per-VM networking
over shared-kernel Docker. Crackbox already has most of this.

No work scheduled. Memory note: "Sandbox (dormant). crackbox
researched as antbox candidate. Decision never made — arizuko still
uses Docker."

Unblockers: execution primitive (new `POST /v1/run` on crackbox-agent
vs existing `/v1/claude-stream`), unix socket over 9p, VM cold-start
latency (pre-boot pool?), host requirements (root, /dev/kvm,
CAP_NET_ADMIN), multi-instance namespace.
