---
status: draft
depends:
  [
    8-crackbox-standalone,
    16-daemon-standalone-matrix,
    17-egress-credential-proxy,
  ]
---

# specs/6/24 — crackbox, lightest

`6/8` ships crackbox as one shared forward proxy holding a registry of
`(source_ip, id, allowlist)` entries, with a transparent mode and an
admin API. This spec cuts it to the smallest thing that still enforces
per-folder egress, by removing what only the SHARED model required.

The result is roughly **250–300 lines** and no new dependency. That is
why this is a cut rather than an adoption: at that size, taking
tinyproxy or iron-proxy would add a C or third-party dependency, a config
renderer and process supervision to replace less code than it costs.
The wheel here is `net/http` — already in use, not reinvented.

## What the shared model forced, and what it costs

| Piece                                    | Lines | Exists because                                            |
| ---------------------------------------- | ----- | --------------------------------------------------------- |
| `pkg/admin` (registry + API)             | 572   | one proxy serves N containers, so it must tell them apart |
| `pkg/client`                             | 175   | consumers register/unregister against that API            |
| `pkg/match`                              | 144   | allowlist glob matching — **keep**                        |
| `pkg/proxy/peek.go`                      | 279   | transparent mode must recover the host from SNI           |
| `pkg/proxy/transparent.go` + `origdst_*` | 195   | intercepting traffic the client did not address to us     |
| `pkg/proxy/proxy.go`                     | 164   | the forward CONNECT/HTTP splice — **keep**                |

**Forward mode never needed peeking.** `proxy.go:61` takes the host from
`r.Host` — the `CONNECT host:443` request line the client sends when
`HTTP_PROXY` points at us. SNI peeking exists solely so transparent mode
can recover a destination the client addressed elsewhere.

## The cut

**One proxy per spawn, statically configured, forward mode only.**

- runed starts a proxy alongside each spawned container with that
  folder's allowlist baked in, and it dies with the turn.
- The container gets `HTTP_PROXY`/`HTTPS_PROXY` pointing at it.
- No registry, no admin API, no client library, no source-IP identity,
  no transparent interception, no SNI peeking.

What survives: parse `CONNECT`, match the host against a fixed list,
splice or refuse. Plus `pkg/match` unchanged.

### Why this is safer, not just smaller

`6/8` §"Security invariants" makes egress safety depend on the consumer:

> Consumers MUST register before the spawned container starts outbound
> traffic, and MUST unregister on exit. The "register before traffic"
> ordering is the consumer's invariant, not egred's.

Late registration fails closed. **Late unregistration does not**: Docker
reuses source IPs, so if runed dies between a container exiting and
`/v1/unregister`, the stale entry survives and the next tenant on that IP
inherits the previous folder's allowlist. Per-spawn removes the
invariant along with the registry — there is no shared state to go stale
and no IP to reuse.

### Nothing dynamic is lost

`runed/docker.go:127` builds the EgressConfig "from runed's own config +
that **static per-folder list**", once, at spawn. There is no
re-register and no mid-run update anywhere in the tree. The registry's
dynamism served MULTIPLEXING, not live rule changes.

If a live rule change is ever wanted — an operator approving a new host
mid-turn — per-spawn makes it _easier_, not harder: signal that spawn's
own proxy over a local socket, with exactly one tenant and no identity
question to answer. It does not require MITM either; a forward proxy
reads the destination from the CONNECT line whether its rules are fixed
or changing.

## MITM stays declined

`6/17` declined interception (2026-08-18): header injection was the only
thing requiring it, and it was withdrawn. This cut depends on that. A
forward proxy that never terminates TLS has no CA, no key material and
no request-body access — which is what keeps 250 lines auditable by
someone who does not trust us.

## Split the two halves

crackbox today is two orthogonal things in one directory: the egress
proxy, and `pkg/host/` — a KVM/qemu sandbox library (`6/9`) that **no
importer outside `crackbox/` uses** and that the proxy binary does not
link (`go list -deps` finds zero references). "Ship one component" is
currently shipping two.

**DECIDED 2026-08-18 — split, and crackbox survives as the tool that
combines them.** Three things where there is one today:

| Piece            | Is                                                            |
| ---------------- | ------------------------------------------------------------- |
| the egress proxy | a library + its own small binary: forward, static, ~250 lines |
| `pkg/host`       | its own component — the KVM/qemu sandbox library (`6/9`)      |
| **`crackbox`**   | a standalone TOOL that composes both, and nothing else        |

This keeps the name and the convenience — `crackbox run --kvm` still
spawns a sandboxed VM behind a filtered egress path — while letting
someone take just the proxy, or just the sandbox library, without the
other. The composition is the product; neither half has to be.

It also matches what `6/16` means by shipping one part: a stranger who
wants egress filtering downloads a forward proxy, not a KVM library
bundled with it.

## The resulting layout

Three directories where there is one, all still inside arizuko's single
`go.mod` (root `CLAUDE.md` — siblings share the module; orthogonality is
enforced by the import graph, not by module boundaries):

```
egress/            the proxy: forward-only, static allowlist
  proxy.go         parse CONNECT, dial, splice          (from pkg/proxy)
  match/           allowlist globbing                   (from pkg/match, unchanged)
  cmd/egress/      the standalone binary
sandbox/           the KVM/qemu lifecycle library       (from crackbox/pkg/host)
crackbox/          the TOOL that composes both
  cmd/crackbox/    `crackbox run --kvm` — sandbox + filtered egress
```

Deleted outright, because only the shared model needed them:
`pkg/admin` (572), `pkg/client` (175), `pkg/proxy/peek.go` (279),
`pkg/proxy/transparent.go` + `origdst_*.go` (195). `pkg/dns` (753) goes
with them — a DNS NXDOMAIN filter is a second enforcement point for a
single-tenant proxy that already sees every destination in the CONNECT
line, and a rule expressed twice is a rule that can disagree with itself.

`runed` changes from "register the spawn's IP against a shared proxy" to
"start the spawn's proxy with its allowlist, set `HTTP_PROXY`". The
`EgressConfig` it already builds at spawn (`runed/docker.go:127`) is
exactly the input, unchanged.

## Why this is worth doing — corrected 2026-08-18

**Not for the stranger.** `6/16`'s plan item is "get one stranger running
crackbox", and that item is weak: Anthropic ships sandboxing and network
restriction inside Claude Code itself, plus the Agent SDK and hosted
Managed Agents. A standalone egress-filtering proxy competes with a
feature the stranger already has, free, in the tool they are already
using. Packaging polish does not fix that.

Do it for the internal reasons, which stand on their own:

- **1,221 of ~1,700 non-test lines exist only to serve a shared-proxy
  model this spec abandons.** Deleting them is not a refactor, it is
  removing the implementation of a design decision that was reversed.
- **It removes a real leak** — `6/8`'s consumer-held unregister invariant
  (above), which no amount of care makes safe while it is a convention.
- **The boundary gets honest**: a proxy that proxies, a sandbox library
  that sandboxes, and a tool that composes them. Today one directory
  claims to be one component and is two.

What arizuko has that Anthropic does not is the COMPOSITION — folder
multitenancy with per-folder egress, ACL, secrets, memory and web mounts,
across ten-plus channels, self-hosted on the operator's own data. The
parts are not the product. That is the reason to make the parts small and
boring rather than to market them.

## Ties

`6/8` (the current design this cuts) · `6/9` (`pkg/host`, the other half)
· `6/16` (ship one, not seven) · `6/17` (MITM declined — this depends on
it) · BUGS `L48` (the proposal and its measurements).
