---
status: shipped
shipped: 2026-05-01
aka: antbox
---

# sandbox — KVM sandboxing library + per-spawn egress

> A Go library for KVM/qemu sandbox lifecycle (`sandbox/`), shipped
> alongside the per-spawn egress proxy
> ([`6/24`](24-crackbox-lightest.md)). It is a library, not a daemon.

## Status

`Shipped` (2026-05-01) as a library. `sandbox/` has the public API
`New(instanceID, dataDir)` + `(*Host).Spawn/Exec/Stop/List`,
system-state-backed `List()` (disk scan), InstanceID namespacing, and
SSH-based `Exec`. `crackbox/pkg/run` is its one consumer; no arizuko
daemon imports it.

## Architecture

`runed` owns container spawn behind the `Runtime` interface — the
ContainerRuntime seam (`runed/runtime.go`, spec [`5/P`](../5/P-runed.md)).
Production wraps `container.DockerRunner`.

The egress proxy ships as the `egress` binary runed starts per spawn
([`6/24`](24-crackbox-lightest.md)), its own process. A consumer imports
`arizuko/sandbox` directly — no intermediate daemon.

`sandbox/` public surface (`sandbox/sandbox.go`):

```go
package sandbox

type VMConfig struct {
    Image     string   // path to base qcow2; empty = default Alpine download
    Memory    string   // e.g. "2G"; empty = default
    CPUs      int      // 0 = default (2)
    Mounts    []Mount  // virtio-fs mounts
    AllowList []string // egress allowlist; granularity: sandbox.Profile.Egress
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
every caller, in every process, sees the same set.

## Egress enforcement

`VMConfig.AllowList []string` is the whole surface. At what granularity a
listed entry becomes a rule is declared once, as a value:
`sandbox.Profile.Egress` (`substrate/substrate.go`, owned by
[`6/25`](25-qm-adoptions.md) §2). This spec restates none of it.

The gate is netfilter on the VM's own tap device
(`sandbox/internal/manager.go`): in the kernel, on the interface the VM's
packets must cross, so a VM cannot be outside it.

There is no admin API, no `/v1/register`, and nothing for a caller to hold
open across a spawn's life. That is the point rather than an omission: an
allowlist held by a REGISTRATION means a crashed consumer leaves an
allowlisted IP registered forever, which is the leak
[`6/24`](24-crackbox-lightest.md) §"Why per-spawn" describes. A rule
attached to the tap dies with the tap.

## Privileges

Caller must have:

- `/dev/kvm` accessible (kvm group or root)
- `CAP_NET_ADMIN` for tap/bridge setup
- writable scratch dir for VM disk overlays

The library acquires no privileges itself; it expects to run with what
it needs. The consumer daemon is the user-facing privilege boundary.

## Acceptance

- `make -C crackbox build && make -C crackbox test` passes on a host
  with no arizuko process and no arizuko data directory.
- `sandbox/` has zero arizuko-internal imports (component test, per
  [`6/16`](16-daemon-standalone-matrix.md)).
- A spawn with a non-empty `AllowList` reaches an entry
  `sandbox.Profile.Egress` covers and is refused one outside it, with the
  VM's tap rules as the evidence — not a registration the caller has to
  remember to undo.
