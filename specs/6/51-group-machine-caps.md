---
status: draft
depends:
  [
    41-qemu-substrate,
    ../5/32-acl-unified,
    ../5/33-paths-roles,
    39-gateway-owns-spend,
  ]
---

# specs/6/51 — each group sizes its own machine

A group's machine is the KVM guest that each of its turns runs in. This spec
lets the operator set that guest's memory, vCPUs, run timeout and tmpfs sizes
for one group, with `acl` rows named after the QEMU or `guest-init` setting
that each one changes. A group with no such row runs at the instance default.

The owner's framing, 2026-10-23: "each group is de facto its own machine
owned by the agent; by default the group has something, and the operator can
set limits on resources and pretty much everything … it's some caps grant or
so." Two constraints from the same day: pay for the change by deleting code
it makes redundant, and give each cap the name and the value spelling of the
QEMU parameter it sets. The direction is the owner's; the shape needs
sign-off, so the status is `draft`.

## Problem — measured at `5ceb13dc` and on hel1v5 2026-10-23

Every guest on an instance has one size, read from instance env at runed
start. The per-group knob that exists has no effect.

| Fact                                                                                                                                   | Where                                                                               | Measured                                                                                                                                                                                                                                                                                                                                             |
| -------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| One guest size for every turn: `RUNED_GUEST_MEM`, default `2G`                                                                         | `runed/qemuhost.go:129`, rendered at `runed/qemu.go:139-140`                        | Peak guest memory 624–746 MiB over 11 turns on v0.70.8 (diary 2026-10-22). Memory-plan step 5 waits on browser-heavy rhias peaks before it lowers the one size for all.                                                                                                                                                                              |
| One vCPU count: `RUNED_GUEST_SMP`, default 2                                                                                           | `runed/qemuhost.go:130`, `runed/qemu.go:138`                                        | hel1v5 has 2 CPUs.                                                                                                                                                                                                                                                                                                                                   |
| Four inputs to one deadline: `RUNED_RUN_TIMEOUT` (20m), `CONTAINER_TIMEOUT` (60m), `IDLE_TIMEOUT` (60m) and `container_config.Timeout` | `runed/manager.go:381-401` `turnTTL`, `core/config.go:227-228`, `core/types.go:105` | Neither krons nor sloth sets any of the three env keys (`env/runed.env`, `env/routd.env`, `.env`, read 2026-10-23). `turnTTL` floors every guest ceiling at `IDLE_TIMEOUT` + 30s = 60m30s, which is above the 20m run ceiling, and then takes the lower of the two. So every turn on both instances ends at 20m whatever its group's `Timeout` says. |
| The host check multiplies one size by one instance's cap                                                                               | `runed/qemuhost.go:200-204`, `:313-331` `memoryFits`                                | `BUGS L513`: krons and sloth each pass with 2 × 2G; together with ~9 GB of residents (whisper 3 GB, sloth ExtendDB 2.6 GB) the host swapped 5 GB and guests booted in 20–30 s.                                                                                                                                                                       |
| tmpfs sizes are consts sized against 2G                                                                                                | `kernel/guest-init/main.go:108` upper `512m`, `:287` `/dev/shm` `1g`                | At `-m 1G` the `/dev/shm` const equals the whole guest.                                                                                                                                                                                                                                                                                              |
| The turn's existing guest capabilities are four separate `acl` reads, through the action lattice                                       | `routd/dispatch.go:762-765`                                                         | `BUGS L538`: a folder holding `admin` or `mcp:*` gets a read-only share, and every world onbod creates holds `mcp:*` (`onbod/main.go:908-911`); a folder holding `*` gets `egress`.                                                                                                                                                                  |

## Decision — a cap is an `acl` row the turn's folder holds

A cap is one `acl` row whose action is `cap:<name>=<value>`. routd reads the
caps of the turn's folder in the same single read that resolves `egress`,
`web:publish` and `gh:write`, and sends the values to runed. runed sizes the
guest from them, and uses the instance default for each cap the folder does
not hold.

