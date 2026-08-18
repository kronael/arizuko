# crackbox

A local-development command. It runs something in a sandbox that can only
reach the hosts you name, by composing two pieces this repo also uses
separately:

- [`egress/`](../egress/) — the forward proxy runed starts per spawn.
- [`sandbox/`](../sandbox/) — the KVM/qemu VM lifecycle library.

**Production never runs crackbox.** `runed` starts an `egress` proxy per
spawn and stops it with the turn; crackbox is how a developer runs the same
pieces on their own machine — a sandboxed VM with filtered egress, or a
container with filtered egress, without bringing up the full compose stack.
Spec: [`specs/6/24`](../specs/6/24-crackbox-lightest.md).

## Use

```sh
crackbox run --allow github.com,api.anthropic.com -- curl https://github.com
crackbox run --image python:3 --allow pypi.org -- pip install requests
crackbox run --kvm --data-dir /var/lib/crackbox --allow github.com -- uname -a
```

Default-deny is the trade. `--allow ""` blocks everything;
`crackbox run --allow github.com -- curl https://example.com` gets a 403
from the proxy.

## The two modes, and where each enforces

| Mode    | Sandbox          | What stops the traffic                                                               |
| ------- | ---------------- | ------------------------------------------------------------------------------------ |
| default | docker container | the container's `--internal` network has no route out; its own proxy is the only one |
| `--kvm` | qemu VM          | netfilter rules on the VM's tap, from the same allowlist                             |

In container mode the proxy is not what confines the sandbox — the missing
route is. The sandbox network is created `--internal`, the proxy is the only
thing attached to both it and an uplink, and the proxy answers only the
sandbox's address. Take any one of those away and the filtering is advisory.

Startup order matters and is enforced: the proxy is started first, polled on
`/health`, and only then does the sandbox exist. A sandbox is never started
against a proxy that is not answering.

## Flags

```
crackbox run --allow <hosts> [--image <img>] [--subnet <cidr>] [--uplink <net>] -- <cmd>...
crackbox run --kvm --data-dir <dir> --allow <hosts> [-- <cmd>...]
```

| Flag            | Default                 | Meaning                                       |
| --------------- | ----------------------- | --------------------------------------------- |
| `--allow`       | (required)              | comma-separated allowlist; `*` allows all     |
| `--image`       | `alpine:3.20`           | image the command runs in                     |
| `--proxy-image` | `arizuko-egress:latest` | image holding the `egress` binary             |
| `--subnet`      | `10.88.0.0/24`          | sandbox network CIDR                          |
| `--uplink`      | `bridge`                | docker network giving the proxy its route out |
| `--kvm`         | off                     | use a qemu VM (needs qemu + root)             |
| `--data-dir`    | `$CRACKBOX_DATA_DIR`    | VM state root (`--kvm` only)                  |

Build the proxy image first: `make images` at the repo root, or
`docker build -t arizuko-egress -f egress/Dockerfile .`.

## What this used to be

Until 2026-08-18 crackbox was a proxy daemon with a registry of
`(source_ip, allowlist)` rows, an admin API, a client library, transparent
interception and a DNS filter — everything a SHARED proxy needs to tell its
tenants apart. One proxy per sandbox needs none of it, so all of it is gone
(spec `6/24`). There is no `crackbox proxy serve`, no `crackbox state`, no
`~/.crackboxrc`. For the proxy alone, run `egress`.

## Layout

```
crackbox/
  cmd/crackbox/main.go   the CLI
  pkg/run/               compose egress + a sandbox, run the command, tear down
  Makefile
```

## Reuse from origin crackbox

The proxy's matching (`egress/match`) is ported from
`/home/onvos/app/crackbox/internal/vm/{proxy,netfilter}.go`. The VM library
(`sandbox/`) is ported from that prototype's `internal/vm/launch.go` (qemu,
virtio-net, virtio-fs) and `internal/vm/network.go` (bridge + tap + NAT).
See [`specs/6/9`](../specs/6/9-crackbox-sandboxing.md).

## Orthogonality

```sh
grep -rE 'github\.com/[^/]+/arizuko/(store|core|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|auth|audit|resreg|obs|routd|runed|authd)' crackbox/ egress/ sandbox/  # returns empty
```

None of the three imports an arizuko-internal package. They share arizuko's
single `go.mod` and stay that way: the import graph enforces orthogonality,
not module boundaries, and the people who run crackbox have already cloned
the repo.
