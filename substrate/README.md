# substrate

What an execution substrate can do, declared as data. `runed` builds the one
substrate at start and logs what it delivers (`runed/README.md` § The turn
substrate).

## Declared is the ceiling, delivered is the fact

- `Profile` is what a substrate declares: its backend, where writes persist,
  whether a workload takes more than one command, and how egress is filtered.
- `Effective` pairs that declaration with what one configuration delivers,
  and `Reason` names what is absent when the delivery falls short.
- Delivered equals declared, or runed does not start. `NewQemuRuntime`
  answers `Effective{Declared: qemuProfile, Delivered: qemuProfile}` only after
  every precondition holds, and a missing proxy binary or an unclampable
  `virtiofsd` is a refusal at start. A feature that quietly does not happen is
  the failure this package exists to end.

runed logs both on its `substrate` line at start, and `/dash/runed/` shows
them.

## The package imports nothing

Every `Profile` field is a bool or an enum, and the package has no import. A
field that held a folder, a grant or an allowlist could carry policy, and
policy is arizuko's, never a substrate's. That is also why the package can
ship outside arizuko (`tests/components/imports_test.go`).

| Field         | Values                          | The KVM substrate                     |
| ------------- | ------------------------------- | ------------------------------------- |
| `Backend`     | `kvm`                           | `kvm`                                 |
| `Persistence` | `mounted_home`, `resident_disk` | `mounted_home`                        |
| `Sessions`    | bool                            | false: one process from start to exit |
| `Egress`      | `none`, `address`, `domain`     | `domain`: names matched at CONNECT    |