**Why `acl`.** The guest's capabilities are already `acl` rows that routd
resolves per turn (`egress`, `web:publish`, `gh:write`, `shell:env:<KEY>`),
and authority over them moves by delegation bounded by subset-of-held
([`5/33`](../5/33-paths-roles.md) decision 3). A machine size is the same
kind of fact. As a row it gets, at zero new surface, the CLI
(`arizuko group <inst> grant`), the `acl` resource on REST and MCP, the dashd
grants page, the audit row, revocation at the next turn, and `auth.Delegate`.

**The one caveat.** A value in an action string has no order under the action
lattice. So delegation is exact-value only, and when several rows match one
folder the largest value wins — a row on `rhias/**` cannot be lowered for
`rhias/nemo` by a second row on `rhias/nemo`. The instance default lives in
env, not in a `**` row, so that a single folder can still be set below it.

### The vocabulary (proposed)

One declaration, in `store/acl.go` beside `ShellEnvPrefix` (`:107-109`),
because the row is store's and every writer renders through `aclPut`
(`store/acl.go:122`). Size values take QEMU's `-m` spelling: `<n>M`, `<n>G`,
or a bare `<n>` meaning MiB — the grammar `parseGuestMem`
(`runed/qemuhost.go:333-353`) already reads, which moves to `store` with it.

| Cap       | Example row action | Sets                                                                            | Hard-coded today                                                              | Default with no row | Built in |
| --------- | ------------------ | ------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- | ------------------- | -------- |
| `m`       | `cap:m=1G`         | QEMU `-m` and the memfd `size=`                                                 | `RUNED_GUEST_MEM` `2G`, `runed/qemuhost.go:129`; `runed/qemu.go:139-140`      | `RUNED_GUEST_MEM`   | step 4   |
| `smp`     | `cap:smp=1`        | QEMU `-smp`                                                                     | `RUNED_GUEST_SMP` 2, `runed/qemuhost.go:130`; `runed/qemu.go:138`             | `RUNED_GUEST_SMP`   | step 4   |
| `timeout` | `cap:timeout=10m`  | the `ctx` deadline `Manager.spawn` arms; at it the reap sends `SIGKILL` to QEMU | `RUNED_RUN_TIMEOUT` 20m, `runed/cmd/runed/main.go:92`; `runed/manager.go:297` | `RUNED_RUN_TIMEOUT` | step 4   |
| `upper`   | `cap:upper=256M`   | `guest-init`'s overlay upper tmpfs `size=`                                      | `upperSize` `512m`, `kernel/guest-init/main.go:108`, used at `:422`           | 512M, a runed const | step 7   |
| `shm`     | `cap:shm=512M`     | `guest-init`'s `/dev/shm` tmpfs `size=`                                         | literal `1g`, `kernel/guest-init/main.go:287`                                 | 1G, a runed const   | step 7   |

`timeout` takes a Go duration, as `RUNED_RUN_TIMEOUT` does, because no QEMU
flag carries it. `timeout` only lowers: the effective deadline is the smaller
of the cap and `RUNED_RUN_TIMEOUT`. Raising it would outrun routd, which
sizes its dispatch deadline, the gateway key's TTL and its crash sweep from
the one instance value (`routd/cmd/routd/main.go:110-116`,
`routd/dispatch.go:403`, `store/turn_context.go:429`).

`aclPut` refuses a `cap:` row whose name is not in the table or not yet
built, whose value does not parse for its name, whose effect is `deny`, or
that carries `params` or a `predicate`. A deny of a size means nothing, and
the resolver carries no claims — the same refusal `aclPut` already makes for a
predicate on `shell:env:` (`store/acl.go:128-137`). A name the code does not
yet read is refused at write, so no row is ever written that does nothing.

### How a folder resolves its machine

`auth.TurnCaps(store, folder)` (proposed) loads the rows of `folder:<f>` once:
its own rows, its roles through `acl_membership`, and the wildcard-principal
rows that match it (`policyRows`, `auth/authorize.go:267`). It reads one axis
only, the route: the sender adds nothing, for the reason
[`5/33`](../5/33-paths-roles.md) §"Where the axis deliberately does NOT reach"
gives — the guest is a function of the folder, not of the conversation.

