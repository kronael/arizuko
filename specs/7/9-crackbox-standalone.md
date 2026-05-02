---
status: shipped
shipped: 2026-04-29
---

# Egred — forward proxy with per-source allowlists

> One daemon, one registry, one matchHost. Ships in the
> [crackbox component](../6/12-crackbox-sandboxing.md). The
> daemon is named `egred`; the larger `crackbox` component
> (library + bundled binaries) provides VM sandboxing on top.

## Status

Shipped 2026-04-29 as the proxy-half of crackbox; renamed `egred`
to disambiguate from the library role. Same binary, same wire
shape — the `egred` name is back in use only at the daemon /
process / container level. All currently-running deployments use
`crackbox proxy serve` as the entrypoint, which is functionally
identical to invoking `egred` directly. The upcoming standalone
`cmd/egred/` binary is wire-compatible.

## What it does

Forward HTTP/HTTPS proxy. Holds an in-memory registry of
`(source-IP → (id, allowlist))` entries. CONNECT/HTTP requests
from a registered source IP are spliced through if the destination
hostname matches the registered allowlist; otherwise 403.

Two listeners by default:

- `:3128` forward proxy (HTTP + CONNECT-tunneled HTTPS). Client
  sets `HTTPS_PROXY=http://egred:3128`.
- `:3127` transparent proxy. Linux `getsockopt(SO_ORIGINAL_DST)`
  - SNI/Host peek. Client side runs `iptables REDIRECT`.

Plus admin listener on `:3129`:

- `POST /v1/register {ip, id, allowlist}`
- `POST /v1/unregister {ip}`
- `GET /v1/state`
- `GET /health`

One pure `matchHost(allowlist, host) bool` decides allow/deny.
Three lines, no branching on caller mode.

## Standalone use

Two binaries; pick one:

```
egred [--config <path>] [--listen :3128] [--admin :3129] [--transparent :3127]
```

Standalone proxy daemon. Long-lived, lifecycle owned by systemd or
docker compose. No idle-shutdown, no auto-restart, no supervision.

```
crackbox proxy serve [--config ...] [--listen ...] [--admin ...] [--transparent ...]
```

Same daemon under the umbrella `crackbox` CLI. Functionally
identical; this is the form used by today's compose. Emitted
`docker compose` keeps using this until [c-sandd](../8/c-sandd.md)
ships, at which point compose may switch to `egred` directly to
make the role visible at the process level.

## Convenience CLI for one-shot use

```
crackbox run --allow <list> [--id <name>] -- <cmd>...
```

~150 LOC of orchestration:

1. Spawn a Docker network (or KVM bridge if `--kvm`).
2. Spawn `egred` on the network.
3. Spawn the user container/VM, register its IP with `egred`.
4. Run `<cmd>` with `HTTPS_PROXY` set.
5. Tear down on exit.

The wrapper composes daemon + admin client + container/VM spawn
primitives. It does not contain a special-case proxy.

## Where egred fits in the bigger picture

| Component                  | Role                                                                   |
| -------------------------- | ---------------------------------------------------------------------- |
| `egred`                    | The proxy daemon. This spec.                                           |
| `crackbox/pkg/proxy/`      | Library used by `egred` and `crackbox proxy serve`.                    |
| `crackbox/pkg/host/`       | Library for VM sandboxing (see [8/a](../6/12-crackbox-sandboxing.md)). |
| `crackbox/cmd/crackbox/`   | Umbrella CLI: `proxy serve`, `run`, `state`, `host`.                   |
| `crackbox/cmd/egred/`      | Standalone proxy binary, just the proxy.                               |
| [`sandd`](../8/c-sandd.md) | arizuko-internal daemon that uses the docker or                        |
|                            | crackbox-host backend; wire-format independent of egred.               |

The naming distinction matters once VM sandboxing lands:
**crackbox = library + bundled binaries (the umbrella component);
egred = the proxy binary specifically.** Today they're often
conflated in conversation because crackbox = proxy is all that
exists in production.

## Go API

Importable from `crackbox/pkg/...`:

- `proxy.NewServer(ServerConfig) *Server` — what `crackbox proxy
serve` and `egred` both run.
- `client.NewClient(adminURL) *Client` — `Register`, `Unregister`,
  `State` over admin API.
- `match.Host(allowlist []string, host string) bool` — pure
  function, exposed for callers that want to share the matcher.

There is **no** `Sandbox` type and no separate single-shot factory.
"Single-shot" = daemon + register + cleanup, all via the API
above.

## Reuse from origin prototype

Ported (with attribution) from
`/home/onvos/app/crackbox/internal/vm/{proxy,netfilter}.go`:

- `Host`, `LooksLikeDomain`, `LooksLikeIP`, `domainRegex`
- Hop-by-hop header stripping, CONNECT splice loop
- Test fixtures for matcher edge cases

## Footprint

| Aspect                       | Number                          |
| ---------------------------- | ------------------------------- |
| Image size                   | ~15 MB (one image)              |
| Daemon RAM                   | 15-20 MB regardless of #entries |
| `crackbox run` overhead      | +1 user container + 1 network   |
| Extra RAM for `crackbox run` | ~10 MB over daemon, ~1 MB net   |
| `crackbox run` spawn latency | 500 ms – 1 s (Docker create)    |

## Don't reinvent supervision

Explicit anti-pattern. No idle-shutdown timer, no auto-restart, no
process supervision, no "if I have zero entries for N minutes shut
myself down." Daemon-mode lifecycle is owned by Docker compose or
systemd. `crackbox run` lifecycle is owned by the invoking shell.

## Out of scope for v1 (proxy-only)

Listed for visibility, deferred:

- Spec 6/11 placeholder injection (selective MITM for secrets).
- KVM/qemu sandbox host (now lives in [8/a](../6/12-crackbox-sandboxing.md))
- MCP tools (`request_network`, `list_network_rules`).
- Traffic logs and audit.
- Response scanning.
- Runtime allowlist mutation (`crackbox allow / deny`).

## Acceptance

- `crackbox run --allow github.com -- curl -s -o /dev/null -w '%{http_code}' https://github.com`
  prints something other than `403`.
- The same invocation against `https://example.com` prints `403`.
- `crackbox proxy serve` (or `egred`) running, plus a separate
  process that calls `crackbox/pkg/client.Register` for its
  container's IP and points the container's `HTTPS_PROXY` at the
  daemon, achieves the same allow/deny result with no code changes
  in the daemon.
- `make -C crackbox build && make -C crackbox test` passes on a
  host with no arizuko process and no arizuko data directory.
- `grep -rE 'github\.com/[^/]+/arizuko/(store\|core\|gateway\|api\|chanlib\|chanreg\|router\|queue|ipc\|grants\|onbod|webd|gated)' crackbox/` returns empty.
