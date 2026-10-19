---
status: queued
depends: [../6/41-qemu-substrate, 28-identity-held-credentials]
---

# specs/7/30 — a packed rootfs and a disk-backed upper

`status: queued`: FILED, NOT STARTED. Held for the owner's word. It changes
the substrate seam, so `../6/41-qemu-substrate.md` §"The seam" governs it and
the owner approves the plan before any code.

## The owner's instruction

> don't use the tmpfs, use disk that gets cleaned in the turn.

That is the whole of decision 2 below. The rest is what tonight's measurements
say about carrying it out.

## What a turn's root is made of today

`kernel/guest-init/main.go:414` `rootPlan` builds three mounts, and their
lifetimes are the reason the design works:

| layer                      | backing today          | lifetime    |
| -------------------------- | ---------------------- | ----------- |
| lower — the release rootfs | virtio-fs, `MS_RDONLY` | per release |
| upper — the agent's writes | **tmpfs**              | one turn    |
| `/home/node` — the state   | virtio-fs, per folder  | forever     |

The upper is a tmpfs because a virtio-fs share works as an overlayfs lower and
NOT as an upper — the code cites kata-containers for that, and the comment
records the consequence: "the agent's every write dies with the guest."

## Decision 1 — the rootfs is packed, not a directory tree

`make rootfs` runs `docker export | tar -x` into
`<data_dir>/images/rootfs-<image-id-12>/`, a plain host directory. Measured
2026-10-19: the ant image is 7.85 GB of layers in `/var/lib/docker` and 7.1 GB
of flat files per instance, so ONE release costs the bytes three times on a
two-instance host.

`docker export | mkfs.erofs -zzstd` replaces the tree with one compressed
read-only file. QEMU attaches it `readonly=on` to every guest at once, the host
page cache is then shared across guests rather than per-virtiofsd, and the
guest-side mount changes only its filesystem type — still the overlay's lower,
still read-only.

Three measured gates, none of them met:

- `CONFIG_VIRTIO_BLK=y` IS already in `kernel/arizuko.fragment`.
- `CONFIG_EROFS_FS` is NOT, and no zstd option is either. `kernel/check-config.sh`
  is where a new requirement is enforced.
- `mkfs.erofs` is not installed on the host; only the `zstd` CLI is.

What this does NOT change: isolation. The lower is read-only in every design,
the per-folder writable share is untouched, and two instances sharing one
read-only image are exactly as isolated as two containers from one image tag.

## Decision 2 — the upper is a disk, cleaned at the end of the turn

A tmpfs upper spends GUEST RAM on every byte the agent writes, so a turn that
installs packages or builds a tree is bounded by memory rather than by disk,
and the failure is an OOM inside the guest rather than a disk error.

A per-turn virtio-blk disk removes that ceiling: the upper becomes an ordinary
filesystem on a block device, and the turn's end discards it. It pairs with
decision 1 — both want virtio-blk, which the kernel already has.

Unsettled, and each needs a measurement before any code:

1. **What backs the disk.** A sparse file per turn, a thin LVM volume, or a
   fixed pool. A sparse file is simplest and has the worst behaviour when a
   turn fills it.
2. **Who makes it empty again.** Creating a fresh filesystem per turn is clean
   and costs a `mkfs`; discarding and reusing is faster and leaves the previous
   turn's blocks readable until overwritten. The second is a disclosure path
   between turns of DIFFERENT folders and has to be settled on that ground, not
   on speed.
3. **What a full upper does.** Today a tmpfs upper that fills gives ENOSPC
   inside the guest. A disk must give the same visible error, never a silent
   truncation.
4. **Whether the turn still dies cleanly.** The current guarantee is that every
   write dies with the guest. A disk that outlives the guest, even briefly,
   weakens it — so the clean-up has to be on the same path that kills the VM,
   not a sweep that runs later.

## Decision 3 — what "permanent" would mean

A VM that outlives one turn changes the lifetime table above, and nothing in
`6/41` describes it. It is named here so the two decisions above are not
designed as if a turn is always the unit. Not specified; it needs its own
section once the owner says what persists.

## Unverified

| #   | Claim                                                                        | How to settle it                                                  |
| --- | ---------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| 1   | EROFS with zstd reaches ~2.5 GB on this tree                                 | Build one and measure; the ratio is a guess from the content mix. |
| 2   | The guest kernel can gain EROFS without growing past its current boot budget | Rebuild with the option and compare.                              |
| 3   | A per-turn block device does not slow a turn's start measurably              | `make bench-boot` already exists.                                 |
| 4   | The SDK's bundled binary runs on musl, if Alpine is ever the base            | `ldd` it; glibc-only would end that idea.                         |

## Order

After phase 6. `6/41` is `defected` on `L379`, so the seam has an open defect
before it takes a change of this size.
