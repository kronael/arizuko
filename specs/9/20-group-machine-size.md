---
status: draft
depends: [../6/41-qemu-substrate]
---

# specs/9/20 — each group sizes its own machine

A group's machine is the KVM guest that each of its turns runs in. This spec
lets the operator set that guest's memory, vCPUs and run timeout for one group,
with three keys in the group's `container_config`: `mem`, `smp`, `timeout`. A
group without a key runs at the instance default.

**Build when** a measured group needs more than 2 GB or 2 vCPUs. Until then
the instance default serves every group, and this spec records the decision
(owner, 2026-11-04), not work to start.

## The problem — measured at `5ceb13dc` and on hel1v5 2026-10-23

Every guest on an instance has one size, read from instance env at runed
start, and the host check counts one instance.

| Fact                                                     | Where                                                                | Measured                                                                                                                        |
| -------------------------------------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| One guest size: `RUNED_GUEST_MEM`, default `2G`          | `runed/qemuhost.go:129`, rendered at `runed/qemu.go:139-140`         | Peak guest memory 624–746 MiB over 11 turns on v0.70.8 (diary 2026-10-22); browser-heavy rhias peaks not yet measured.          |
| One vCPU count: `RUNED_GUEST_SMP`, default 2             | `runed/qemuhost.go:130`, `runed/qemu.go:138`                         | hel1v5 has 2 CPUs.                                                                                                              |
| One deadline: `RUNED_RUN_TIMEOUT`                        | `runed/manager.go:388-390` `turnTTL`                                 | —                                                                                                                               |
| The host check multiplies one size by one instance's cap | `runed/qemuhost.go:200-204`, `:313-333` `memoryFits`                 | `BUGS L513`: krons and sloth each pass with 2 × 2G; with ~9 GB of residents the host swapped 5 GB and guests booted in 20–30 s. |
| tmpfs sizes are constants sized against 2G               | `kernel/guest-init/main.go:108` upper `512m`, `:287` `/dev/shm` `1g` | At `-m 1G` the `/dev/shm` constant equals the whole guest.                                                                      |

## Where it fits

```
 operator
    │ arizuko apply
    ▼
┌───────────────────────────────────────────┐  RunRequest.container_config  ┌───────┐
│ groups.container_config                   ├──────────────────────────────►│ runed │
│ {Mounts, mem, smp, timeout} (changed)     │  opaque map, routd forwards   └───┬───┘
└───────────────────────────────────────────┘                                   │
                       ┌─────────────────────────────┬─────────────────────────┤
                       ▼                             ▼                         ▼
             ┌───────────────────┐       ┌─────────────────────┐   ┌──────────────────────┐
             │ admit: mem vs     │       │ turnTTL: min(       │   │ Turn.Mem, Turn.CPUs  │
             │ MemAvailable      │       │ timeout, run        │   │ (new) → qemuArgs     │
             │ (changed)         │       │ ceiling) (changed)  │   │ -m, -smp (changed)   │
             └───────────────────┘       └─────────────────────┘   └──────────┬───────────┘
                                                                              ▼
                                                                  ┌───────────────────────┐
                                                                  │ guest-init: upper 25%,│
                                                                  │ /dev/shm 50% (changed)│
                                                                  └───────────────────────┘
```

The routd→runed wire does not change: `RunRequest.ContainerConfig`
(`runed/api/v1/types.go:33`) already carries the group's document as an opaque
map. No `acl` row type, no resolver and no new resource.

