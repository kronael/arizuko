---
status: draft
depends:
  [
    ../5/9-agent-capability-eval,
    41-qemu-substrate,
    42-dynamo-over-postgres,
    23-llm-gateway,
  ]
---

# specs/6/50 — anteval runs against an instance it creates

`anteval run` creates a disposable arizuko instance from the release image,
runs its cases against that instance, and removes the instance when the run
ends. It never reaches an instance that serves users.

The owner's ruling, 2026-10-21: "anteval should not even touch live
instances; it should spawn its own setup in a disposable qemu or so … or at
least like testcontainers be ephemeral. it's a test." This spec chooses the
shape. The direction is ruled; the shape needs sign-off, so the status is
`draft`.

## Problem — measured on krons 2026-10-21

Every anteval run wrote to krons, and nothing removed what it wrote. The
chain, with the code that makes each link:

| Link                                                                                                                                              | Where                                                                                                                                      | Measured                                                                                                       |
| ------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------- |
| Two smoke cases tell the agent to `register_group` a child named after the per-case nonce: `eval/<run>-child-delegate`, `eval/<run>-priv-grant-g` | `anteval/cases/anteval.toml:73`, `:106`; nonce `anteval/pkg/run/run.go:178`, run id `anteval/cmd/anteval/main.go:85`                       | 47 child groups on 2026-10-21; the first `/migrate` row to one is dated 2026-07-11                             |
| The driver has no teardown, and the groups resource has no delete for it to call                                                                  | `Drive` is sink, reset, case, settle (`anteval/pkg/run/run.go:66-104`); `routd/groups_resource.go:45-81` serves `list` and `register` only | `grep` finds no delete in `anteval/`                                                                           |
| The documents say a teardown exists                                                                                                               | `specs/5/9-agent-capability-eval.md` §"Approach", `anteval/README.md:128`                                                                  | —                                                                                                              |
| Every release enqueues `/migrate` for EVERY group behind `MIGRATION_VERSION`, eval children included                                              | `routd/loop.go:484-518`                                                                                                                    | 1001 `/migrate` rows to the 47 children since 2026-07-11; on 2026-10-21, 87 of 129 spawns and 11.5 guest-hours |
| The entry that recorded it in August was not in the queue a person reads                                                                          | `L72`'s heading said "proposal", which `tests/bugsindex/index_test.go:39` does not read as open                                            | —                                                                                                              |

The run left more than groups on the instance. Measured on krons at the same
time, after the 47 groups were removed:

| What                                                                             | Where it stays                               | Count           |
| -------------------------------------------------------------------------------- | -------------------------------------------- | --------------- |
| Webhook and chat-link tokens, no expiry                                          | `route_tokens`, owner `eval`                 | 28              |
| Per-case topic sessions                                                          | `sessions`, folder `eval`                    | 190             |
| Chat rows for case JIDs (`web:eval/<nonce>`, `hook:eval/<nonce>`, child folders) | `chats`                                      | 97              |
| Skills the `self-skill` case built (`BUGS L153`)                                 | `groups/eval/.claude/skills/`                | 21              |
| Published pages                                                                  | `web/pub/eval/`, `web/priv/eval/`            | 58 and 25 files |
| A `/priv` web route                                                              | `web_routes`                                 | 1               |
| Folder egress `*`, inherited by every child                                      | `network_rules` (`eval *`, since 2026-07-11) | 1               |
| Docker networks from the Docker substrate                                        | host                                         | 48              |

The removal itself had a cost, which `BUGS L511` records: the children's
queued `/migrate` rows became 24 onboarding rows and 23 pairing links, and
three turns ran after their folder was deleted.

A run against a live instance also measures the instance and not the
release. The same build scored 5/9 during a migration storm and 8/9 after it
(`.claude/skills/eval/gates.md` § "Post-deploy gates"), because the eval
cases waited behind every other folder's turns.

## Decision — a full instance, made by the operator's own commands

`anteval run` creates the instance with the commands an operator uses:
`arizuko create`, `arizuko generate`, `docker compose up`, then `arizuko
group add`, `grant`, `network allow` and `token issue bearer` for the eval
folder. anteval executes the `arizuko` binary and `docker`; it imports no
arizuko package, so it stays the black-box prober of `5/9-agent-capability-eval`. At exit it runs
`docker compose down --volumes` for the project and deletes the data
directory it made.

