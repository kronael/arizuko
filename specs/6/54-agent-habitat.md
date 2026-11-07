---
status: draft
---

# The local agent owns a habitat

An agent runs locally on arizuko's trusted side and owns a virtual machine, its habitat.
The habitat holds persistent groups, files, installed software and web pages.
The agent holds the model loop, harness selection, credentials and turn state.
"Local" means outside the habitat, not on the message sender's computer.

The owner's words define the boundary:

> in essence yes but i think its. unideal. and it wuld be better to rather have that as agent is local and owns a virtual ma chine and that might be the havbitat rather. that is the persistne tgroups and everyhtging. migt fit into the habitat but not i to the agents.

A message reaches a group in a habitat. A bound agent may run a turn there.
The computer and its contents do not depend on that agent's presence.
The VM is the habitat, not the agent or a temporary turn.

## The guest runs the harness today

Source baseline: `a3769cd56b9a01c51815cd25f400541ab2d82ad3`.

The code already separates routing from execution. `routd/loop.go:660`
resolves messages to folders; `routd/dispatch.go:830` retains topic sessions.
`runed/runtime.go:32` exposes only `Run` and `Kill`.
`runed/mounts.go:56` mounts persistent group files at `/home/node`.
Its web slots at `runed/mounts.go:194` belong to the folder.
`store/groups.go:507` stores an opaque harness document.
`ant/src/backend.ts:124` alone interprets backend, model and effort.
These boundaries fit the model.

The execution boundary does not fit. `runed/qemu.go:445` sends the turn into
the guest. `kernel/guest-init/main.go:264` starts ant there.
`runed/render.go:74` places credentials in the payload sent to that guest.
`runed/mounts.go:170` exposes Codex state and staged login files there.
Claude enables native tools with guest isolation as its protection at
`ant/src/claude.ts:550`. Codex makes the same assumption at `ant/src/codex.ts:43`.
Moving those invocations to the host unchanged would expose the host's shell and files.

Persistence also differs. `kernel/guest-init/main.go:414` puts root writes
on tmpfs. `runed/qemu.go:255` reaps the VM at the turn's end.
Installed system software therefore disappears even though home files persist.
`routd/loop.go:660` has no agent-binding check on direct folder addresses.
The settings in `store/groups.go:48` contain no binding switch.
A habitat with no agent cannot rely on an observe route to prevent dispatch.

## The agent and habitat have separate lifetimes

```text
 adapters ── messages ──► routd ── turn (changed) ──► runed
                           │                          │
                  groups and messages                 │
                           │                 ┌────────▼──────────────┐
                           ▼                 │ local ant (changed)   │
                         store               │ loop, credentials     │
                                             │ harness, submit_turn  │
                                             └────────┬──────────────┘
                                                      │ tools (new)
                                             ┌────────▼──────────────┐
                                             │ habitat VM (changed)  │
                                             │ groups, files, web    │
                                             │ installed software    │
                                             │ guest ant (removed)   │
                                             └───────────────────────┘
```

The group remains the existing folder coordinate for messages, grants and web slots.
The habitat owns the group's persistent environment.
The store retains authoritative group rows, messages, routes, grants and audit records outside the VM.
Ownership by the habitat does not move platform tables into the guest.
The first implementation keeps one existing group folder per habitat.
Several groups sharing one VM need a separate decision about isolation.
Neither a folder name nor VM ownership grants authority.

The boundary changes these existing modules. Implementation requires approval:

```yaml
- path: routd/loop.go (changed)
  job: resolve group messages once, then test the optional agent binding
  names: [resolveRows]
  never: boot a VM or infer a binding from files
- path: store/groups.go (changed)
  job: retain group settings independently of the optional agent
  names: [GroupConfig, GroupHarness, SetGroupHarness]
  never: interpret the harness document
- path: store/message_folders.go (changed)
  job: retain primary posts without claiming an agent observed them
  names: [MarkMessagesObserved, ObservedSince]
  never: choose whether an agent runs
- path: runed/qemu.go (changed)
  job: coordinate the local turn and access to the persistent habitat
  names: [Run, Kill]
  never: send agent credentials into the guest
- path: runed/render.go (changed)
  job: render one turn envelope for the local agent
  names: [renderTurn]
  never: give the guest the agent payload or credentials
- path: runed/mounts.go (changed)
  job: separate habitat content from local harness state and login files
  names: [buildMounts]
  never: mount a local credential cache into the habitat
- path: kernel/guest-init/main.go (changed)
  job: assemble persistent guest storage and serve habitat operations
  names: [rootPlan]
  never: start the model harness
- path: ant/src/claude.ts (changed)
  job: run the local Claude loop with tools confined to the habitat
  names: [claudeHarness]
  never: expose native host shell or file access
- path: ant/src/codex.ts (changed)
  job: run the local Codex loop with the same habitat boundary
  names: [codexHarness]
  never: treat host execution as guest isolation
```

## Local tools act inside the habitat

