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

## What a stranger gets

A single Go binary, forward proxy, static allowlist from a config file or
flags, no admin surface, no state. Reads in an afternoon. That is the
thing `6/16`'s "get one stranger running it" is actually asking for —
today's 2,400-line version with a registry and a source-IP trust model
is not.

Packaging friction is separate and unfixed: crackbox shares arizuko's
`go.mod` by design (root `CLAUDE.md`), so a stranger cannot `go install`
it and must clone arizuko or pull the image.

## Ties

`6/8` (the current design this cuts) · `6/9` (`pkg/host`, the other half)
· `6/16` (ship one, not seven) · `6/17` (MITM declined — this depends on
it) · BUGS `L48` (the proposal and its measurements).
