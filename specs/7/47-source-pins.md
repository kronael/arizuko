---
status: partial
depends: [../5/28-packages]
relates-to: [25-publish-plan, 30-permanent-vms, ../6/41-qemu-substrate]
---

# Packages select immutable files for one target

A package supplies files from one source to a group or the system.
The package lifecycle belongs to [5/28-packages](../5/28-packages.md).
This contract extends that lifecycle with mandatory core and immutable skills.
Native dependencies, provisioning and record retirement remain accepted,
unfinished work below. The manifest foundation is implemented; core activation
and the retirement of agent migrations are proposed here.

## One source schema separates desired membership from active files

`container/manifest.go:16` defines the shared `arizuko.toml` schema.
Root metadata describes its owner. Ordered `[[product]]` entries select sources.
A source uses metadata for package identity. A group owns its own manifest,
including metadata, comments and extension fields. Runtime folder identity
comes from configuration and the store, never a directory basename or metadata.

Source metadata is not payload. Install never copies, claims, hashes or deletes
the group's root manifest. Nested source selections refuse before writes.
No recursive resolver, lockfile, install script or second source schema enters
this cut. The root `skills` metadata keeps its existing informational meaning;
actual skill directories define exports.

| Fact                                    | Owner and readers                                                                         |
| --------------------------------------- | ----------------------------------------------------------------------------------------- |
| Desired optional group membership       | Group-owned manifest; explicit package operations read its ordered selections.            |
| Artifact bytes                          | Immutable content snapshots; source validation and runtime read the same source manifest. |
| Active membership and artifact identity | One filesystem reference graph; runtime and status use one shared reader.                 |
| Source provenance and owned assets      | Existing installed-package receipts; status checks them against active references.        |

These facts have separate purposes. A pending desired edit cannot change active
files. A DB receipt cannot select files. A filesystem path cannot establish
source provenance.

## Proposed snapshots keep every selected byte stable

Built-in release paths, bare Git sources and local directories use the same
validator and snapshot reader. Private Git fetches use the operator's host Git
credentials. No folder credential reaches a fetch. Version selection and pins
are owner-deferred; no revision field is required in the group manifest.

Snapshots live beneath the configured instance data directory at
`packages/<name>/<content-digest>/`. The digest identifies content, including
the source manifest. It is not a user version pin. Source preparation excludes
repository metadata and rejects traversal, unsafe symlinks and invalid names.
A selected snapshot never changes in place. A local edit or moving branch
therefore cannot change installed files.

The existing receipt records the resolved Git revision or a local content
identity. Its existing ownership/hash maps carry a reserved non-resource
artifact kind. The lifecycle validates that kind beside `skill` and
`compose_fragment`. No table, scalar field or sidecar catalog is required.
Caller-controlled fields contain source declarations and validated identities,
never host mount paths. Trusted code resolves artifact paths beneath configured
storage and verifies their containment.

Other unpinned packages change through explicit install, upgrade or sync.
Required core follows the selected release through that same operation.
Inactive artifacts and completed selections remain available throughout this
cut. An already-rendered turn keeps its exact paths after an update or removal.
Garbage collection is separate work.

## Proposed activation uses one serialized filesystem selection

One shared lifecycle writes snapshots and active references outside writable
group homes. Each target has a completed selection directory containing only
references to immutable artifacts. One active reference selects that directory.
There is no JSON/TOML active-list file. The source manifest remains the only
package declaration.

One instance package lock serializes every target mutation, including setup,
generate, install, upgrade, apply, sync, removal and receipt repair. The writer
holds the lock across complete preflight, owned-file checks, selection and
receipt reconciliation. A journal-local rename lock in
`cmd/arizuko/group_rename.go:524` does not cover this lifecycle. A narrowly scoped
filesystem lock belongs beside the shared package implementation in `container/`.

The writer prepares a complete candidate and its references before selection.
An atomic replacement of the active reference selects the completed directory.
The writer does not rename a directory over a populated active directory.
The selected directory and its artifacts are immutable. Runtime resolves the
system and exact group references once under the same lock, then releases the
lock with stable artifact paths. A turn never follows a mutable active link
after that resolution. Instance serialization also protects the combined
system/group collision check.

| Failure                                                 | Active files and receipts                                                                                     |
| ------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Source, validation, collision or clean-copy check fails | Keep the prior selection and receipts; report the exact cause.                                                |
| Candidate preparation or atomic switch fails            | Keep the prior active reference and receipts; report failure. Unselected artifacts confer no installed state. |
| Receipt write fails after selection                     | Keep the completed selected files; report an incomplete operation. Status reports the mismatch.               |
| Removal switches successfully but receipt cleanup fails | The package is inactive; report incomplete removal until its receipt is reconciled.                           |