- `egress`, `web:publish`, `gh:write`: an allow row with the EXACT action, its
  scope glob matching the folder, and no deny row — `namespacedAllows`
  (`auth/authorize.go:619`), the matcher `ShellEnvKeys` and `CheckHold`
  already share. An operator's `(*, **)` row confers none of them. An elevated
  (`/root`) turn still sets all three, as today.
- `cap:<name>=<value>`: among the matching allow rows, the largest value per
  name. A name with no row is absent from the result.

There is no parent-folder inheritance. A row held by `folder:rhias` does not
reach `rhias/nemo`, because `folder:rhias/nemo` is a different principal. A
row reaches a subtree only when its principal glob and its scope glob both
say so: `grant 'folder:rhias/**' 'rhias/**' cap:m=3G`. A role reaches every
folder bound to it: a `role:browser` row `cap:m=3G` on `**`, plus one
membership edge per folder.

### Who may write a cap

- **The operator**, any value, through any door: the CLI
  (`arizuko group <inst> grant folder:rhias rhias cap:m=2G`), the dashd
  grants page, or `POST /v1/acl` with a root token. No new verb and no new
  flag: a second door for one row is a second mechanism.
- **An agent**, through `add_acl` only, and only a row it holds WITH GRANT
  OPTION, at the exact value (`auth.Delegate`, `auth/delegate.go:34`;
  `actionCovers` at `auth/authorize.go:391` and `paramsCover` at
  `auth/delegate.go:156` both match exactly). An agent cannot raise its own
  machine or hand a child more than it holds. The CLI and dashd doors write no
  `grant_option`, so this path opens only when an operator writes the row
  through `POST /v1/acl`, `add_acl` or a package manifest; no case today needs
  more.
- **A folder admin on dashd, or the holder of an `acl:write` token**, today
  writes any row in its subtree without `auth.Delegate`
  (`dashd/grants_admin.go:179-252`, `routd/acl_resource.go:55-66`), and so
  could size its own folder's machine. It can already grant itself `egress`
  the same way. onbod gives every person it onboards `admin` on their world
  (`onbod/main.go:895`), so wherever onboarding creates worlds, a tenant can
  do this. `BUGS L540` records the gap. On krons (the `grants` listing of
  2026-10-20) every `acl` row names a role or a folder agent, so no onboarded
  world exists there; the membership edges were not read.

### How the value reaches runed — the port change (needs approval)

**The wire** (`runedv1.RunRequest`, routd's format,
`runed/api/v1/types.go:15`) gains one field:
`Caps map[string]string` (`json:"caps,omitempty"`), cap name to value as the
row spells it. routd forwards what `TurnCaps` returned and knows no cap name.
runed decodes the body with plain `encoding/json` (`runed/server.go:111`), so
an older runed ignores the field and a newer one reads an absent field as
"all defaults"; either daemon can deploy first.

