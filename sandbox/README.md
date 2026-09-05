# sandbox

The KVM/qemu sandbox lifecycle library: create a VM, start it, exec into it
over SSH, list, stop, destroy. Spec
[`specs/6/9`](../specs/6/9-crackbox-sandboxing.md).

Moved out of `crackbox/pkg/host` on 2026-08-18 (spec
[`6/24`](../specs/6/24-crackbox-lightest.md)). It lived next to a proxy it
never linked; one directory claimed to be one component and was two.

**Not wired into arizuko.** `runed` spawns docker containers; nothing in the
spawn path imports this. Its consumer today is `crackbox run --kvm`.

## Coverage

Everything a test can reach without a hypervisor is covered: `New`'s
validation and instance-id namespacing, `List` on a fresh host, the not-found
paths of `Stop` and `Exec`, `LibexecDir`'s override-first search, the on-disk
VM record, `detectState`/`readPID`, `Destroy`'s soft-delete branch, and
`netfilter.go`'s rule rendering.

Coverage stops around 20% because everything below `Start` needs a real
hypervisor: `qemu-system-x86_64`, a KVM device node, the `libexec` helpers, and
`ip`/`nft` to build the guest network. A mock of qemu would assert the mock.
That half is exercised by `crackbox run --kvm` on a host that has KVM.

## Use

```go
h, err := sandbox.New("myinstance", "/var/lib/sandbox")
handle, err := h.Spawn(sandbox.VMConfig{
    AllowList: []string{"github.com"},
    Mounts:    []sandbox.Mount{{Host: "/src", Guest: "/src", ReadOnly: true}},
})
code, stdout, stderr, err := h.Exec(handle, []string{"uname", "-a"}, nil)
err = h.Stop(handle)
```

`instanceID` must match `^[a-zA-Z0-9_-]{1,32}$` and namespaces every VM's
state under `<dataDir>/<instanceID>/vms/<id>/`.

## Egress

`AllowList` becomes netfilter rules on the VM's own tap device. An entry that
is an IP address or a CIDR becomes a rule; a host name becomes nothing
(`internal/netfilter.go` writes a rule only for what
[`egress/match.LooksLikeIP`](../egress/match/) accepts). That is why
`Profile.Egress` in `sandbox.go` is `address` where the proxy's is `domain`
— the declaration is the statement of record, and BUGS `L189` holds the open
question of whether host names should be enforced here at all.

The VM case needs no proxy and no registration: a guest has exactly one
virtual NIC, the tap the host created, so the tap IS the identity. Nothing
has to answer "which tenant is this address?".

Until 2026-08-18 `Spawn` also registered the VM with a shared proxy's admin
API and `Stop` unregistered it — inheriting that design's leak, where a
crash between VM exit and unregister left a stale allowlist bound to an
address the next VM could get. The registration is gone; the netfilter rules
were always the enforcement.

## Requirements

qemu, KVM, and root (tap devices, netfilter). The helper scripts live in
`libexec/` and are found via `CRACKBOX_LIBEXEC`,
`/usr/local/libexec/crackbox`, or `/usr/libexec/crackbox` — `make -C
crackbox install` puts them there.

## Layout

```
sandbox/
  sandbox.go     the public API: New, Spawn, Exec, List, Stop
  internal/      qemu invocation, tap + netfilter, cloud-init, metadata
  libexec/       cloudinit + tap helper scripts
```