Why this shape: anteval is the only check that drives a real model through
the shipped image — compose generation, proxyd, webd, routd, the KVM
substrate and the guest rootfs together. Only an instance made by the
release's own declaration (`compose/compose.go`) tests that. The cost is the
caveat below, and it is the one that would change the decision.

**Caveat — the host budget.** One instance holds 1.7 to 3.0 GB with no guest
running (measured 2026-10-21: krons, 17 containers, 2460 MiB, of which runed
held 720 MiB with two guests up; sloth, 17 containers, 3042 MiB, no guest),
plus one guest at `RUNED_GUEST_MEM`, 2 GB by default. hel1v5 has 15 GB and 2
vCPUs, and had 3 GB available with krons and sloth up. So the run does not
fit on hel1v5 beside two live instances. The run refuses to start
when `MemAvailable` is below the instance's measured footprint plus one
guest, and says the number. The run host is any host with `/dev/kvm` and the
release images.

### What the instance is made of (proposed)

| Part              | Decision                                                                                                                                                                                                           | Why                                                                                                                                                                                                                                                                            |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Data directory    | `ARIZUKO_DATA_DIR=<run base>`, name `anteval-<run id>` (`cmd/arizuko/main.go:216-229`)                                                                                                                             | the CLI already takes the base; the name sets the compose project, containers and networks, never derived (root `CLAUDE.md`, "Identity is configured")                                                                                                                         |
| Services          | the core fleet that `generate` writes with no adapter fragment, plus the store and the gateway below                                                                                                               | the catalog's cases use web chats, webhooks and `/pub`; no case needs an adapter                                                                                                                                                                                               |
| Store             | ExtendDB's development build (`objstore/dynstore/dyntest/dyntest.go:32`) as a catalog fragment beside `template/services/extenddb.yml`, every daemon on the dev credential (`objstore/dynstore/dynstore.go:31-32`) | the deployment store needs a person to mint keys (`BUGS L319`); the dev build needs none. A green on it proves nothing about the PostgreSQL engine, and anteval does not certify the engine: `objstore/conformance` does                                                       |
| Model gateway     | the `litellm` package (`template/services/litellm.yml`, spec `6/23-llm-gateway`)                                                                                                                                   | `settle` (`anteval/pkg/run/run.go:159-174`) and `max_tokens` read the gateway's meter; without it every case waits the full 180 s bound. The gateway's memory is not measured on any host (no live instance runs it, `BUGS L131`), so its footprint is part of the start check |
| Model credential  | given to the run explicitly and written into the new `.env`; never read from another instance                                                                                                                      | a run must not copy a serving instance's secret; the backend under test (`ARIZUKO_BACKEND`) chooses which credential                                                                                                                                                           |
| vsock window      | `RUNED_VSOCK_CID_BASE` and `RUNED_VSOCK_PORT_BASE` set to a window reserved for anteval                                                                                                                            | the CID and port space is the host's (`BUGS L378`); nothing detects an overlap, so the window is declared, not found                                                                                                                                                           |
| Guest taps        | no setting                                                                                                                                                                                                         | measured 2026-10-21 with two guests running: zero tap or tun links in the host namespace; they live in runed's container                                                                                                                                                       |
| Rootfs and kernel | the trees the release already exported, read-only                                                                                                                                                                  | `compose/compose.go:1232-1237` already shares one tree per image across instances; a second 8 GB export buys nothing                                                                                                                                                           |
| Host ports        | a free port for each host port the fleet publishes                                                                                                                                                                 | two instances cannot share one; `BUGS L392` names the ports and the one that is a code default rather than a `.env` key                                                                                                                                                        |
| Run slots         | `MAX_CONCURRENT_CONTAINERS=1`                                                                                                                                                                                      | the cases run one at a time on one folder (`anteval/README.md` § Run)                                                                                                                                                                                                          |
| Eval folder       | the grants of `.claude/skills/eval/gates.md` § "Bringing `anteval` up", plus `web:publish` on `eval/**` with the grant option (`BUGS L308`)                                                                        | the instance holds nothing else, so the grants measure the agent and not a grant table                                                                                                                                                                                         |

### What the run owns, and where it goes at exit

