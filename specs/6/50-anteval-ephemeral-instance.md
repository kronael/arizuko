---
status: partial
depends:
  [
    ../5/9-agent-capability-eval,
    41-qemu-substrate,
    42-dynamo-over-postgres,
    23-llm-gateway,
  ]
---

# specs/6/50 — anteval runs against an instance it creates

anteval runs only when the owner starts it by hand; no deploy gate, timer or
hook starts it.

`anteval run` creates a disposable arizuko instance from the release image,
runs its cases against that instance, and removes the instance when the run
ends. It never reaches an instance that serves users.

The CLI lifecycle and fake-based tests are implemented.
The device, artifact, image, memory and allocation checks run before creation.
Live acceptance remains incomplete.

## Remaining

- Prove guest callback reachability and disposable smoke/product runs on an isolated external test store, including cleanup after success, failure, partial startup and interruption (`anteval/cmd/anteval/lifecycle_test.go`, `anteval/cases/anteval.toml`; L379).

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

## Decision — anteval run uses the arizuko CLI lifecycle

`anteval run` is the one eval entry point. It invokes the `arizuko` binary
for instance lifecycle and setup, then calls its existing `run.Drive` cases.
This adds fewer names than an `arizuko eval` subcommand: `anteval run`
already owns case selection and reports. The lifecycle is also used by the
arizuko CLI for ordinary instances; anteval owns no second Compose generator,
startup path or removal implementation.

```text
anteval run (changed)
    │
    ├─► arizuko create ─► isolated instance
    ├─► arizuko store enroll ─► isolated store account
    ├─► arizuko run ────► generate + Compose startup
    ├─► arizuko group / network / token ─► eval setup
    ├─► run.Drive ──────► public HTTP / REST / MCP cases
    └─► arizuko rm ─► instance removal at exit
```

### CLI lifecycle and verification

`anteval/cmd/anteval/main.go` selects cases and handles signals.
`anteval/cmd/anteval/lifecycle.go` composes the existing CLI commands.
`anteval/cmd/anteval/host_preflight.go` checks devices, artifacts, cached images and host allocations. The allocation checks are point-in-time.
`create` seeds files; `store enroll` enrolls the new account; foreground `run`
generates Compose and starts services. After the first boot creates the owners'
tables, `package add catalog:litellm` installs the gateway. The runner restarts
its foreground child and waits for the required services' healthchecks.
Setup uses `group`, `apply`, `network` and `token`, then calls `run.Drive`.
The selected product is installed for the eval group through `package sync`.

`cmd/arizuko/rm.go` owns instance removal. Anteval stops and reaps its child
before calling `rm` with an uncanceled cleanup context. Cleanup runs on success,
case failure, setup failure after creation, and handled interruption.
It completes before the entry point exits, including paths that call `os.Exit`.

### One release, one isolated instance

The CLI creates from the selected release image and its packaged assets.
`APP_SRC_DEV` stays off. Every CLI call supplies the new instance name
explicitly and uses the run's private `ARIZUKO_DATA_DIR` base.
Inherited instance defaults, endpoints and credentials cannot select a target.
The run refuses an existing data directory or Compose project.

| Part              | Contract                                                                                                                                                                                                                                                                               |
| ----------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Identity and data | `anteval-<run id>` is the explicit instance name. The CLI resolves its data directory under `ARIZUKO_DATA_DIR` and declares the Compose project and network identities.                                                                                                                |
| Services          | Use the release's normal package installation and Compose generation.                                                                                                                                                                                                                  |
| Store             | Use one isolated account on an explicitly configured external test store, through `arizuko store enroll`. The account owns all eval tables and credentials. The server serves no user instance and follows `43-shared-store-server.md`; no private store package or dev-store variant. |
| Configuration     | Write the new instance's `.env` using existing CLI configuration keys. Allocate separate host ports and a declared, nonoverlapping `RUNED_VSOCK_CID_BASE` / `RUNED_VSOCK_PORT_BASE` window. Add no eval-specific copies of these keys.                                                 |
| Release assets    | Rootfs and kernel assets from the release are read-only. Mutable guest state belongs to the new instance.                                                                                                                                                                              |
| Model and gateway | Supply the model credential explicitly. Install the normal gateway package for case cost and settle checks. Never copy another instance's credential.                                                                                                                                  |
| Eval setup        | Use `group add`, `group grant`, `network allow` and `token issue bearer` on the new instance. Give the selected cases their grants and a reachable callback sink.                                                                                                                      |
| Capacity          | Set `MAX_CONCURRENT_CONTAINERS=1`. Require `RUNED_GUEST_MEM`. Refuse startup without KVM, the release images, or available memory for 1536 MiB of services and gateway plus one configured guest.                                                                                      |

Store enrollment and removal belong to the ordinary CLI lifecycle.
They must work without anteval's cases. anteval invokes those commands and
passes the resulting public addresses, folder and token to `run.Drive`.
The current target, `--api`, `--mcp`, `--token`, `--chat` and `--folder`
inputs become setup outputs. No option accepts an existing instance.
Case selectors, sink settings and report outputs remain anteval's interface.

### Cases stay black-box

`anteval/README.md` defines cases as observations over public surfaces.
`anteval/pkg/run/target.go` and `mcpread.go` carry those HTTP, REST and MCP calls.
Cases neither read instance files nor import daemon or storage packages.
Only lifecycle setup invokes the operator CLI.

The MCP reader uses the folder supplied by setup. Anteval imports no daemon
or storage package and carries no JID parser.

Product cases use `arizuko create --product`; the same lifecycle runs them.
The manual-only rule remains: no deploy gate, timer, hook or CI starts an eval.
The run never reads, mutates, drains or removes a serving instance.
A disposable run certifies the release, not another instance's data.

### Removal has one owner

All mutable state belongs to the created project, data directory and store account.
`arizuko rm` removes that exact instance, including partially created state.
It removes only that instance's store account, never the external server.
It never sweeps instances by an `anteval` name prefix.
A hard crash can leave resources behind; the operator uses the same CLI
removal command for that recorded instance before retrying.
Reports live outside the disposable directory.

Acceptance must exercise success, case failure, partial startup and interruption.
Each path must leave no owned services, guests, store account, volumes, networks or data directory.
Tests must also prove that existing targets are refused and unrelated instances
remain untouched. `anteval/cmd/anteval/lifecycle_test.go` exercises these paths
through fake CLI commands, including a real SIGINT and a fake foreground child.
The tests start no instance and claim no live capability result.

## Ties

`BUGS L72` (the leak, and the entry this spec answers), `L511` (what the
removal of the leak cost), `L512` (two egress cases that the documented
setup cannot pass), `L379` (the sink a guest cannot reach — this spec does
not change it, and the run's egress allowlist waits on its decision), `L188`
(the case backfill that `L72` blocks).
