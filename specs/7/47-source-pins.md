---
status: partial
---

# Native package layers and provisioning

Related specs: [25-publish-plan](25-publish-plan.md),
[30-permanent-vms](30-permanent-vms.md) and
[41-qemu-substrate](../6/41-qemu-substrate.md).

A native package layer supplies guest dependencies above the release rootfs.
Selected skills, hooks and executables already use the source layout in
[EXTENDING.md](../../EXTENDING.md#installing-packages) and the mounts in
[runed/README.md](../../runed/README.md#mounts). Native layers, provisioning and
record retirement remain accepted, unfinished work.

## A native layer depends on the release rootfs

A package's `Dockerfile.ant` supplies files above the release rootfs.
The guest root share can expose `rootfs/` and `layer/` on one filesystem, with
the layer first in overlayfs `lowerdir`. `kernel/guest-init/main.go` owns that
plan. `runed/qemu.go` owns the exported root share and guest environment.

Install builds against the release ant image through `ARIZUKO_ANT_BASE`.
The proposed package layer comes from `docker save`, without exporting the
base again. Recipes may only add files; deletion markers refuse. A changed
base requires a rebuilt layer. One group selects at most one native layer.
Layer selection and image environment need actual VM proof.

The base rootfs stays a plain directory tree. Packing belongs to
[30-permanent-vms](30-permanent-vms.md) and BUGS L447. A different base image,
two native layers or a group-scoped connector each needs its own contract.

## System provisioning retains each resource's owner

Connector declarations need a source reader in `routd/connectors.go`.
Outbound OAuth provider identity stays system-only in
`auth/surrogate/registry.go`. Declared credential delivery, source containment
and adapter service principals remain requirements; BUGS L200 records the
adapter boundary. Group package installation confers none of that authority.
The fragment, resource, env and grant handlers retain their authorization.

The accepted installed-package table/resource retirement requires replacements
for dirty checks, remove, sync discovery, folder rename/delete, REST/MCP and
dashboard status. The current implementation retains those rows as actual
selected state. Retirement cannot remove their consumers or visibility.

## The unfinished contracts require service and VM evidence

Native dependency installation must affect only the selected group while the
release base stays identical. System readers must activate and withdraw their
owned payloads through actual service tests. Selected skill mounts alone do
not prove either contract.
