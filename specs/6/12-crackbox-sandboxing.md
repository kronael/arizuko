---
status: planned
aka: antbox
---

# Crackbox — sandboxing library + bundled egred proxy

> Crackbox is a Go library for KVM/qemu sandbox lifecycle, plus the
> egred egress-proxy daemon shipped alongside it. Not a daemon
> itself. Imported directly by `gated` via a Backend interface
> (Docker today, crackbox/pkg/host/ next). Sandd extraction
> ([8/c](c-sandd.md)) deferred.

## Status

`Planned`. The shipped v0.32.x crackbox is the **proxy half only**
(now named [`egred`](../6/9-crackbox-standalone.md)). The
sandboxing-library half is the next phase. This spec describes the
target shape.

The original prototype at `/home/onvos/app/crackbox/` already
implements KVM isolation + per-VM egress proxy + secrets injection.
Reuse the prototype's VM-spawn code; do not rewrite.

## Architecture

```
gated (keeps spawn ownership; sandd deferred per 8/c)
  ├── backend: docker  → docker run                            [today]
  └── backend: crackbox → import crackbox/pkg/host/            [next]
                          ├── spawn KVM VM
                          ├── manage privileges (/dev/kvm, CAP_NET_ADMIN)
                          ├── attach to per-VM network
                          └── ensure egred is running
                                ├── start one if not present
                                └── OR use external one (env hint)
```

Crackbox is **a library**, not a daemon. The `egred` daemon ships in
the same component (`crackbox/cmd/egred/`) but is its own process,
runnable standalone or auto-managed by the library. `gated` imports
`crackbox/pkg/host/` and `crackbox/pkg/client/` directly — no
intermediate daemon.

## Components in the crackbox repo

```
crackbox/
  cmd/
    crackbox/main.go    — CLI: `crackbox run --kvm`, `crackbox state`
                           (one-shot user-facing entrypoint)
    egred/main.go       — proxy daemon binary
                           (long-running, deployable standalone)
  pkg/
    host/               — VM-spawn library (NEW; the focus of this spec)
                          imported by sandd, by `crackbox run --kvm`,
                          by anything else that wants a KVM sandbox
    proxy/              — egred internals; imported by cmd/egred/
    match/              — Host(allowlist, host) bool
    admin/              — egred admin API server
    client/             — HTTP client for egred admin API
    config/             — TOML loader
    run/                — `crackbox run` orchestration (network +
                          spawn-and-wait)
```

`pkg/host/` is the new addition. Public surface:

```go
package host

type VMConfig struct {
    Image       string
    Memory      string  // "2G"
    CPUs        int
    Mounts      []Mount  // host:guest, ro/rw
    Network     string  // existing network or empty for new
    EgressProxy string  // empty = ensure local egred; URL = use external
    AllowList   []string
}

type Handle struct{ ID string; IP string }

func Spawn(VMConfig) (Handle, error)        // boot VM, register with egred
func Exec(h Handle, cmd []string, stdin io.Reader) (exitCode int, stdout, stderr []byte, error)
func Stop(h Handle) error                    // shutdown VM, unregister
func List() []Handle                         // all running VMs on this host
```

The library holds **no in-RAM index of VMs**. Source of truth is the
system: per-VM metadata files on disk (`<datadir>/vms/<id>/meta.yaml`)
plus live process/network state (pidfile, qemu PID, tap interface,
DHCP lease). `List()` scans the metadata dir and runs `detectState()`
on each entry — every caller, in every process, sees the same set.
Pool management (warm VMs across many spawns, eviction policy) lives
in `sandd`, not here.

## Egred lifecycle (managed by the library)

When `host.Spawn` is called:

1. Check the `EgressProxy` field:
   - Empty → look for a local egred at the conventional address
     (`http://127.0.0.1:3129` or via `EGRED_ADMIN` env). If one is up,
     use it. Otherwise spawn one (in-process subprocess or
     `cmd/egred/` binary), wait for `/health`, use it.
   - Non-empty → use that URL; never spawn one. (Operator pre-runs
     egred at deployment scale.)
