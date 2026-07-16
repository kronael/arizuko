---
status: shipped
shipped: 2026-04-29
---

# Egred — forward proxy with per-source allowlists

> One daemon, one registry, one matchHost. The egress proxy
> ("egred") ships as the `crackbox proxy serve` subcommand of the
> single `crackbox` binary; that binary's `pkg/host/` adds VM
> sandboxing ([`6/9`](9-crackbox-sandboxing.md)).

## Status

Shipped 2026-04-29 as the proxy half of crackbox. There is no
separate `egred` binary — "egred" names the proxy role in code and
in this spec; the shipped entrypoint is `crackbox proxy serve`.

## What it does

Forward HTTP/HTTPS proxy. Holds an in-memory registry of
`(source-IP → (id, allowlist))` entries. CONNECT/HTTP requests
from a registered source IP are spliced through if the destination
hostname matches the registered allowlist; otherwise 403.

Two listeners by default:

- `:3128` forward proxy (HTTP + CONNECT-tunneled HTTPS). Client
  sets `HTTPS_PROXY=http://<proxy-host>:3128`.
- `:3127` transparent proxy. Linux `getsockopt(SO_ORIGINAL_DST)`
  - SNI/Host peek. Client side runs `iptables REDIRECT`.

Plus admin listener on `:3129`:

- `POST /v1/register {ip, id, allowlist}`
- `POST /v1/unregister {ip}`
- `GET /v1/state`
- `GET /health`

One pure `match.Host(allowlist, host) bool` decides allow/deny.

## Standalone use

```
crackbox proxy serve [--config <path>] [--listen :3128] [--admin :3129] [--transparent :3127] [--dns-listen :53] [--dns-upstream 1.1.1.1:53]
```

Long-lived daemon; lifecycle owned by systemd or docker compose. No
idle-shutdown, no auto-restart, no supervision.

## Convenience CLI for one-shot use

```
crackbox run --allow <list> [--id <name>] [--image <img>] -- <cmd>...
```

Docker-only orchestration (`crackbox/pkg/run/run.go`):

1. Create a Docker network.
2. Start the crackbox proxy container on it.
3. Start the user container, register its IP with the proxy admin API.
4. Run `<cmd>` with `HTTP(S)_PROXY` set and `--dns <proxyIP>`.
5. Tear down on exit.

The wrapper composes daemon + admin client + container spawn
primitives. It does not contain a special-case proxy.

## Where egred fits

| Component                | Role                                                    |
| ------------------------ | ------------------------------------------------------- |
| `crackbox proxy serve`   | The proxy daemon ("egred"). This spec.                  |
| `crackbox/pkg/proxy/`    | Library the daemon runs.                                |
| `crackbox/pkg/host/`     | VM-sandbox library ([`6/9`](9-crackbox-sandboxing.md)). |
| `crackbox/cmd/crackbox/` | The single CLI: `proxy serve`, `run`, `state`.          |

## Go API

Importable from `crackbox/pkg/...`:

- `proxy.New(*admin.Registry) *Proxy` — what `crackbox proxy serve`
  runs.
- `client.New(adminURL, secret string) *Client` — `Register`,
  `Unregister`, `State` over the admin API.
- `match.Host(allowlist []string, host string) bool` — pure
  function, exposed for callers that want to share the matcher.

There is **no** `Sandbox` type and no separate single-shot factory.
"Single-shot" = daemon + register + cleanup, all via the API above.

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

## Security invariants

Source-IP reuse is safe under egred's registry model because all
lookups are keyed by current registry state, not by trust in the
IP itself:

- Each registration is `(source_ip, id, allowlist)`. Lookups on
  inbound traffic resolve `source_ip → entry` against the live
  registry. There is no cached state outside the registry.
- Docker / KVM may reuse a source IP after the previous tenant's
  container exits and `/v1/unregister` runs. The next tenant POSTs
  its own `/v1/register` before its container makes egress; the
  old allowlist is gone.
- If a source IP makes a request with no registered entry, egred
  refuses (403). There is no implicit allowlist, no fallback to a
  prior tenant's rules.
- Consumers MUST register before the spawned container starts
  outbound traffic, and MUST unregister on exit. The "register
  before traffic" ordering is the consumer's invariant, not
  egred's — egred just enforces "no entry → deny".

## Out of scope for v1 (proxy-only)

Listed for visibility, deferred:

- Secret handling (now spec [`5/13`](../5/13-ext-mcp.md), tool-level
  broker — no proxy involvement).
- KVM/qemu sandbox host (now lives in [`6/9`](9-crackbox-sandboxing.md)).
- MCP tools (`request_network`, `list_network_rules`).
- Traffic logs and audit.
- Response scanning.
- Runtime allowlist mutation (`crackbox allow / deny`).

## Acceptance

- `crackbox run --allow github.com -- curl -s -o /dev/null -w '%{http_code}' https://github.com`
  prints something other than `403`.
- The same invocation against `https://example.com` prints `403`.
- `crackbox proxy serve` running, plus a separate process that calls
  `crackbox/pkg/client.Register` for its container's IP and points the
  container's `HTTPS_PROXY` at the daemon, achieves the same allow/deny
  result with no code changes in the daemon.
- `make -C crackbox build && make -C crackbox test` passes on a host
  with no arizuko process and no arizuko data directory.
- Zero arizuko-internal imports (component test, per
  [`6/16`](16-daemon-standalone-matrix.md)):
  `grep -rE 'kronael/arizuko/(routd|runed|authd|onbod|webd|store|core|ipc|grants)' crackbox/`
  returns empty.