```text
 local ant (changed) ── shell / read / edit (new) ──► guest tool endpoint (new)
       │                                                   │
       └── permissioned platform tools ──► routd            ▼
                                                  habitat files / processes
 native host shell and file tools (removed)
```

Current, `kernel/guest-init/main.go:832-837`:

```go
func agentCmd(env map[string]string, stdin []byte) *exec.Cmd {
	cmd := exec.Command(agentBin)
	cmd.Env = envList(env)
	cmd.Stdin = bytes.NewReader(stdin)
```

Proposed boundary, pseudocode:

```text
runed starts ant locally with the rendered turn and its credentials
ant's shell and file operations reach only the selected habitat
guest-init serves those operations without receiving the model payload
ant submits completion to routd through submit_turn
```

The guest endpoint accepts only operations bound to the active turn and its habitat.
It carries arguments, input, output, exit status and cancellation, never the agent environment.
An unavailable endpoint fails the tool call visibly. No tool falls back to host execution.
Backend child agents and package hooks obey the same boundary.
Guest-controlled hooks or executables cannot run beside local model credentials.
Guest network access still crosses the existing egress policy.

The authenticated tool transport and each backend's native-tool replacement need proof before implementation.
The existing vsock bridge carries guest MCP calls toward the host at `runed/qemu.go:772`.
It does not supply host-to-guest shell and file operations.

