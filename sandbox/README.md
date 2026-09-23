# sandbox

The KVM/qemu lifecycle: create a VM, start it, exec into it over SSH, list,
stop, destroy. Spec
[`specs/6/41`](../specs/6/41-qemu-substrate.md).

**This is runed's substrate, not a shippable library.** `qemubox` is the
standalone KVM tool and it lives outside this repo. `6/41` step 1 folds this
package into `runed/` as unexported code and rewrites the qemu argument list
for `-M microvm`; until that step runs, `runed` spawns docker containers and
nothing in the spawn path imports this.

## Coverage

Everything a test can reach without a hypervisor is covered: `New`'s
validation and instance-id namespacing, `List` on a fresh host, the not-found
paths of `Stop` and `Exec`, `LibexecDir`'s override-first search, the on-disk
VM record, `detectState`/`readPID`, `Destroy`'s soft-delete branch, and
`netfilter.go`'s rule rendering.

Coverage stops around 20% because everything below `Start` needs a real
hypervisor: `qemu-system-x86_64`, a KVM device node, the `libexec` helpers, and
`ip`/`nft` to build the guest network. A mock of qemu would assert the mock.
That half runs only on a host with `/dev/kvm`; `6/41` §Acceptance names the
end-to-end turn and `make bench-boot` as the gates that cover it.

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

The VM case needs no registration: a guest has exactly one virtual NIC, the
tap the host created, so the tap IS the identity. Nothing has to answer "which
tenant is this address?", and a rule attached to the tap dies with the tap.

`6/41` §"Egress by name, sealed below the kernel" changes what enforces a
name: the tap rule set becomes static — accept guest to the host-side proxy,
drop everything else — and the proxy matches names and addresses alike. That
closes `L189` by construction, because a name never reaches netfilter.

## Requirements

qemu, KVM, and root (tap devices, netfilter). The helper scripts live in
`libexec/` and are found via `CRACKBOX_LIBEXEC`,
`/usr/local/libexec/crackbox`, or `/usr/libexec/crackbox`
(`internal/libexec.go:26-27`). Nothing installs them today, so the search
falls back to walking up from the source file's compile-time path until it
finds `libexec/crackbox-cloudinit` (`internal/libexec.go:14-16`).

## Layout

```
sandbox/
  sandbox.go     the public API: New, Spawn, Exec, List, Stop
  internal/      qemu invocation, tap + netfilter, cloud-init, metadata
  libexec/       cloudinit + tap helper scripts
```