The filesystem and store do not form one transaction. Retrying the same declared
operation verifies the selected bytes and repairs matching receipts under the
lock. It cannot silently relabel source provenance. A changed declaration or
different selected bytes require refusal or a newly explicit install operation.
Fresh setup can prepare before a store exists. Normal setup completion writes
the matching receipt before the first production turn; no second copy path or
bootstrap command handles that timing difference.

## Proposed status checks references against receipts

`routd/packages_store.go:24` defines the retained receipt shape.
REST, MCP, CLI and dashboard status enumerate canonical references through one
reader. One existing row renderer joins matching receipts for source identity,
owned resources and timestamps. Missing, stale or mismatching receipts return
an actionable incomplete-operation error. Status neither hides an active entry
nor renders a stale DB row as an active installation.

A receipt marked with the artifact kind but absent from active references also
reports disagreement. Adapter-only receipts retain their existing lifecycle;
they are not incomplete skill installations merely because they lack an
artifact reference. Runtime reads the active filesystem graph directly. Runtime
does not read routd's DB, pending group choices or a new package service.
The existing installed-package resreg handler and its authorization remain the
REST/MCP surface. The dashboard consumes that same checked result.

## Proposed core is mandatory for every registered group

One `core` source rooted at `ant` supplies the base manual, base skills and
output styles. Its `arizuko.toml` declares its identity. The release declares
that required source once, using configured release paths. Core removal and a
conflicting core identity refuse. A missing core source is a setup/update error.
An absent or metadata-only optional group manifest cannot disable core.

Fresh create, CLI group add, dashboard creation, onboarding and archive restore
use the same core lifecycle. Ordinary deterministic release update covers every
existing registered group, including groups without optional installed records.
No operation inserts core metadata or selections into an existing group file.
Each individual skill keeps its disable choice outside immutable source.

Group and system skill packages share install, upgrade, remove, sync and list.
Skills-only sources need no dummy service fragment. `products apply` delegates
its ordered selections to that lifecycle. An exact `--group` target is separate
from the existing repeatable `--folder` grant bounds. Instance-wide remains the
default. Direct group edits change only the intended selection entry. If a
lossless edit cannot preserve the rest of the manifest, refuse before mutation.
Removal withdraws that entry and the package's managed contribution, so sync
cannot resurrect the removed package. Core remains mandatory.

## Proposed mounts preserve the existing discovery paths

`runed/mounts.go:36` renders the mount table. `runed/runtime.go:50` already carries
that table in `Turn.Mounts`; `core/types.go:108` supplies `core.Mount.RO`.
The renderer emits one read-only mount per packaged skill onto
`/home/node/.claude/skills/<name>`. Core mounts its manual onto
`/home/node/.claude/CLAUDE.md` and styles at the existing output-style paths.
Distinct custom skills and styles remain writable. The group's root `CLAUDE.md`
remains its runbook. Claude loads the mounted manual natively; Codex and pi read
the same manual once. No additional manual enters the turn payload.

`runed/vmtree.go:79` orders parent mounts before children. Host enforcement
applies read-only binds before sharing. Packaged writes fail with EROFS;
renaming or removing a mounted name fails with EBUSY. This needs native proof,
including a successful write to a distinct custom skill.

Effective skill names must be unique across core, system packages, group
packages, custom `.claude/skills` directories and top-level group skills.
Disabled names also participate in this check. No packaged-name override is
allowed. The writable `.disabled` marker stays in the group home. A disabled
name receives an empty read-only mount, which masks stale files without loading
a skill. Clearing the marker selects the current artifact on the next turn.

The runtime port stays `Run` and `Kill`. `Turn` and `RunRequest` gain no fields.
Per-name mounts solve skill delivery without native rootfs layers or a changed
guest image contract.

## Proposed ordinary install adopts only proven-clean copies

Install and upgrade preview existing copied skills and the resident manual.
The same operation adopts only a complete file set whose bytes match a known
shipped snapshot or recorded ownership hashes. A known shipped Git tree can
prove unchanged older content. Exact source equality is also a clean case.
An independently refreshed `.merge-base` does not prove ownership.

Unknown provenance, changed files, extra files, unsafe symlinks and edited
manuals block the affected operation with exact paths. No files are deleted on
that refusal. The operator resolves distinct instructions in the group runbook
or renames a custom skill. The lifecycle does not create a hidden override.

Preview and apply share the comparison. Apply rechecks hashes under the package
lock before removing individually verified files and empty mount directories.
Recursive removal is forbidden. Preserve custom siblings, disable markers,
group metadata, persona, memories, settings, credentials and runtime state.
Stock, system-package and product copies use this one ownership check.

