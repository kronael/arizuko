# crackbox

Sandbox + egress umbrella component. Two halves:

- **`egred`** — forward HTTP/HTTPS proxy daemon (shipped). Per-source-IP
  allowlist, admin API, transparent + forward modes. Same binary as
  `crackbox proxy serve`. See [`specs/6/9`](../specs/6/9-crackbox-standalone.md).
- **`pkg/host/`** — Go library for KVM/qemu sandbox lifecycle (planned;
  see [`specs/8/a`](../specs/6/12-crackbox-sandboxing.md)). Spawns VMs,
  manages privileges, ensures egred is up. Imported by
  [`sandd`](../specs/8/c-sandd.md) (arizuko-internal sandbox daemon)
  and by `crackbox run --kvm` for laptop one-shots.

The two halves ship together so `crackbox run` composes a
sandboxed-execution CLI with no extra dep. Today only the proxy half
is in production. The library half is next phase.

## Subcommands

```
crackbox proxy serve [--config <path>] [--listen :3128] [--admin :3129] [--transparent :3127]
crackbox run --allow <list> [--id <name>] [--image <img>] [--kvm] -- <cmd>...
crackbox state [--admin <url>]
crackbox host run --image <vm> [--memory 2G] -- <cmd>...   # planned
```

- `proxy serve` — long-running egred daemon. Functionally identical
  to invoking `egred` standalone. Forward proxy on `:3128`, admin API
  on `:3129` (`/v1/register`, `/v1/unregister`, `/v1/state`, `/health`),
  transparent-mode listener on `:3127`. matchHost is per-source-IP
  lookup.
- `run` — convenience wrapper. Creates a Docker network (or KVM
  bridge with `--kvm`), spawns egred on it, registers one allowlist
  for the to-be-run user container/VM, runs `<cmd>` with `HTTPS_PROXY`
  set, tears everything down on exit. Same daemon code; the registry
  just happens to have one entry.
- `state` — query a running daemon's registry.
- `host run` (planned) — spawn one KVM VM directly via `pkg/host/`
  for non-egress use cases. Same backend `sandd` will use.

## Transparent mode

Same daemon, two ways to receive traffic. Forward mode = client sets
`HTTPS_PROXY=http://crackbox:3128`. Transparent mode = client side runs
iptables `REDIRECT` to send port-80/443 traffic to crackbox's
`:3127`. Crackbox reads the pre-NAT destination via
`getsockopt(SO_ORIGINAL_DST)`, peeks SNI (443) or `Host:` (80), runs
the same per-source-IP `Allow()`, splices on success.

The transparent listener is enabled by default. It's idle when nothing
redirects to it, so binding costs nothing. Disable it in
`~/.crackboxrc` (or any config path below) with `transparent_listen = ""`,
or pass `--transparent ""` on the CLI.

Linux-only (uses Linux netfilter sockopts). v1 supports ports 80 and
443; other ports are rejected.

One-liner iptables example, redirecting outbound 443 from a single
test user `tester` to a local crackbox instance:

```sh
sudo iptables -t nat -A OUTPUT -p tcp --dport 443 \
  -m owner --uid-owner tester \
  -j REDIRECT --to-ports 3127
```

## Configuration file

Optional TOML at `~/.crackboxrc`, `$XDG_CONFIG_HOME/crackbox/crackbox.toml`,
or `/etc/crackbox.toml` (in that order). Override the path with
`--config`. Missing file = run on defaults.

```toml
[proxy]
listen = ":3128"
admin_listen = ":3129"
transparent_listen = ":3127"  # set to "" to disable

[admin]
secret = ""                   # bearer token; empty disables auth

[state]
path = ""                     # registry persistence; empty = RAM-only
```

Precedence: flags > env > config file > defaults.

## Standalone use

```sh
crackbox run --allow github.com,api.anthropic.com -- bash
crackbox run --image python:3 --allow pypi.org -- pip install requests
crackbox run --quiet --allow api.anthropic.com -- curl https://api.anthropic.com/
```

Default-deny is the trade. `crackbox run --allow ""` blocks everything;
`crackbox run --allow github.com -- curl https://example.com` returns 403.