```yaml
- path: core/types.go (changed)
  job: the group's container document
  names:
    - 'GroupConfig{Mounts, Mem string `mem`, SMP int `smp`, Timeout string `timeout`}'
    - 'ParseGuestMem(s string) (int64, error)  # moved from runed/qemuhost.go:335'
  never: holds an instance default; empty means "the instance's"
- path: resreg/resources/groups.go (changed)
  job: the one write door validates the three keys
  names:
    - 'BeforeInsert refuses a container_config whose mem, smp or timeout does not parse'
  never: bounds a value; the host check does
- path: runed/manager.go (changed)
  job: admits a run against host memory and arms its deadline
  names:
    - "admit: Busy when the turn's mem exceeds MemAvailable, terminal above MemTotal"
    - 'turnTTL(req) = min(timeout, RUNED_RUN_TIMEOUT)'
  never: raises the deadline above RUNED_RUN_TIMEOUT
- path: runed/runtime.go (changed — open question 1)
  job: the turn envelope
  names:
    - 'Turn.Mem int64, Turn.CPUs int'
  never: carries a substrate's own state
- path: runed/qemu.go (changed)
  job: renders QEMU's argv from the turn
  names:
    - 'qemuArgs: -m, memfd size=, -smp from Turn.Mem, Turn.CPUs'
  never: reads RUNED_GUEST_MEM or RUNED_GUEST_SMP
- path: runed/qemuhost.go (changed)
  job: start-time preconditions
  names:
    - 'memoryFits (removed); RUNED_GUEST_MEM must fit MemTotal'
  never: multiplies a size by MAX_CONCURRENT_CONTAINERS
- path: kernel/guest-init/main.go (changed)
  job: mounts the guest's tmpfs
  names:
    - 'upperSize = size=25%; /dev/shm size=50%'
  never: reads a size from the host
```

## The size is three keys the operator writes

```go
// core/types.go:106-108 — current
type GroupConfig struct {
	Mounts []Mount
}
```

Proposed:

```go
type GroupConfig struct {
	Mounts  []Mount
	Mem     string `json:"mem,omitempty"`     // QEMU -m spelling: 1G, 1536M, 2048; "" = RUNED_GUEST_MEM
	SMP     int    `json:"smp,omitempty"`     // vCPUs; 0 = RUNED_GUEST_SMP
	Timeout string `json:"timeout,omitempty"` // Go duration; "" = RUNED_RUN_TIMEOUT
}
```

`mem` takes QEMU's `-m` spelling because `RUNED_GUEST_MEM` does, and one parser
reads both: `parseGuestMem` (`runed/qemuhost.go:335-353`) moves to `core` so
the groups write door and runed share it. `timeout` takes a Go duration, as
`RUNED_RUN_TIMEOUT` does.

**Only the operator writes them.** The operator's door is `arizuko apply`,
which stores `container_config` as the raw JSON string
(`resreg/resources/groups.go:17`). The `groups` resource exposes `register`,
`list` and `get` only (`resreg/resources/groups.go:28-32`); `register` takes
`folder` and `jid` (`:66-69`), and dashd does not render or write the document
(`dashd/groups_admin.go:264`). So an agent cannot set its own size.

**Its one caveat** is `BUGS L656`: `register_group` on a folder that already
exists upserts it with an empty `container_config`
(`routd/groups_resource.go:79`, `store/groups.go:153-166`), and an onboarded
world's agent may name its own folder. That resets the group to the instance
default, which enlarges a group the operator set below it. This spec does not
build until `L656` closes.

## The deadline only lowers

```go
// runed/manager.go:388-390 — current
// turnTTL is the one deadline: the run ceiling, RUNED_RUN_TIMEOUT. A group's
// container_config carries no timeout (specs/9/20-group-machine-size.md).
func (m *Manager) turnTTL(runedv1.RunRequest) time.Duration { return m.runTTL }
```

Proposed: `turnTTL(req)` returns the group's `timeout` when it is set and below
`m.runTTL`, else `m.runTTL`. `ARIZUKO_QUERY_TIMEOUT_MS` keeps following the
armed value (`runed/render.go:164-173`).

Why only lower: routd sizes its dispatch deadline
(`routd/cmd/routd/main.go:124-130`), its crash sweep (`routd/loop.go:431-432`)
and the gateway key's TTL (`routd/dispatch.go:445`) from the one instance
value. A group above it would need a deadline on the turn row.

## The guest's tmpfs follow its memory

```go
// kernel/guest-init/main.go:108 — current
upperSize = "size=512m"
// kernel/guest-init/main.go:287 — current
{"tmpfs", "/dev/shm", "tmpfs", "size=1g"},
```

