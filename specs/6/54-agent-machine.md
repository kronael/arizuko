---
status: draft
---

# The agent owns a persistent machine and runtime

An agent's machine is its private Unix environment for files, installed software and ordinary processes.
The agent can customize that environment and keep the changes between turns.
The agent reaches the machine as a Unix user with an SSH key.
Machine provisioning and image creation belong to Hive, a separate project.
Groups, messages, routes and grants retain their arizuko owners.

## Installed software disappears at the turn boundary

Source baseline: `a3769cd56b9a01c51815cd25f400541ab2d82ad3`.

`kernel/guest-init/main.go:414` puts root writes on tmpfs.
`runed/qemu.go:255` reaps the VM at the turn's end.
Installed system software therefore disappears even though home files persist.
`runed/mounts.go:56` mounts persistent group files at `/home/node`.
The home mount alone cannot preserve changes elsewhere in the guest root.

## The machine keeps state beyond a turn

```text
 routd ── turn ──► runed ──► agent harness
                              │
                              │ SSH as a Unix user (new)
                              ▼
                   ┌─────────────────────────┐
                   │ agent machine (changed) │
                   │ files, software         │
                   │ Unix processes          │
                   └──────────┬──────────────┘
                              │ writes (changed)
                              ▼
                   ┌─────────────────────────┐
                   │ private disk (new)      │
                   └─────────────────────────┘
```

An SSH command is an ordinary process of the agent.
Its user, key, destination and host key are explicit configuration.
A failed connection returns an error; it cannot fall back to the host shell.
Unix permissions limit the user's file and process access inside the machine.
Arizuko grants still gate platform actions; an SSH key grants no platform authority.

Machine access uses the standard SSH client and Unix processes.
No custom shell/file endpoint, exec tool or host-side harness migration is required.
[The seam](41-qemu-substrate.md#the-seam-one-interface-one-production-type-one-instance-one-substrate) still governs Runtime and its Turn value.
Keep `Run(ctx, Turn)` and `Kill(handle)` without new methods or fields.
`renderTurn` remains the single producer of the turn envelope.
Machine details stay in runtime-owned state reached through the handle.

The persistence change touches these modules. Implementation requires approval:

```yaml
- path: runed/qemu.go (changed)
  job: attach private persistent storage and release only turn-owned resources
  names: [Run, Kill]
  never: erase the machine disk at turn completion or cancellation
- path: kernel/guest-init/main.go (changed)
  job: assemble a writable root backed by the machine disk
  names: [rootPlan]
  never: use a temporary upper for persistent software
- path: runed/mounts.go (changed)
  job: preserve workspace mounts and the selected package view
  names: [buildMounts]
  never: treat machine ownership as authority over another group's files
```

## Private disk retains installed software

```text
 release base ── read-only ──► machine root (changed)
                                      ▲
                                      │ writable upper (changed)
                              private disk (new)
 turn tmpfs upper (removed)
```

Current, `kernel/guest-init/main.go:421-425`:

```go
        {Source: shareRoot, Target: lowerDir, FS: "virtiofs", Flags: syscall.MS_RDONLY},
        {Source: "tmpfs", Target: overlayDir, FS: "tmpfs", Data: upperSize},
        {Source: "overlay", Target: guestRoot, FS: "overlay",
            Data:  "lowerdir=" + lowerDir + ",upperdir=" + overlayDir + "/upper,workdir=" + overlayDir + "/work",
            Needs: []string{overlayDir + "/upper", overlayDir + "/work"}},
```

Proposed storage rule, pseudocode:

```text
root = read-only release base + private disk-backed machine writes
home retains its existing persistent backing
turn cleanup removes only turn scratch and transport state
```

A stopped or restarted machine retains installed software and files.
A completed, failed or cancelled turn cannot erase them.
Each machine has private writable storage. Reusing another machine's disk is forbidden.
Disk exhaustion returns a visible error, not truncation or a memory-backed fallback.
Release changes must preserve the writable state or refuse an incompatible base visibly.

The operator chooses destructive reset or deletion explicitly.
Reset names the exact machine and storage it erases, including whether home files remain.
Turn cancellation, release installation and package removal never imply a reset.
Packed rootfs selection remains a separate storage optimization under BUGS L447.
No compressed image format or first-boot provisioning contract is required here.

## Turn cleanup preserves the machine disk

```text
 completed / failed / cancelled turn
                │ cleanup (changed)
                ▼
       turn processes and scratch released
       private machine disk retained (new)

 explicit operator reset (new) ──► named machine storage erased (new)
```

Current, `runed/qemu.go:255-262`:

```go
    // One teardown for every exit, in the order spec 6/41 §"The turn" step 9
    // fixes. It removes the record only when every pid it names is gone.
    defer func() {
        if err := reapVM(rec, q.procs, q.paths.Grace, q.reapPoll, tree, q); err != nil {
            slog.Error("vm teardown incomplete; record kept for the next disk scan",
                "handle", rec.Handle, "err", err)
        }
    }()
```

Proposed cleanup rule, pseudocode:

```text
turn completion or Kill(handle) ends the turn and its owned processes
cleanup releases the turn's handles, transports and scratch
cleanup leaves the machine's private disk intact
only an explicit authorized reset or deletion erases that storage
```

Persistence does not require an always-running machine.
The owner question below decides whether background processes can continue between turns.
Any reset control follows the existing resource handler and authorization rules.
The reset caller needs authority over the named target, independent of the machine user's login.

## Packages retain one declaration and each resource's owner

Packages use the existing installation declarations in
[EXTENDING.md](../../EXTENDING.md#installing-packages).
Dependencies installed into the machine persist under the caller's existing authority.
Package installation grants no connector credentials, routes or service authority.
Selected package records retain dirty checks, removal, sync, rename/delete and operator visibility.
The machine needs neither a second package inventory nor a special native-layer format.
The fragment, resource, env and grant handlers retain their authorization.

[Per-group harness](53-per-group-harness.md) owns the opaque document and ant's validation.
That document selects the harness, not the machine's OS or installed software.
[Identity-held credentials](../7/28-identity-held-credentials.md) owns model credential selection.
[Identity-held tools](../7/29-identity-held-tools.md) owns tool account selection and consent.
Persistent storage does not change those credential rules.

## Owner question

1. **Keep the machine running between turns?**
   `runed/qemu.go:255` ties VM cleanup to a turn.
   Recommend keeping the machine until an explicit stop, so ordinary background processes can continue.
   Caveat: an idle machine consumes resources; its power lifecycle needs approval before implementation.