**The port** (`runed.Turn`, `runed/runtime.go:50`) gains one field. Adding it
widens the value the port carries, which root `CLAUDE.md` ("A port is two
methods and stays two") reserves for a plan the owner approves. This spec is
that plan:

```go
// Machine is the size of the guest this turn runs in. It is the one Turn
// field a substrate interprets rather than delivers: it sizes the guest.
Machine Machine

type Machine struct {
	Mem   int64 // bytes of guest memory
	CPUs  int   // vCPUs
	Upper int64 // bytes of the guest's writable layer (step 7)
	Shm   int64 // bytes of /dev/shm (step 7)
}
```

The deadline stays `ctx`, as `specs/6/41-qemu-substrate.md` §"The seam"
fixes; `timeout` changes the value `Manager.spawn` arms it with and adds no
field.

**The two vocabularies, reconciled.** The cap name is the guest's own word,
because the operator types it and reads it back in `grants`: `cap:m=2G` is
`-m 2G`. The port field is the turn's word, because the port is written for
any substrate (`specs/6/41-qemu-substrate.md` §"The seam": "named for the
turn, never for a substrate") — `Mem` and `CPUs` are what Firecracker's
`mem_size_mib` and `vcpu_count` hold too. Each name is written once on each
side: `machineOf` maps a cap name to a field, `qemuArgs` maps a field to a
flag, and the value keeps its meaning (bytes, count) across both.

### How runed applies it (proposed)

- `machineOf(cfg, req)` reads `req.Caps`, parses each value with the store's
  one parser, takes the instance default for each absent name, and refuses
  the run as terminal when the wire names a cap this runed does not know —
  a version skew, never a state to boot in. `Manager.Run` calls it once,
  before `admit`, and hands the result to both `admit` and `renderTurn`.
- `qemuArgs` (`runed/qemu.go:133`) renders `-m`, the memfd `size=` and `-smp`
  from `Turn.Machine` instead of `qemuPaths.Mem`/`SMP` (`runed/qemu.go:353`).
  The "booting guest" line (`runed/qemu.go:360`) logs `mem`, `cpus` and the
  deadline.
- The deadline: `ttl = RUNED_RUN_TIMEOUT`, lowered by `timeout` when the
  folder holds a smaller one. `ARIZUKO_QUERY_TIMEOUT_MS` keeps following it
  (`runed/render.go:164-173`).
- Step 7: `qemuArgs` appends `ARIZUKO_UPPER=<bytes> ARIZUKO_SHM=<bytes>` to
  the kernel command line beside `ARIZUKO_CTL`, and `guest-init` reads all
  four keys the way `bootPorts` (`kernel/guest-init/main.go:346`) reads two —
  a missing key is a refusal, never a default. `/dev/shm` is mounted before
  the ctl channel exists (`:287`), so the command line, not a ctl frame, is the
  only carrier.

### The host check, per turn, across instances (proposed)

`memoryFits` checks one size times one instance's cap, once, at start. With
per-turn sizes no product at start bounds anything, and the measured swap
came from residents the product never counts. So the check moves to
admission and reads the host's own number:

- In `Manager.admit` (`runed/manager.go:199`), after the count cap
  (`:217-225`) and under the same `m.mu`, an agent run reads `MemAvailable`
  from `/proc/meminfo`. When `Machine.Mem` exceeds it, the run is `Busy` —
  routd retries it later and the folder's breaker does not count it — and runed logs
  `guest memory unavailable` with both numbers. When `Machine.Mem` exceeds
  `MemTotal`, no wait can fit it: the run is terminal and the chat gets the
  notice.
- `MemAvailable` is the kernel's count for the whole host. It already
  subtracts the memory that the other instance's guests and every resident
  daemon hold, with no state shared between instances. runed reads the host's `/proc/meminfo` from its
  container, as `hostMemTotal` does today (`runed/qemuhost.go:356`).
- At start, runed checks only that `RUNED_GUEST_MEM` fits `MemTotal`. The
  tap-subnet loop (`runed/qemuhost.go:210-216`) stays, because
  `MAX_CONCURRENT_CONTAINERS` still sizes the tap subnet, the CID window
  (`runed/qemu.go:535`) and routd's dispatch slots.

**Its caveat.** The check sees memory as it is, not as it becomes: a guest
admitted while another is still small meets that guest's growth later. The
balloon's free-page reporting (`runed/qemu.go:165-170`, shipped in v0.70.8)
keeps `MemAvailable` close to what guests really hold, and the count cap
still bounds this instance's own concurrent guests. A host that is short of
memory now holds turns instead of swapping, and says so in the log.

## What it deletes — measured at `5ceb13dc`

| What                                                              | Where                                                                               | Lines | Step |
| ----------------------------------------------------------------- | ----------------------------------------------------------------------------------- | ----- | ---- |
| `turnTTL`'s four-input arithmetic                                 | `runed/manager.go:381-401`                                                          | 21    | 1    |
| `containerGracePeriod`                                            | `runed/runtime.go:18-21`                                                            | 4     | 1    |
| `Config.Timeout`, `IdleTimeout`, their env reads, `envDur`        | `core/config.go:64-65`, `:227-228`, `:338-345`                                      | 12    | 1    |
| `GroupConfig.Timeout`                                             | `core/types.go:105`                                                                 | 1     | 1    |
| `CONTAINER_TIMEOUT`, `IDLE_TIMEOUT` in runed's env list           | `compose/compose.go:152`, `:171`                                                    | 2     | 1    |
| their reference entries                                           | `template/web/pub/arizuko/reference/env.html:132-142`, `:308-318`                   | 22    | 1    |
| the four-input deadline test                                      | `runed/manager_test.go:228-261`                                                     | 34    | 1    |
| `share_mount(readonly)`: the read, the wire field, the mount flag | `routd/dispatch.go:762`, `runed/api/v1/types.go:49`, `runed/mounts.go:69-70`, `:77` | 6     | 2    |
| three more `acl` reads per dispatch                               | `routd/dispatch.go:763-765` (four reads become one)                                 | 0     | 2    |
| `parseGuestMem`, moved to `store`                                 | `runed/qemuhost.go:333-353`                                                         | 21    | 3    |
| `qemuPaths.Mem`, `SMP` and their reads                            | `runed/qemuhost.go:104-105`, `:129-130`                                             | 4     | 4    |
| `memoryFits`, its call and its test                               | `runed/qemuhost.go:200-204`, `:313-331`; `runed/qemu_test.go:168-189`               | 46    | 5    |
| `upperSize` and the `/dev/shm` literal                            | `kernel/guest-init/main.go:104-108`, `:287`                                         | 6     | 7    |

What stays, and why. `RUNED_GUEST_MEM`, `RUNED_GUEST_SMP` and
`RUNED_RUN_TIMEOUT` stay as the instance defaults, read where they are read
today; a `**` row cannot replace them, because the largest row wins and no
folder could then go below it. `MAX_CONCURRENT_CONTAINERS` stays, because it
sizes slots, not memory. The renderer's per-capability reads stay
(`egressAllowlist` at `runed/render.go:143`, `ARIZUKO_GH_WRITE` at `:126`, the
web mounts): each is the one place a decision becomes a guest fact. Folding the
three wire booleans into `Caps` saves about 12 lines of field comments and
rewrites every test fixture that sets them, so they stay typed.

`GroupConfig.Timeout` needs no data migration. It has no effect on either live
instance (the Problem table's arithmetic), and `groupConfig` ignores a key the
type does not name (`runed/render.go:150-162`), so a row that still carries
`Timeout` decodes without error and the key is unread. `GroupConfig` keeps
`Mounts`.

## Not in scope, and the case that would bring each in

| Resource                                 | Why not now                                                                                                                                                                          | The case that brings it in                       |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------ |
| Disk quota of the group home             | The home is a host directory that `virtiofsd` exports; nothing in the stack meters it, and the measured disk pressure came from rootfs trees and repacks (`BUGS L359`), not a group. | A group that fills the instance disk.            |
| Concurrency per folder                   | One live run per folder is the design (`runed/manager.go:203`); a folder's topics serialize.                                                                                         | A folder whose topics must run at the same time. |
| Model                                    | `groups.model` is a parameter of the agent CLI, not of the guest, with its own instance default. Moving it into rows deletes nothing.                                                | A model that must follow delegation.             |
| Spend                                    | The gateway is the ledger and enforces the folder cap ([`6/39`](39-gateway-owns-spend.md)).                                                                                          | —                                                |
| The egress allowlist                     | `network_rules` keeps its own resource and ancestry; only the allow-all `egress` flag moves into `TurnCaps`.                                                                         | —                                                |
| Extra mounts (`container_config.Mounts`) | Host paths gated by `MOUNT_ALLOWED_ROOTS`; a different resource from size.                                                                                                           | `BUGS L496`.                                     |
| A vCPU host budget                       | CPU overcommit costs time, not swap.                                                                                                                                                 | —                                                |

## Why not the other shapes

- **Fields on the group config** (`container_config.Mem`, …). The wiring is
  the smallest: `container_config` already reaches runed
  (`runed/api/v1/types.go:29`, `runed/render.go:154`). But no door validates
  it — the groups resource stores raw JSON and `groupConfig` ignores unknown
  keys by design, so `"mem"` for `"Mem"` is silent — no CLI verb writes it, the
  dashd group page does not render it (`dashd/groups_admin.go:256`), and
  nothing bounds what a folder admin writes for its own subtree. A setter, a
  view, a validator and a second ceiling rule cost more lines than the
  vocabulary and the resolver, and the ceiling rule is a second mechanism.
- **A mixed shape** — values on the group row, a grant to permit raising them —
  is two mechanisms for one fact.
- **A host ledger of guest ceilings** shared by every runed. It is exact for
  ceilings, but it needs a host directory mounted into every runed, a lock,
  cleanup of a crashed instance's files, and one budget value two `.env` files
  must agree on — and it does not see residents, which caused the measured
  swap.
- **A `timeout` above the instance ceiling** needs a deadline on the turn row,
  because routd's crash sweep keys on one global value
  (`store/turn_context.go:429`, `routd/loop.go:441-442`).
- **Narrowest row wins** needs an order on globs that the evaluator does not
  have; largest-wins is the union rule `acl` already has.
- **Renaming `egress`, `web:publish`, `gh:write` into `cap:`** moves 12 live
  rows on krons alone, and no mechanism moves live `acl` rows (`BUGS L473`).
- **Sizing `upper` and `shm` as fractions of guest memory inside
  `guest-init`** is the smallest shape for those two (about 7 lines, no port
  field), but it is a derived default that the operator cannot change for one
  group, which is the knob the owner asked for.

## Order

One sonnet and one commit per step; each step's docs land in its commit. Each
commit leaves a deployable tree: routd sends no `caps` until step 6, and runed
reads an absent field as all defaults, so no step changes a live turn before
the step that means to. Line counts: deletions measured at `5ceb13dc`,
additions estimated; tests and docs included.

| Step | What lands                                                                                                                                                                                                                                                                                                                                                                                       | Proof (each test fails with the step's edit reverted)                                                                                                                                                                                                                                                                                                             | Net lines |
| ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------- |
| 1    | Delete the dead deadline inputs: `CONTAINER_TIMEOUT`, `IDLE_TIMEOUT`, `envDur`, `GroupConfig.Timeout`, `containerGracePeriod`; the deadline is `RUNED_RUN_TIMEOUT`. Docs: `template/web/pub/arizuko/reference/env.html`, `template/web/pub/arizuko/reference/schema.html:87`, `ant/README.md:130`, `runed/README.md:192`, `MIGRATION.md:75`, `specs/6/41-qemu-substrate.md` §"The seam".         | A run whose `container_config` carries `Timeout` gets the `RUNED_RUN_TIMEOUT` deadline; `TestRunedEnvKeysCoverEveryRunedVar` passes with both keys gone.                                                                                                                                                                                                          | ≈ −85     |
| 2    | `auth.TurnCaps`: one read, exact match for `egress`, `web:publish`, `gh:write`; delete `share_mount(readonly)`. Gate: the sub reads `arizuko group sloth grants` first and stops if any `share_mount` row exists (krons has none). Fixes `L538`. Docs: `template/web/pub/arizuko/reference/grants.html:205`, `runed/README.md:306`, `auth/README.md:52`, `specs/5/33-paths-roles.md` decision 6. | A folder holding `admin`, one holding `mcp:*` and one holding `*` each resolve all three false with a writable share; an explicit row resolves true; a deny wins.                                                                                                                                                                                                 | ≈ +55     |
| 3    | The vocabulary in `store`: `CapPrefix`, the names `m`, `smp`, `timeout`, the size parser moved from runed, the `aclPut` refusals.                                                                                                                                                                                                                                                                | `aclPut` refuses `cap:m=lots`, `cap:nope=1`, `cap:upper=1G` (not built), a deny, params and a predicate on a cap; accepts `cap:m=1G`.                                                                                                                                                                                                                             | ≈ +75     |
| 4    | runed: `RunRequest.Caps`, `machineOf`, `Turn.Machine` (the port change), `qemuArgs` from it, the lowered deadline.                                                                                                                                                                                                                                                                               | `Caps{m:1G, smp:1}` renders `-m 1024M`, memfd `size=1024M`, `-smp 1`; no caps renders the defaults; an unknown name is a terminal refusal; `timeout=5m` under a 20m ceiling arms 5m, `timeout=60m` arms 20m.                                                                                                                                                      | ≈ +105    |
| 5    | The host check per turn: `MemAvailable` in `admit`; delete `memoryFits`. Closes `L513`. Docs: `INSTALL.md:45`, and under `template/web/pub/arizuko/`: `howto/operate.html:99-103`, `howto/index.html:56`, `concepts/retries.html:101`, `reference/env.html` (`MAX_CONCURRENT_CONTAINERS`, `RUNED_GUEST_MEM`); `runed/README.md:81`.                                                              | With a fake meminfo: available below `Mem` is `Busy` with no spawn row and the log line; above admits; `Mem` above `MemTotal` is terminal.                                                                                                                                                                                                                        | ≈ +10     |
| 6    | routd: `TurnCaps` values, `caps` on the wire, both in the capabilities log line; the CLI accepts `cap:` (`cmd/arizuko/main.go:594`). Caps take effect here. Docs: `template/web/pub/arizuko/reference/grants.html`, `GRANTS.md`, `runed/README.md` § The turn envelope.                                                                                                                          | A `cap:m=1G` row dispatches `caps={m:1G}`; rows at 1G (role) and 2G (folder) dispatch 2G; a `folder:rhias` row does not reach `rhias/nemo` and a `folder:rhias/**` row on `rhias/**` does; `add_acl` by an agent holding `cap:m=2G` WITH GRANT OPTION writes exactly that row and is refused `cap:m=4G` and `cap:m=1G`; the default `admin` grant confers no cap. | ≈ +80     |
| 7    | `upper` and `shm`: the names unlock in the vocabulary, `Machine.Upper`/`Shm`, the kernel command line, `guest-init` reads them. Rebuilds the kernel artifact (`KERNEL_KEY` hashes `guest-init`, `Makefile:51-52`), 30–60 min of both vCPUs. Lands with memory-plan step 5, whose 1G default is the case.                                                                                         | `guest-init` refuses a command line without either key; the KVM turn test boots with `cap:shm=64M` and the guest's `/dev/shm` reports 64M.                                                                                                                                                                                                                        | ≈ +55     |

Total ≈ +295 lines, of which ≈ +95 are non-test code; the deletions pay for
about a third. Step 1 stands alone and is worth landing whatever the answers
below are.

`dashd` view and control (`specs/CLAUDE.md` item 6) need no new page: the
grants page lists every row and adds any row. Its action field is a fixed
dropdown of names no evaluator reads (`BUGS L539`); that fix, not this spec,
puts `cap:` rows on it, and it lands before step 6.

## Open questions

1. Approve the port change: `runed.Turn` gains one field, `Machine`
   (`Mem`, `CPUs`; `Upper`, `Shm` at step 7), the one field a substrate
   interprets.
2. Approve caps as `acl` rows `cap:<name>=<value>` named after the QEMU or
   `guest-init` setting, sizes in QEMU's `-m` spelling, the largest matching row
   winning, and exact-value delegation.
3. `timeout` only lowers `RUNED_RUN_TIMEOUT`. Does any folder need to run
   longer than the instance ceiling? That costs a deadline on the turn row.
4. The per-turn check against `MemAvailable` holds a turn (`Busy`) on a host
   short of memory, where today the guest boots into swap. Accept?
5. Step 2 reads `egress`, `web:publish`, `gh:write` by exact match — a folder
   holding `*` stops getting them, and krons has none — and deletes
   `share_mount(readonly)`, which gives every onboarded world its writable
   share back. Both are auth changes (`L538`). Go?
6. `L540`: the dashd and REST `acl` doors skip `auth.Delegate`. Must that
   close before step 6, or stay its own entry?

## Ties

[`41`](41-qemu-substrate.md) owns the substrate, the seam and the host
budget this changes; [`5/32`](../5/32-acl-unified.md) owns the row and the
evaluator; [`5/33`](../5/33-paths-roles.md) owns delegation and the
single-axis rule for guest capabilities; [`39`](39-gateway-owns-spend.md)
owns spend. `BUGS L513`, `L537`, `L538`, `L539`, `L540`.
