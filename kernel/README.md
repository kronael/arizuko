# kernel

The guest kernel and initrd every turn boots. `runed` starts one KVM guest per
turn from these two files (`runed/README.md` § The guest's turn, step by step).

## What `make kernel` builds

`make kernel DATA_DIR=<instance data dir>` writes three files to
`<data>/images/kernel-<key>/`:

- `bzImage`, Linux `KVER` (6.1.112), LZ4-compressed;
- `initrd.gz`, holding `guest-init` as `/init` and the device nodes the turn
  needs, nothing else;
- `kernel.config`, the resolved configuration, kept so the artifact names
  what produced it.

The base configuration is Firecracker's 6.1 microvm config, pinned to a
Firecracker tag in `Dockerfile`. `arizuko.fragment` is the delta on top of it.
Every driver a turn needs is built in, never a module, because the initrd
carries no module loader. `check-config.sh` runs after `olddefconfig` and fails
the build when the resolved config dropped a line the fragment asked for, or
when the fragment disables a symbol Kconfig does not declare.

## The key is every input

`<key>` is `KVER` plus a hash of `Dockerfile`, `arizuko.fragment`,
`check-config.sh` and `guest-init/*.go`, test files excluded. Each of these
lands in the artifact, so each is in the key. A key that missed `guest-init`
and later the `Dockerfile` shipped a stale `bzImage` twice, because the target
skips a build when the keyed directory is already complete. An incomplete
directory stops the target and is kept for inspection.

`RUNED_KERNEL_DIR` points runed at the directory; runed refuses to start
without `bzImage` and `initrd.gz` there.

## Why the kernel is its own artifact

The kernel is not a stage of `ant/Dockerfile`. `ant/` changes about twice a
day and the kernel changes only with its version or its fragment, so a stage
there would rebuild for nothing. `docker builder prune -f` is the disk lever
an operator pulls, and it would delete a kernel that lived in a build cache.
The artifact on disk survives both.

## /dev is what the initrd carries

`CONFIG_DEVTMPFS_MOUNT` does nothing on an initramfs boot, so `/dev` in the
guest is exactly the nodes `Dockerfile` creates with `mknod`. A turn tool
that opens a node the list lacks fails with "no such file". `guest-init`
cannot mount devtmpfs itself: that replaces the console the kernel handed
PID 1, and the guest then runs in silence.

## guest-init

`guest-init/` is PID 1: a static Go binary in its own module with its own
`go.mod`, so the root `go test ./...` never reaches it; run `go test ./...`
inside `kernel/guest-init/`.
It mounts what the host shares, assembles the agent's root, starts the MCP
bridge and runs the agent (`runed/README.md` § Mounts).
