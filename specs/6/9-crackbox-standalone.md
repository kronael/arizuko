---
status: planned
---

# Crackbox — forward proxy with per-source allowlists

> One daemon, one registry, one matchHost. CLI subcommands are
> sugar over the same daemon — there is no "single-shot" code path
> separate from the multi-tenant one.

## Status

The `egred/` daemon shipped on krons 2026-04-29 is the working
prototype this spec replaces. Daemon-mode behavior stays
identical; the whole component moves under `crackbox/` with the
layout required by
[`specs/8/b-orthogonal-components.md`](../8/b-orthogonal-components.md).
arizuko's consumer-side rework is described in
[`specs/6/10-crackbox-arizuko.md`](10-crackbox-arizuko.md).

## Problem

Running untrusted code (Claude Code agents, CI jobs, build
scripts) needs default-deny network egress with a small
per-instance allowlist. The shipped prototype demonstrates the
mechanism in production: a forward HTTP/HTTPS proxy that registers
`(source-IP → allowlist)` entries and matches incoming
connections against the registered list. This spec describes v1
of that mechanism extracted into a sibling component, named
`crackbox`, with a thin convenience CLI that lets a developer use
the same daemon for one-shot isolation on a laptop.

## The single mechanism

There is exactly one thing: a long-running forward HTTP/HTTPS
proxy daemon with an in-memory registry of
`(source-IP → (id, allowlist))` entries and an admin API for
managing the registry.

- Listener at `:3128` for forward HTTP and CONNECT-tunneled HTTPS.
- Listener at `:3129` for the admin API:
  - `POST /v1/register` — body `{ip, id, allowlist}`
  - `POST /v1/unregister` — body `{ip}`
  - `GET /v1/state` — current registry snapshot
  - `GET /health` — liveness
- In-memory `Allowlist` map: `source-IP → (id, []string)`.
- One pure `matchHost(allowlist, host) bool` function — three
  lines, ported from the shipped prototype.
- No source-IP-trust assumption beyond "the network topology only
  allows our agent containers to reach this proxy" — same as the
  shipped prototype.

There is **no** "single-shot mode" anywhere in the daemon. The
proxy cannot tell — and does not branch on — whether the registry
holds one entry because a CLI wrapper just registered it, or one
entry because a long-running consumer happens to have one agent
spawned. Same code, same lookup, same response. Branching, if
any, lives at the call-site that sets up the daemon — never in
the proxy server, never in `matchHost`, never in the admin API
handlers.

## CLI

One binary, three subcommands:

```bash
crackbox proxy serve [--listen :3128 --admin :3129]
    Run the daemon. Long-lived. Lifecycle owned by Docker compose
    or systemd. No idle-shutdown, no auto-restart, no supervision
    inside the binary.

crackbox run --allow <list> [--id <name>] -- <cmd>...
    Convenience wrapper. ~150 LOC of orchestration:
      1. Spawn a Docker network.
      2. Spawn `crackbox proxy serve` on the network.
      3. Spawn the user container (created), inspect IP, POST
         /v1/register {ip, id, allowlist=<list>}.
      4. Start the container with HTTPS_PROXY pointing at the
         daemon.
      5. On exit, tear down the user container, daemon container,
         and network.
    The wrapper does NOT contain a special-case proxy
    implementation. It composes the existing daemon + admin
    client + container-spawn primitives.

crackbox state [--admin <url>]
    Query the running daemon's registry. Same /v1/state endpoint.
```

What looks like "two ways of using it" is one daemon plus one CLI
wrapper. The wrapper's branching lives at the call-site (it sets
up + tears down). It does not exist anywhere inside `pkg/proxy/`
or `pkg/admin/`.

## Go API

Importable from `crackbox/pkg/...`:

- `crackbox.NewServer(ServerConfig) *Server` — same `*Server`
  that `crackbox proxy serve` runs.
- `crackbox.NewClient(adminURL) *Client` with `Register`,
  `Unregister`, `State`.
- `crackbox.MatchHost(allowlist []string, host string) bool` —
  pure function exposed for callers that want to share the
  matcher.

There is **no** `Sandbox` type. No `NewSandbox`. No separate
single-shot factory. "Single-shot" is achieved by daemon +
register + cleanup, all of which use the API above.

## Layout

Sibling of `ant/` inside the arizuko monorepo:

```
crackbox/
  cmd/crackbox/main.go     CLI dispatcher: proxy / run / state
  pkg/proxy/               daemon: proxy.go, splice, hop-by-hop
  pkg/match/               matchHost + validators (ported)
  pkg/admin/               /v1/register etc handlers
  pkg/run/                 convenience wrapper for `crackbox run`
  pkg/client/              http client for admin
  Dockerfile
  Makefile
  README.md
  CHANGELOG.md
```

## Footprint

| Aspect                       | Number                          |
| ---------------------------- | ------------------------------- |
| Image size                   | ~15 MB (one image)              |
| Daemon RAM                   | 15-20 MB regardless of #entries |
| `crackbox run` overhead      | +1 user container + 1 network   |
| Extra RAM for `crackbox run` | ~10 MB over daemon, ~1 MB net   |
| `crackbox run` spawn latency | 500 ms – 1 s (Docker creates)   |

## Reuse from the shipped prototype

Ported (with attribution) from
`/home/onvos/app/crackbox/internal/vm/{proxy,netfilter}.go` and
the current arizuko `egred/`:

- `matchHost`, `looksLikeDomain`, `looksLikeIP`, `domainRegex`.
- Hop-by-hop header stripping, CONNECT splice loop.
- Test fixtures for matcher edge cases.

New code:

- The CLI dispatcher (proxy / run / state).
- `pkg/run/` convenience wrapper.
- `pkg/admin/` handlers for the register/unregister/state shape.

## Don't reinvent supervision

Explicit anti-pattern. No idle-shutdown timer, no auto-restart,
no process supervision, no "if I have zero entries for N minutes
shut myself down" logic inside crackbox. Daemon-mode lifecycle is
owned by Docker compose or systemd. `crackbox run` lifecycle is
owned by the invoking shell (the wrapper's exit teardown, plus
the user hitting Ctrl-C).

## Out of scope for v1

Listed for visibility, deferred:

- Spec 6/11 placeholder injection (selective MITM for secrets).
- QEMU backend.
- MCP tools (`request_network`, `list_network_rules`).
- Traffic logs and audit.
- Response scanning.
- Runtime allowlist mutation (`crackbox allow / deny`).

## Acceptance

- `crackbox run --allow github.com -- curl -s -o /dev/null -w '%{http_code}' https://github.com`
  prints something other than `403`.
- The same invocation against `https://example.com` prints `403`.
- `crackbox proxy serve` running, plus a separate process that
  calls `crackbox.Client.Register` for its container's IP and
  points the container's `HTTPS_PROXY` at the daemon, achieves
  the same allow/deny result with no code changes in the daemon.
- `make -C crackbox build && make -C crackbox test` passes on a
  host with no arizuko process and no arizuko data directory.
- `grep -r 'github.com/kronael/arizuko' crackbox/` returns empty.