Proposed: `size=25%` for the overlay upper and `size=50%` for `/dev/shm`.
tmpfs reads a `%` size as a share of the RAM the guest kernel sees, so the
ratio needs no carrier from the host. At the 2G default the two give about
today's 512m and 1g, so a group without `mem` sees no change. The change
rebuilds the kernel artifact (`KERNEL_KEY` hashes `guest-init`, `Makefile:51`).

## The host check counts every instance's real guests

`memoryFits` checks one size times one instance's cap, once, at start. With
per-group sizes no product at start bounds anything, and the measured swap
came from residents the product never counts (`L513`). So the check moves to
admission and reads the host's own number:

```go
// runed/qemuhost.go:200-204 — current
if total, err := hostMemTotal(); err != nil {
	miss("%s", err)
} else if err := memoryFits(p.Mem, cfg.MaxContainers, total); err != nil {
	miss("%s", err)
}
```

Proposed:

- In `Manager.admit` (`runed/manager.go:198`), after the count cap
  (`:215-225`) and under the same `m.mu`, an agent run reads `MemAvailable`
  from `/proc/meminfo`. When the turn's `mem` exceeds it, the run is `Busy`:
  routd retries it later and the folder's breaker does not count it, and runed
  logs `guest memory unavailable` with both numbers. When `mem` exceeds
  `MemTotal`, no wait can fit it: the run is terminal and the chat gets the
  notice.
- `MemAvailable` is the kernel's count for the whole host. It already
  subtracts what the other instances' guests and every resident daemon hold,
  so the guests of every instance are summed at their real size, with no state
  shared between instances. runed reads the host's `/proc/meminfo` as
  `hostMemTotal` does today (`runed/qemuhost.go:356`).
- At start, runed checks only that `RUNED_GUEST_MEM` fits `MemTotal`. The
  tap-subnet loop (`runed/qemuhost.go:210-216`) stays, because
  `MAX_CONCURRENT_CONTAINERS` still sizes the tap subnet, the CID window and
  routd's dispatch slots.

The caveat: the check sees memory as it is, not as it becomes. A guest
admitted while another is still small meets that guest's growth later. The
balloon's free-page reporting (`runed/qemu.go:165-170`) keeps `MemAvailable`
close to what guests hold, and the count cap still bounds this instance's own
guests. A host that is short of memory holds turns instead of swapping, and
says so in the log.

## Why not a grant

The first draft made each size an `acl` row `cap:<name>=<value>`. The owner
chose `container_config` (2026-11-04): a size is a fact about one group that
only the operator sets, so it needs no delegation, no resolver, no action
lattice and no new `RunRequest` field, and `container_config` already reaches
runed.

## Done

Step 1, `0cd176327`: the deadline is `RUNED_RUN_TIMEOUT` alone.
`CONTAINER_TIMEOUT`, `IDLE_TIMEOUT`, `envDur`, `containerGracePeriod` and
`GroupConfig.Timeout` are gone. `GroupConfig.Timeout` floored every guest
ceiling at `IDLE_TIMEOUT` + 30s, above the run ceiling, so it never moved a
turn. The `timeout` key above has no floor and no second instance default.

## Open questions

1. Per-turn `mem` and `smp` must reach `qemuArgs`, and today it reads them
   from the substrate's own config (`runed/qemu.go:353`), not from the turn.
   The wire needs no change, but `runed.Turn` (`runed/runtime.go:46`) gains
   `Mem int64` and `CPUs int`, a field a substrate interprets, which root
   `CLAUDE.md` ("A port is two methods and stays two") reserves for an
   approved plan. Recommendation: approve the two fields, named for the turn
   as `specs/6/41-qemu-substrate.md` §"The seam" asks. No other carrier
   reaches the substrate without it interpreting `Turn.Env`.
2. The per-turn check against `MemAvailable` holds a turn (`Busy`) on a host
   short of memory, where today the guest boots into swap. Recommendation:
   accept; a held turn is visible in the log, swap is not.