[The seam](41-qemu-substrate.md#the-seam) still governs `Runtime` and its `Turn` value.
Keep `Run(ctx, Turn)` and `Kill(handle)` without new methods or fields.
`Turn.Env` and `Turn.Stdin` describe the local agent's input.
`renderTurn` remains their single producer.
VM IDs, disks and transport endpoints stay in runtime-owned state reached through the handle.
Cancellation ends the turn and its tool calls, not the habitat's persistent storage.
Explicit habitat stop or deletion is a different operation from turn cancellation.

## Habitat storage retains installed software

```text
 release base ── read-only ──► guest root (changed) ◄── persistent disk (new)
                                      │
                                      ▼
                             installed software
 group files / web slots ─► habitat content
 turn tmpfs upper (removed)
```

Current, `kernel/guest-init/main.go:421-422`:

```go
		{Source: shareRoot, Target: lowerDir, FS: "virtiofs", Flags: syscall.MS_RDONLY},
		{Source: "tmpfs", Target: overlayDir, FS: "tmpfs", Data: upperSize},
```

Proposed storage rule, pseudocode:

```text
root = read-only release base + private disk-backed habitat writes
home and web slots retain their existing persistent backing
turn cleanup removes only turn scratch and transport state
```

A stopped or restarted VM retains installed software and files.
A completed, failed or cancelled turn cannot erase them.
Each habitat has private writable storage. Reusing another habitat's disk is forbidden.
Disk exhaustion returns a visible error, not truncation or a memory-backed fallback.
Release changes must preserve the writable state or refuse an incompatible base visibly.
The operator chooses destructive reset or deletion explicitly.

Packages use the existing installation declarations in
[EXTENDING.md](../../EXTENDING.md#installing-packages).
Guest dependencies install into the habitat under the caller's existing authority.
Package installation grants no connector credentials, routes or service authority.
Selected package records retain dirty checks, removal, sync, rename/delete and operator visibility.
The habitat needs neither a second package inventory nor a special native-layer format.
Packed rootfs selection remains a separate storage optimization under BUGS L447.
No EROFS, compression ratio, cloud-init or first-boot contract is required here.

## A habitat can have no agent

```text
 direct / routed / timed message ──► resolveRows (changed)
                                      │
                           ┌──────────┴───────────┐
                           ▼                      ▼
                     agent bound           no agent (new)
                       local turn          stored primary post
                                                  │
                                        existing folder feed
```

One explicit `agent` setting on the existing group row records the binding.
An absent setting retains the bound behavior of existing groups.
The existing groups resource authorizes binding updates on the target folder through REST and derived MCP.
Unbinding retains messages, files, web pages, installed software, routes and grants.
It does not delete the habitat or change its VM power state.
The one resolver checks the binding after direct or route resolution, including scheduled and delegated messages.
Neither `PERSONA.md`, package presence nor a busy refusal represents an absent agent.

Posts without an agent are primary posts, not observed ambient context.
`store/message_folders.go:312` excludes observed posts from watched-source reads.
Extend the existing feed writer with that distinction rather than adding another writer.
No-agent ingestion advances the cursor without dispatch, retries or a missing-reply notice.
Binding read failures remain errors, never an implicit absent agent.

Existing bearer authentication and grants admit external writers.
Writes report that the post was stored without promising a queued turn.
Group reads expose the binding. Clients that require an agent reply refuse an unbound target before waiting.
No synthetic turn completion is emitted for a post that runs no turn.
Default tasks keep their own lifecycle. Unbinding does not rewrite the task table.
Binding later starts with new triggers, without replaying retained posts automatically.
External file and web access remain subject to the existing surfaces and their permissions.

## Programmable turns belong beside the local agent

```text
 local agent program (new) ── authorized message or task ──► routd
 local agent program (new) ◄──────── turn result ─────────── routd
 habitat artifacts stay outside the model loop
```

A program can request several turns and retain their results on the trusted side.
Each turn uses normal dispatch, folder grants, serialization and cancellation.
Changing the habitat's VM lifetime does not change the lifetime of a turn or session.
A program gains no authority beyond its caller and target grants.
Files remain artifacts in the habitat, not executable control state for the trusted loop.

`store/turn_results.go:71` records status and failure, not a workflow output value.
The existing task and delegation paths suffice until a concrete program needs more.
This draft adds no workflow resource, graph schema, scheduler, approval node or retry engine to routd.
Those contracts need their own evidence and approval before implementation.

## Credentials stay beside the local loop

```text
 identity / secret broker ── credentials ──► local agent (changed)
 local agent ── authorized tool call ──► host broker ──► provider
 local agent ── credential-free operation (new) ──► habitat
 guest model payload and login mounts (removed)
```

[Identity-held credentials](../7/28-identity-held-credentials.md) owns payer selection,
the canonical identity join, enrollment, refresh and revocation.
[Identity-held tools](../7/29-identity-held-tools.md) owns tool account selection and consent to act as a human.
Both remain separate specs. Any identity consumer uses their one join.
The habitat boundary depends on those credential rules, not on a second identity map.

Model keys, OAuth tokens, gateway keys and login caches stay on the trusted side.
Brokered tool credentials stay with the broker.
The habitat receives neither credential environment values nor credential files, including through shell grants.
An authenticated CLI in the habitat needs a brokered operation rather than a copied token.
Existing `shell:env` delivery at `routd/dispatch.go:860` conflicts with this boundary.
Changing that delivery requires explicit approval alongside the local-tool contract.
Mutable habitat files cannot select the payer, replace trusted hooks or install a local connector process.
The local agent's credentials do not exempt its calls from authorization, containment or audit.

Messaging and delivery retain their existing owners: routd routes and records delivery, and adapters transport it.
The local agent uses delivery tools and `submit_turn` under
[voluntary reply](47-voluntary-reply.md). Prose fallback is not another delivery path.
[Per-group harness](53-per-group-harness.md) still owns the opaque document and ant's validation.
That document selects the local harness, not the habitat's OS or installed software.

## Consolidation keeps only the core model

| Source                | Survives here                                                                                                 | Cut from the core model                                                                                   |
| --------------------- | ------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Agent habitat         | Shared messages, routing, permissions, delivery ownership and module boundaries                               | Process composition and automatic prose delivery                                                          |
| Permanent VMs         | Private disk-backed habitat state, visible disk errors and explicit reset                                     | Per-turn upper destruction, mandatory EROFS packing, compression estimates and substrate experiments      |
| Source pins           | Installed guest dependencies persist, package declarations remain canonical, resource owners retain authority | Native layer builds, one-layer selection, installed-record retirement and system provisioning expansion   |
| Groups without agents | Explicit optional binding, primary posts, external writers and clients that do not wait for absent turns      | New external bearer setup, feed/history expansion, extra DAV mounts and moving sessions into runed        |
| Programmable turns    | Agent-side programs use ordinary authorized turns and results                                                 | Broker workflow graphs, new workflow resources, scheduler changes, approval DSL and bounded output schema |

Package import adapters in phase-7 spec 25 and composition drafts 26 and 27 remain deferred to phase 8, outside this model.

## Owner questions

1. **Approve local ant with tools reaching into the VM?**
   `kernel/guest-init/main.go:264` runs ant in the guest, while Claude and Codex enable native tools.
   Recommend running ant on the trusted side with one authenticated guest endpoint for shell, reads and edits.
   This keeps model credentials outside the habitat.
   Caveat: each backend must prove native host tools, child agents and hooks cannot bypass that endpoint before migration.
2. **May the habitat remain running between turns?**
   `runed/qemu.go:255` ties VM cleanup to a turn, which cannot support a persistent guest service.
   Recommend keeping the VM until an explicit stop, with disk state surviving every stop and restart.
   Caveat: a running habitat consumes resources without an active agent, so power controls need a separate approved lifecycle contract.
3. **Approve the credential boundary for guest CLIs too?**
   `routd/dispatch.go:860` currently exports granted shell credentials into the guest payload.
   Recommend replacing authenticated guest CLI operations with brokered tools, so credentials never enter habitat storage or processes.
   Caveat: an existing CLI that requires a token needs a broker operation before its credential delivery can be removed.
4. **Keep one existing group per habitat for the first implementation?**
   `runed/mounts.go:56` mounts one folder as the guest home.
   Recommend retaining that relation and adding only the explicit optional agent binding on the group row.
   Caveat: several persistent groups in one VM need a later decision about file layout and isolation.
5. **Keep programmable turns as local agent code without a routd workflow engine?**
   `store/turn_results.go:71` stores no reusable workflow output.
   Recommend ordinary messages, tasks and recorded outcomes until a measured program requires a new contract.
   Caveat: operator resume, structured output transfer and human approval steps remain unspecified.