## Layout

```
crackbox/
  cmd/
    crackbox/main.go          umbrella CLI: proxy serve / run / state / host (planned)
    egred/main.go             standalone egred binary (planned; thin wrapper around pkg/proxy)
  pkg/
    proxy/                    forward HTTP + CONNECT + transparent + splice (egred internals)
    host/                     KVM/qemu sandbox lifecycle (planned; spec 8/a)
    config/                   TOML config loader (search path + defaults)
    match/                    Host(allowlist, host) bool, validators
    admin/                    Registry + admin API handlers
    run/                      `crackbox run` orchestration
    client/                   admin HTTP client (consumed by sandd, by `crackbox run`)
  Dockerfile, Makefile
```

## Configuration

| Var                         | Default                 | Used by                                                        |
| --------------------------- | ----------------------- | -------------------------------------------------------------- |
| `CRACKBOX_PROXY_ADDR`       | `:3128`                 | `proxy serve` listen                                           |
| `CRACKBOX_ADMIN_ADDR`       | `:3129`                 | `proxy serve` admin                                            |
| `CRACKBOX_TRANSPARENT_ADDR` | `:3127`                 | `proxy serve` transparent listener; empty = disabled           |
| `CRACKBOX_ADMIN_SECRET`     | (unset)                 | bearer token for `/v1/register`+`/v1/unregister`; empty = open |
| `CRACKBOX_STATE_PATH`       | (unset)                 | persist registry to JSON file; empty = RAM-only                |
| `CRACKBOX_IMAGE`            | `crackbox:latest`       | `run` proxy image                                              |
| `CRACKBOX_SUBNET`           | `10.99.0.0/16`          | `run` Docker subnet                                            |
| `CRACKBOX_ADMIN`            | `http://localhost:3129` | `state`                                                        |

`CRACKBOX_ADMIN_SECRET` empty leaves mutating endpoints unauthenticated
and logs a warning. The same secret must be set on both the daemon and
each consumer (e.g. arizuko's `gated`). Read-only endpoints
(`/v1/state`, `/health`) never require auth.

`CRACKBOX_STATE_PATH` empty keeps the registry in memory; restarts drop
state. When set, every `Set`/`Remove` rewrites the file atomically and
startup reloads it. Corrupt or missing files reset to empty (logged as
warnings) — a stale snapshot can never block startup.

## Don't reinvent supervision

No idle-shutdown, no auto-restart. Daemon mode lifecycle is owned
by Docker compose / systemd. `crackbox run` lifecycle is owned by
the invoking shell.

## Reuse from origin crackbox

The proxy half (`pkg/match/Host`, `LooksLikeDomain`, `LooksLikeIP`,
`domainRegex`, test fixtures) is ported from
`/home/onvos/app/crackbox/internal/vm/{proxy,netfilter}.go`.

The host-library half (planned, `pkg/host/`) ports the prototype's
`internal/vm/launch.go` (qemu invocation, virtio-net, virtio-fs),
`internal/vm/network.go` (bridge + tap + iptables NAT), and
`internal/vm/secrets.go` (TLS-terminating placeholder injection).
See [`specs/6/12-crackbox-sandboxing.md`](../specs/6/12-crackbox-sandboxing.md).

## Orthogonality

```sh
grep -rE 'github\.com/[^/]+/arizuko/(store|core|gateway|api|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|gated)' crackbox/  # returns empty
```

The grep is owner-agnostic on purpose — `arizuko`'s `go.mod` carries
a stale `onvos/` owner while the canonical GitHub home is
`github.com/kronael/arizuko`. The orthogonality property is "no
arizuko-internal subpackage is imported," not "the owner string
matches X."

This component imports nothing from arizuko-internal packages.
Consumers (arizuko's gated, future tools) import
`<arizuko-module>/crackbox/pkg/...` or invoke the CLI.

Crackbox shares arizuko's single `go.mod` and stays that way —
orthogonality is enforced by the import graph (the grep above), not
by module separation. External users consume crackbox as a CLI or
Docker image, not as a Go library.