Everything the run makes is under its compose project and its data
directory, so one `down` and one delete remove all of it — groups, tokens,
sessions, pages, skills and networks included. A run that crashes leaves both
behind; the next run finds them by the project label and removes them before
it starts, as `dyntest` reaps a dead process's store
(`objstore/dynstore/dyntest/dyntest.go:257`). The data directory is
root-owned after a turn, so the delete runs with the privilege the run
already needs for `docker`.

### What changes elsewhere when this ships

- `5/9-agent-capability-eval` §"WHY a shipped component": the check runs
  against the release image, not "a live deployed instance". The `<target>`,
  `--api`, `--token`, `--chat` and `--folder` arguments become outputs of the
  bring-up.
- Product cases (`5/9-agent-capability-eval` §"A product carries its own cases") run against an
  instance made with `arizuko create --product <name>`, never a live group.
- `.claude/skills/eval/gates.md` gate 5 stops needing the drain wait, the
  bearer mint and the sink-gateway lookup on the live instance.
- No port changes. `runed.Runtime` stays `Run` + `Kill` (spec `6/41-qemu-substrate`
  §"The seam"); this spec adds no method and no `Turn` field.

**What is given up.** A run on krons once found a krons-only fault: the
`/hook/` route was never seeded there (`5/9-agent-capability-eval` §"What the first live run
proved"). A disposable instance cannot find a fault in another instance's
data. That class belongs to gates 1-4 and the `/eval` checks
(`.claude/skills/eval/checks.md`), which read the live instance and write
nothing to it.

## Why not the other shapes

**runed's substrate as a library inside anteval.** The substrate starts a
guest from a `Turn` that runed's renderer makes from a run request (spec
`6/41-qemu-substrate` §"The seam"). The guest's tools are served by routd over the turn's
MCP socket, and its pages by webd and proxyd. With the substrate alone, no
case in the catalog can run: each one calls a platform tool. To run them,
anteval has to rebuild routd, webd and proxyd around the guest, which is the
shape below. It also makes anteval import runed, which `5/9-agent-capability-eval` §"Approach"
refuses, and it puts pressure on the port to export the renderer.

**An in-process Go assembly, testcontainers style.** `tests/split_federation_test.go`
already wires authd, routd and runed in one process with `FakeRuntime`;
putting the real QEMU runtime, webd and proxyd into it gives a full turn
without compose. It is a second declaration of the fleet: the daemons'
wiring, env and mounts are declared once, in `compose/compose.go`, and the
assembly would state them again and drift from them (root `CLAUDE.md`, "One
declaration, many readers"). It also tests the Go source and not the shipped
images. The testcontainers property — disposable, reaped by label, removed at
exit — is taken into the decision above without the second declaration.

**A disposable VM that holds the whole instance.** hel1v5 is a KVM guest
(`systemd-detect-virt` answers `kvm`) and already nests one level for turn
guests (`kvm_amd nested=1`). An instance inside a VM puts each turn guest a
third level down. The boot time of a turn guest is measured with one level of
nesting (`BUGS L448`) and not with two.

## What the existing tests already give, and what they do not

| Test                                               | Gives                                                                    | Does not give                  |
| -------------------------------------------------- | ------------------------------------------------------------------------ | ------------------------------ |
| `dyntest` (`objstore/dynstore/dyntest/dyntest.go`) | a store per test binary, removed at exit, reaped by pid label            | anything above the store       |
| `tests/split_federation_test.go`                   | authd, routd and runed over real HTTP and the pinned run contract        | a model, a guest, webd, proxyd |
| `runed/qemu_turn_boot_test.go`                     | a real guest booted from a test rootfs, the MCP socket, the egress proxy | routd's tools, a model         |
| `make test-e2e` (`Makefile:236-244`)               | webd's route-token surface                                               | a turn                         |

None of them drives a real model through the release image. That is the one
fact anteval measures, and it is why anteval needs a real instance at all.

## Ties

`BUGS L72` (the leak, and the entry this spec answers), `L511` (what the
removal of the leak cost), `L512` (two egress cases that the documented
setup cannot pass), `L379` (the sink a guest cannot reach — this spec does
not change it, and the run's egress allowlist waits on its decision), `L188`
(the case backfill that `L72` blocks).