2. POST `/v1/register {ip, id, allowlist}` to the egred admin API.
3. Boot VM with `HTTPS_PROXY=http://<egred-host>:3128`.

`Stop` reverses: POST `/v1/unregister`, shut VM down, and (if this
process spawned the local egred) tear it down too.

The "external egred" mode is for arizuko-style deployments where one
egred container is shared across hundreds of agent spawns — paying the
egred boot cost once is materially cheaper than per-spawn.

## Privileges

Caller must have:

- `/dev/kvm` accessible (kvm group or root)
- `CAP_NET_ADMIN` for tap/bridge setup
- writable scratch dir for VM disk overlays

`sandd` is the user-facing privilege boundary; this library doesn't
acquire privileges itself, just expects to run with what it needs.

## Reuse from origin prototype — do not rewrite

The prototype at `/home/onvos/app/crackbox/` is **the source of truth**
for the sandbox half. It works. The point of this spec is to bring
it into the arizuko monorepo as `crackbox/pkg/host/` with minimal
adaptation, not to redesign or rewrite. Files to port:

- `internal/vm/launch.go` — qemu-system-x86_64 invocation, virtio-net
  setup, virtio-fs mount plumbing
- `internal/vm/network.go` — bridge + tap + iptables NAT
- `internal/vm/proxy.go` — already ported into `crackbox/pkg/proxy/`
  during the v0.31.0 egred extraction
- `internal/vm/secrets.go` — secrets injection at proxy (planned for
  [6/11](../6/11-crackbox-secrets.md))

The acceptance test for the port: same Go code, new package paths.
Diff against the prototype should be ~import-path renames + small
glue for the `pkg/host/` public API (`Spawn` / `Exec` / `Stop` /
`List`). If the diff grows beyond mechanical changes, stop and
revisit — we're rewriting instead of reusing.

Concrete delta the port DOES need:

- New `pkg/host/host.go` with the public API; everything else is
  unexported under `pkg/host/internal/...` matching the prototype's
  layout.
- Egred lifecycle hook (auto-spawn or external-detect) — the
  prototype doesn't have this in its current shape because it
  bundled its proxy inline; we now have a standalone `egred`
  binary, so the hook decides which to use.

Resolve the unblockers below in the port pass; do not wait for a
v2.

## Unblockers

- **Cold-start latency.** ~30s qemu boot is too slow for interactive
  chat. Resident VM per group is the pattern (one VM lives across
  many agent runs; `Exec()` runs the agent process inside). Pool
  management is in `sandd`, not here, but the library must support
  `Spawn` returning a handle that subsequent `Exec`s can target.
- **Multi-instance namespace.** When two arizuko instances share a
  host, their VMs must not collide on bridge names, IP ranges, or
  shared egred. Library accepts an `InstanceID` for namespacing.
- **MCP across VM boundary.** `gated.sock` is a unix socket on the
  host; agent inside VM needs to reach it. socat TCP bridge over
  virtio-vsock or 9p (decision in implementation; spec accepts
  either).
- **Image management.** Base VM image stored on host; per-VM overlay
  via qcow2. `pkg/host/` provides `EnsureImage(url) string` to
  download + cache.

## Out of scope for this spec

- The pool of resident VMs (lives in [`sandd`](c-sandd.md))
- Crackbox-specific operator CLI beyond `crackbox run --kvm`
- Cluster / multi-host VM placement
- VM live-migration

## Acceptance

- `crackbox run --kvm --allow github.com -- curl https://github.com`
  works on a host with `/dev/kvm`. The CLI uses `pkg/host/` directly.
- `sandd` with `SAND_BACKEND=crackbox` spawns agents in VMs that
  reach `api.anthropic.com` but get 403 on anything else.
- `pkg/host/` has zero arizuko-internal imports. Same orthogonality
  test as [8/b](b-orthogonal-components.md).
- External-egred mode: pre-run `egred` as a separate container; `pkg/host/`
  detects it via `EGRED_ADMIN` env and skips the auto-spawn.
