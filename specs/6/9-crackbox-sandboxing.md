---
status: shipped
shipped: 2026-05-01
aka: antbox
---

# sandbox — KVM sandboxing library + per-spawn egress

> A Go library for KVM/qemu sandbox lifecycle (`sandbox/`), shipped
> alongside the per-spawn egress proxy
> ([`6/24`](24-crackbox-lightest.md)). It is a library, not a daemon.
> The KVM backend is **not wired into runed yet** — `runed` still
> spawns via Docker.

## Status

`Shipped` (2026-05-01) as a library. `sandbox/` has the public API
`New(instanceID, dataDir)` + `(*Host).Spawn/Exec/Stop/List`,
system-state-backed `List()` (disk scan), InstanceID namespacing, and
SSH-based `Exec`. `crackbox/pkg/run` is its one consumer; no arizuko
daemon imports it (see "Wiring gap").

## Architecture

`runed` owns container spawn behind the `Runtime` interface — the
ContainerRuntime seam (`runed/runtime.go`, spec [`5/P`](../5/P-runed.md)).
Production wraps `container.DockerRunner`. KVM would land as one more
backend behind that same seam, importing `sandbox/`:

```
runed.Runtime (runed/runtime.go)
  ├── DockerRunner   → docker run                 [today]
  └── (KVM backend)  → import arizuko/sandbox  [not wired yet]
                       ├── spawn KVM VM
                       ├── manage privileges (/dev/kvm, CAP_NET_ADMIN)
                       ├── attach to per-VM network
                       └── register with the egress proxy
```

Crackbox is **a library**. The egress proxy ships as the `egress`
binary runed starts per spawn ([`6/24`](24-crackbox-lightest.md)), its own
process. A consumer imports `arizuko/sandbox` and
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
    run/                 — `crackbox run` orchestration (network + spawn-and-wait)
```

`sandbox/` public surface (`sandbox/sandbox.go`):

```go
package sandbox

type VMConfig struct {
    Image     string   // path to base qcow2; empty = default Alpine download
    Memory    string   // e.g. "2G"; empty = default
    CPUs      int      // 0 = default (2)
    Mounts    []Mount  // virtio-fs mounts
    AllowList []string // host allowlist enforced by netfilter on the VM's tap
}

type Handle struct{ ID string; IP string }

func New(instanceID, dataDir string) (*Host, error)
func (h *Host) Spawn(cfg VMConfig) (Handle, error)
func (h *Host) Exec(h Handle, cmd []string, stdin io.Reader) (int, []byte, []byte, error)
func (h *Host) Stop(h Handle) error
func (h *Host) List() ([]Handle, error)                                        // running VMs in this namespace
```

Five fields, and the fifth is the whole egress story: no proxy URL, because
there is no proxy to address. See "Egress enforcement".

The library holds **no in-RAM index of VMs**. Source of truth is the
system: per-VM metadata files on disk plus live process/network state.
`List()` scans the metadata dir and runs `detectState()` on each entry —
every caller, in every process, sees the same set. A warm-VM pool is
not implemented.

## Egress enforcement

`VMConfig.AllowList []string` is the whole surface, and enforcement is
netfilter rules on the VM's own tap device (`sandbox/internal/manager.go`).
The gate is in the kernel, on the interface the VM's packets must cross, so
a VM cannot be outside it.

There is no admin API, no `/v1/register`, and nothing for a caller to hold
open across a spawn's life. That is the point rather than an omission: an
allowlist held by a REGISTRATION means a crashed consumer leaves an
allowlisted IP registered forever, which is the leak
[`6/24`](24-crackbox-lightest.md) §"It removes a real leak" was written to
delete. A rule attached to the tap dies with the tap.

## Privileges

Caller must have:

- `/dev/kvm` accessible (kvm group or root)
- `CAP_NET_ADMIN` for tap/bridge setup
- writable scratch dir for VM disk overlays

The library acquires no privileges itself; it expects to run with what
it needs. The consumer daemon (runed, once the KVM backend is wired) is
the user-facing privilege boundary.

## Wiring gap

`sandbox/` is shipped and tested but **no arizuko daemon imports it** —
`grep -rn arizuko/sandbox runed/` is empty. Today's
arizuko egress path is Docker-only: one shared `crackbox proxy serve`
per instance, with per-spawn register/unregister of the routd-resolved
allowlist (`runed/docker.go`, spec [`5/P`](../5/P-runed.md),
[`SECURITY.md`](../../SECURITY.md)). Landing KVM means adding a
`Runtime` backend that calls `sandbox/`; image management
(`EnsureImage`), the resident-VM pool, and MCP-over-vsock into the VM
are unbuilt.

## Acceptance

- `make -C crackbox build && make -C crackbox test` passes on a host
  with no arizuko process and no arizuko data directory.
- `sandbox/` has zero arizuko-internal imports (component test, per
  [`6/16`](16-daemon-standalone-matrix.md)).
- A spawn with a non-empty `AllowList` reaches a listed host and is
  refused a listed-out one, with the VM's tap rules as the evidence —
  not a registration the caller has to remember to undo.