## Proposed package updates retire agent migrations entirely

Ordinary deterministic install/update supplies core and packaged files without
an agent turn or a model call. The copied-skill updater, merge-base delivery,
forced migrate seeding, numeric migration counter, pending warning, startup
enqueue, session reset, `/migrate` skill and numbered agent instructions retire.
No conversion command, bootstrap command, install script or replacement agent
job takes their place. Release instructions require neither that counter nor a
migration broadcast. Historical release notes remain history.

`container/runner.go:50`, `runed/settings.go:26` and `routd/loop.go:484` identify
the current paths to retire. Deterministic daemon schema preparation remains in
`store/served.go:24`. Each owner retains its schema and data migrations.
Restart and repeated core update must preserve sessions and enqueue zero
migration messages. Backend discovery, clean-copy adoption and native mount
checks must prove the replacement before the old delivery paths retire.

## Accepted native layers remain unfinished

Rhias's cadastral tool needs native dependencies in its own guest only. A
package's `Dockerfile.ant` supplies files above the release rootfs. The guest
root share can expose `rootfs/` and `layer/` on one filesystem, with the layer
first in overlayfs `lowerdir`. `kernel/guest-init/main.go:414` owns that plan.
`runed/qemu.go` owns the exported root share and guest environment.

Install builds against the release ant image through `ARIZUKO_ANT_BASE`.
The package layer comes from `docker save`, without exporting the base again.
Recipes may only add files; deletion markers refuse. The layer lives inside
its artifact directory, keyed by the base rootfs identity. A changed base
requires a rebuilt layer. One group selects at most one native layer.
Artifact, native layer and image environment need joint activation and actual
VM proof. Core skill mounts do not establish that contract.

The base rootfs stays a plain directory tree. Packing belongs to
[30-permanent-vms](30-permanent-vms.md) and BUGS L447. A different base image,
two native layers or a group-scoped connector each needs its own concrete
contract. No Rhias-only image selection is claimed by this core cut.

## Accepted provisioning and record retirement remain unfinished

System payload readers need their own complete activation contract. Compose
fragments can link to immutable artifacts through `compose/fragments.go`.
Connector declarations need a source reader in `routd/connectors.go`.
Outbound OAuth provider identity stays system-only in `auth/surrogate/registry.go`.
Declared credential delivery, source containment and adapter service principals
remain requirements; BUGS L200 records the adapter boundary. Group skill
installation confers none of that authority. The current fragment, resource,
env and grant handlers retain their owners and authorization. System payload
removal withdraws only its owned links and reports edited copies. Group skill
selections refuse system-only payloads.

The accepted table/resource retirement requires replacements for dirty checks,
remove, sync discovery, folder rename/delete, REST/MCP and dashboard status.
That work is separate from retaining receipts in the core cut. Filesystem
activation cannot silently remove these consumers or their visibility.
Tools-package publication and stock-name overlap also remain unfinished.
Core supplies the existing base skills; package installation refuses overlap.
This cut neither publishes tools nor synchronizes vendored stock skills.
An agent can develop a tool inside its group and publish a Git source. The
operator installs that source through the same lifecycle; publication adds no
MCP tool or automatic deployment authority.

## The contract requires independent failure evidence

The baseline is bdf11c1a2e0db5f3b9149e986e7c392428f41752. The work record holds
the exact verification commands and their output; this spec claims no runtime
implementation beyond the shared manifest foundation.

| Contract                         | Required evidence                                                                                                                                  |
| -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| Stable source bytes              | Edit a local source and move a Git source; active and already-rendered paths retain the selected bytes.                                            |
| One active graph                 | Concurrent mutations serialize; source and switch failures keep the prior selection; receipt failure is visible and a matching retry repairs it.   |
| Honest status                    | Plant missing, stale and DB-only artifact receipts; every status surface reports disagreement through the shared handler.                          |
| Mandatory core                   | All creation paths and existing registered groups receive core; removal, missing source and conflicting identity refuse.                           |
| Protected custom state           | Modified, extra and uncertain copies refuse; proven-clean copies adopt; package collisions refuse; disable choices survive.                        |
| Native enforcement               | Packaged writes, rename and removal fail; distinct custom writes succeed; every backend discovers the mounted content.                             |
| Deterministic updates            | Restart and repeated updates make zero migration turns or model calls and retain sessions.                                                         |
| Broader native/provisioning work | Rhias gains its native tool alone; the base stays identical; system readers activate and withdraw their owned payloads with real VM/service proof. |
