---
status: shipped
date: 2026-05-01
aka: antbox
---

# Crackbox — sandboxing library + egress proxy

> Crackbox is a Go library for KVM/qemu sandbox lifecycle
> (`crackbox/pkg/host/`), shipped alongside the egress-proxy daemon
> ([`6/8`](8-crackbox-standalone.md)). It is a library, not a daemon.
> The KVM backend is **not wired into runed yet** — `runed` still
> spawns via Docker.

## Status

`Shipped` (2026-05-01) as a library. `crackbox/pkg/host/` has the
public API `New(instanceID, dataDir)` + `(*Host).Spawn/Exec/Stop/List`,
system-state-backed `List()` (disk scan + `detectState()`), InstanceID
namespacing, and SSH-based `Exec`. No arizuko daemon imports it yet
(see "Wiring gap").

## Architecture

`runed` owns container spawn behind the `Runtime` interface — the
ContainerRuntime seam (`runed/runtime.go`, spec [`5/P`](../5/P-runed.md)).
Production wraps `container.DockerRunner`. KVM would land as one more
backend behind that same seam, importing `crackbox/pkg/host/`:

```
runed.Runtime (runed/runtime.go)
  ├── DockerRunner   → docker run                 [today]
  └── (KVM backend)  → import crackbox/pkg/host/  [not wired yet]
                       ├── spawn KVM VM
                       ├── manage privileges (/dev/kvm, CAP_NET_ADMIN)
                       ├── attach to per-VM network
                       └── register with the egress proxy
```

Crackbox is **a library**. The egress proxy ships as
`crackbox proxy serve` ([`6/8`](8-crackbox-standalone.md)), its own
process. A consumer imports `crackbox/pkg/host/` and
`crackbox/pkg/client/` directly — no intermediate daemon.

## Components in the crackbox repo

```
crackbox/
  cmd/crackbox/main.go   — the single CLI: `proxy serve`, `run`, `state`
  pkg/
    host/                — VM-spawn library (the focus of this spec)
    proxy/               — egress-proxy internals
    match/               — Host(allowlist, host) bool
    admin/               — proxy admin API server
    client/              — HTTP client for the admin API
    config/              — TOML loader
    dns/                 — DNS filter ([`6/10`](10-crackbox-dns-filter.md))
    run/                 — `crackbox run` orchestration (network + spawn-and-wait)
```

`pkg/host/` public surface (`crackbox/pkg/host/host.go`):

```go
package host

type VMConfig struct {
    Image       string   // path to base qcow2; empty = default
    Memory      string   // e.g. "2G"
    CPUs        int
    Mounts      []Mount  // virtio-fs mounts
    EgressProxy string   // proxy admin URL; empty = no proxy
    AllowList   []string // pushed to the proxy on Spawn
}

type Handle struct{ ID string; IP string }

func New(instanceID, dataDir string) (*Host, error)
func (h *Host) Spawn(cfg VMConfig) (Handle, error)                              // boot VM, register with proxy
func (h *Host) Exec(h Handle, cmd []string, stdin io.Reader) (int, []byte, []byte, error)
func (h *Host) Stop(h Handle) error                                            // shutdown VM, unregister
func (h *Host) List() ([]Handle, error)                                        // running VMs in this namespace
```

The library holds **no in-RAM index of VMs**. Source of truth is the
system: per-VM metadata files on disk plus live process/network state.
`List()` scans the metadata dir and runs `detectState()` on each entry —
every caller, in every process, sees the same set. A warm-VM pool is
not implemented.

## Egress-proxy registration

`Spawn` reads the `EgressProxy` field:

- Empty → no proxy is configured (the VM gets no allowlist gate).
- Non-empty → health-check that admin URL, then POST
  `/v1/register {ip, id, allowlist}`. `Stop` POSTs `/v1/unregister`
  (best-effort).

There is no auto-spawn of a local proxy from `pkg/host/`; the caller
supplies a running proxy URL. The shared-proxy deployment (one
`crackbox proxy serve` across many spawns) is described in
[`6/8`](8-crackbox-standalone.md).

## Privileges

Caller must have:

- `/dev/kvm` accessible (kvm group or root)
- `CAP_NET_ADMIN` for tap/bridge setup
- writable scratch dir for VM disk overlays

The library acquires no privileges itself; it expects to run with what
it needs. The consumer daemon (runed, once the KVM backend is wired) is
the user-facing privilege boundary.

## Wiring gap

`crackbox/pkg/host/` is shipped and tested but **no arizuko daemon
imports it** — `grep -rn crackbox/pkg/host runed/` is empty. Today's
arizuko egress path is Docker-only: one shared `crackbox proxy serve`
per instance, with per-spawn register/unregister of the routd-resolved
allowlist (`runed/docker.go`, spec [`5/P`](../5/P-runed.md),
[`SECURITY.md`](../../SECURITY.md)). Landing KVM means adding a
`Runtime` backend that calls `pkg/host/`; image management
(`EnsureImage`), the resident-VM pool, and MCP-over-vsock into the VM
are unbuilt.

## Acceptance

- `make -C crackbox build && make -C crackbox test` passes on a host
  with no arizuko process and no arizuko data directory.
- `pkg/host/` has zero arizuko-internal imports (component test, per
  [`6/16`](16-daemon-standalone-matrix.md)).
- External-proxy mode: pre-run `crackbox proxy serve`; a `pkg/host/`
  spawn with a non-empty `EgressProxy` registers on Spawn and
  unregisters on Stop against it.
