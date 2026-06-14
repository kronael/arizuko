# crackbox

Egress-filtering proxy + KVM sandbox library. Two halves:

- **Proxy** (`crackbox proxy serve`) — forward HTTP/HTTPS proxy with
  per-source-IP allowlists. Admin API, transparent + forward modes, DNS
  interception. See [`specs/11/9`](../specs/11/9-crackbox-standalone.md)
  - [`specs/11/15`](../specs/11/15-crackbox-dns-filter.md).
- **`pkg/host/`** — Go library for KVM/qemu sandbox lifecycle (shipped;
  see [`specs/11/12`](../specs/11/12-crackbox-sandboxing.md)). Spawns VMs,
  manages privileges, integrates proxy registration. Imported by
  arizuko's container runner and by `crackbox run --kvm` for standalone use.

Both halves ship together. Proxy is production; host library is complete but
not yet wired into arizuko's spawn path.

## Subcommands

```
crackbox proxy serve [--config <path>] [--listen :3128] [--admin :3129] \
                      [--transparent :3127] [--dns-listen :53] [--dns-upstream 1.1.1.1:53]
crackbox run --allow <list> [--id <name>] [--image <img>] [--kvm] -- <cmd>...
crackbox state [--admin <url>]
```

- `proxy serve` — proxy daemon. Forward mode on `:3128`, transparent on
  `:3127`, DNS on `:53` (all configurable). Admin API on `:3129`
  (`/v1/register`, `/v1/unregister`, `/v1/state`, `/health`). Per-source-IP
  allowlist enforced at HTTP/CONNECT + DNS layers.
- `run` — one-shot wrapper. Creates a Docker network (or KVM bridge with
  `--kvm`), spawns proxy, registers one allowlist, runs `<cmd>` with
  `HTTPS_PROXY` set, tears down on exit.
- `state` — query running daemon's registry.

## Transparent mode + DNS interception

Three enforcement layers, same allowlist:

1. **Forward proxy** (`:3128`) — client sets `HTTPS_PROXY`, crackbox checks
   `Allow(src-ip, dest-host)`, splices or 403.
2. **Transparent proxy** (`:3127`) — client iptables redirects 80/443,
   crackbox reads `SO_ORIGINAL_DST`, peeks SNI/Host, same `Allow()`.
3. **DNS filter** (`:53`) — client resolver points at crackbox, queries for
   non-allowed hosts return NXDOMAIN. Allowed queries forward to upstream.

Transparent + DNS are optional (disable with empty listen addr). Forward is
always on. All three layers use the same per-source-IP registry.

Linux-only (uses `getsockopt(SO_ORIGINAL_DST)`). Transparent supports ports
80/443; other ports rejected.

iptables example (redirect 443 from user `tester` to crackbox):

```sh
sudo iptables -t nat -A OUTPUT -p tcp --dport 443 \
  -m owner --uid-owner tester -j REDIRECT --to-ports 3127
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
dns_listen = ":53"            # set to "" to disable
dns_upstream = "1.1.1.1:53"

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
    crackbox/main.go          umbrella CLI: proxy serve / run / state
  pkg/
    proxy/                    forward HTTP + CONNECT + transparent + splice
    dns/                      UDP/53 allowlist-enforcing resolver
    host/                     KVM/qemu sandbox lifecycle (shipped; spec 11/12)
    config/                   TOML config loader (search path + defaults)
    match/                    Host(allowlist, host) bool, validators
    admin/                    Registry + admin API handlers
    run/                      `crackbox run` orchestration
    client/                   admin HTTP client
  Dockerfile, Makefile
```

## Configuration

| Var                         | Default                 | Used by                                           |
| --------------------------- | ----------------------- | ------------------------------------------------- |
| `CRACKBOX_PROXY_ADDR`       | `:3128`                 | `proxy serve` forward-mode listen                 |
| `CRACKBOX_ADMIN_ADDR`       | `:3129`                 | `proxy serve` admin API                           |
| `CRACKBOX_TRANSPARENT_ADDR` | `:3127`                 | `proxy serve` transparent listener; "" = disabled |
| `CRACKBOX_DNS_ADDR`         | `:53`                   | `proxy serve` DNS listener; "" = disabled         |
| `CRACKBOX_DNS_UPSTREAM`     | `1.1.1.1:53`            | upstream resolver for allowed queries             |
| `CRACKBOX_ADMIN_SECRET`     | (unset)                 | bearer token for admin mutations; empty = open    |
| `CRACKBOX_STATE_PATH`       | (unset)                 | registry persistence; empty = RAM-only            |
| `CRACKBOX_IMAGE`            | `crackbox:latest`       | `run` proxy image                                 |
| `CRACKBOX_SUBNET`           | `10.99.0.0/16`          | `run` Docker subnet                               |
| `CRACKBOX_ADMIN`            | `http://localhost:3129` | `state` default admin URL                         |

`CRACKBOX_ADMIN_SECRET` empty leaves mutating endpoints unauthenticated
and logs a warning. The same secret must be set on both the daemon and
each consumer (e.g. arizuko's `routd`/`runed`). Read-only endpoints
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

Proxy (`pkg/match/Host`, `LooksLikeDomain`, `LooksLikeIP`, `domainRegex`, test
fixtures) ported from `/home/onvos/app/crackbox/internal/vm/{proxy,netfilter}.go`.

Host library (`pkg/host/`) ported from the prototype's `internal/vm/launch.go`
(qemu invocation, virtio-net, virtio-fs), `internal/vm/network.go` (bridge +
tap + iptables NAT), `internal/vm/secrets.go` (TLS-terminating placeholder
injection). See [`specs/11/12`](../specs/11/12-crackbox-sandboxing.md).

## Orthogonality

```sh
grep -rE 'github\.com/[^/]+/arizuko/(store|core|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|auth|audit|resreg|obs|routd|runed|authd)' crackbox/  # returns empty
```

The grep is owner-agnostic on purpose — `arizuko`'s `go.mod` carries
a stale `onvos/` owner while the canonical GitHub home is
`github.com/kronael/arizuko`. The orthogonality property is "no
arizuko-internal subpackage is imported," not "the owner string
matches X."

This component imports nothing from arizuko-internal packages.
Consumers (arizuko's runed, future tools) import
`<arizuko-module>/crackbox/pkg/...` or invoke the CLI.

Crackbox shares arizuko's single `go.mod` and stays that way —
orthogonality is enforced by the import graph (the grep above), not
by module separation. External users consume crackbox as a CLI or
Docker image, not as a Go library.
