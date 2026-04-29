# crackbox

Forward HTTP/HTTPS proxy daemon with per-source-IP allowlists.

One daemon, one registry, one matchHost. CLI subcommands are sugar
over the same daemon — there is no separate single-shot proxy.

## Subcommands

```
crackbox proxy serve [--listen :3128 --admin :3129]
crackbox run --allow <list> [--id <name>] [--image <img>] -- <cmd>...
crackbox state [--admin <url>]
```

- `proxy serve` — long-running daemon. Forward proxy on `:3128`,
  admin API on `:3129` (`/v1/register`, `/v1/unregister`, `/v1/state`,
  `/health`). matchHost is per-source-IP lookup.
- `run` — convenience wrapper. Creates a Docker network, spawns
  `crackbox proxy serve` on it, registers one allowlist for the
  to-be-run user container, runs `<cmd>` with `HTTPS_PROXY` set,
  tears everything down on exit. Same daemon code; the registry
  just happens to have one entry.
- `state` — query a running daemon's registry.

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
  cmd/crackbox/main.go        CLI dispatcher
  pkg/proxy/                  forward HTTP + CONNECT tunnel + splice
  pkg/match/                  Host(allowlist, host) bool, validators
  pkg/admin/                  Registry + admin API handlers
  pkg/run/                    `crackbox run` orchestration
  pkg/client/                 admin HTTP client
  Dockerfile, Makefile
```

## Configuration

| Var                   | Default                 | Used by              |
| --------------------- | ----------------------- | -------------------- |
| `CRACKBOX_PROXY_ADDR` | `:3128`                 | `proxy serve` listen |
| `CRACKBOX_ADMIN_ADDR` | `:3129`                 | `proxy serve` admin  |
| `CRACKBOX_IMAGE`      | `crackbox:latest`       | `run` proxy image    |
| `CRACKBOX_SUBNET`     | `10.99.0.0/16`          | `run` Docker subnet  |
| `CRACKBOX_ADMIN`      | `http://localhost:3129` | `state`              |

## Don't reinvent supervision

No idle-shutdown, no auto-restart. Daemon mode lifecycle is owned
by Docker compose / systemd. `crackbox run` lifecycle is owned by
the invoking shell.

## Reuse from origin crackbox

`pkg/match/Host`, `LooksLikeDomain`, `LooksLikeIP`, `domainRegex`,
and the test fixtures are ported from
`/home/onvos/app/crackbox/internal/vm/{proxy,netfilter}.go`.

## Orthogonality

```sh
grep -r 'github.com/kronael/arizuko' crackbox/  # returns empty
```

This component imports nothing from arizuko. Consumers (arizuko's
gated, future tools) import `github.com/onvos/arizuko/crackbox/pkg/...`
or invoke the CLI.
