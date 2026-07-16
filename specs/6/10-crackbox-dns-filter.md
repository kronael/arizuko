---
status: shipped
shipped: 2026-05-13
depends: [8-crackbox-standalone, 9-crackbox-sandboxing]
---

# Crackbox DNS filter — NXDOMAIN for non-allowlisted hostnames

> Same allowlist, one layer earlier. For clients whose resolver
> points at crackbox, denied hostnames return NXDOMAIN instead of
> a wasted CONNECT 403.

## Why

Crackbox enforces at HTTP/CONNECT today
(`crackbox/pkg/proxy/proxy.go:65`, `:107`; transparent at
`crackbox/pkg/proxy/transparent.go:81`). The container's resolver
succeeds against the default upstream, then the connect 403s. That:

1. Distinguishes "denied" from "nonexistent" — leaks the allowlist.
2. Wastes a TCP round-trip per denied call.

Shipped as a UDP/53 listener inside `crackbox proxy serve`
(`crackbox/pkg/dns/`), gated by the same per-source allowlist.

## Scope

Crackbox-internal only: the DNS server and its wiring into
`crackbox proxy serve` / `crackbox run`. Two related consumer changes
are out of scope, each owning its own change:

- **arizuko spawn path** (`container/egress.go`, `container/runner.go`):
  needs to discover the crackbox container's IP on each per-folder
  network and pass `--dns <ip>` to `docker create`. Today's egress path
  uses the `crackbox` Docker DNS alias for `HTTPS_PROXY` but never
  resolves it to an IP, so the arizuko container spawn does **not** yet
  point at the DNS filter. Tracked as follow-up under
  [`6/9`](9-crackbox-sandboxing.md).
- **transparent-mode rebinding boundary**: see "Composition".

## Mechanism

`crackbox proxy serve` opens an additional UDP listener. Per query:

1. Parse the first question's QNAME. Anything that does not parse
   cleanly → drop silently (no FORMERR; includes QDCOUNT > 1).
2. **`QTYPE == ANY` → REFUSED**, regardless of allowlist. ANY is not
   useful for libc resolution and would make the daemon a potential
   reflector for large upstream answers.
3. `Registry.Allow(src, qname)` — the existing per-source-IP check
   (`crackbox/pkg/admin/registry.go:139`), backed by `match.Host`
   (`crackbox/pkg/match/match.go:46-64`). Subdomain semantics identical
   to the HTTP path.
4. **Allow** → forward to the configured upstream resolver
   (§"Forwarder hygiene"); relay the reply back.
5. **Deny / unregistered source** → synthesize NXDOMAIN echoing the
   original ID + question.

## Forwarder hygiene

A naive per-query UDP forwarder lets a same-network attacker race a
forged reply into the ephemeral socket. The forwarder
(`crackbox/pkg/dns/forward.go`) tightens:

1. **Source-address check.** The reply must come from the configured
   upstream addr (`net.UDPAddr` equality on IP+port). Anything else is
   dropped and the wait continues until timeout.
2. **ID + question match.** The reply's first 12 bytes (ID + flags
   stripped of QR) must match the query's, and the echoed question
   section must byte-equal the query's. Drop on mismatch.
3. **Per-query socket, bounded lifetime.** One UDP socket per in-flight
   query, closed on first valid reply or after a fixed timeout (3 s).
   No state between queries.
4. **No EDNS rewriting.** Bytes go through verbatim in both directions.

## Where it lives

- `crackbox/pkg/dns/` — `server.go` (listener, handler, NXDOMAIN synth,
  question parse), `forward.go` (per-query dial+read with validation),
  `server_test.go`.
- `crackbox/cmd/crackbox/main.go` (`cmdProxy`) — `dns.New(reg, upstream)`
  constructed alongside `proxy.New(...)`, exposing `Serve(addr) error`
  and `Close() error`.

No new binary. The DNS listener is part of `crackbox proxy serve`.

## Wire

RFC 1035 header (12 bytes: ID, flags, QDCOUNT, ANCOUNT, NSCOUNT,
ARCOUNT) + one question (length-prefixed labels + null, QTYPE, QCLASS).
**Pointer compression (0xC0) in QNAMEs is rejected as malformed** — real
resolvers do not compress queries (only responses); rejecting keeps the
parser tight and removes a class of pointer-loop bugs. The parser's
contract is binary: clean `(name, qtype)` or the caller drops the packet.

NXDOMAIN response: copy header+question verbatim, set QR=1 RCODE=3,
preserve RD echo, zero AN/NS/AR. No SOA in authority — the cut is exactly
the name asked (RFC 8020-aligned). No glue.

## Composition with other paths

The DNS filter helps any client whose `/etc/resolv.conf` points at the
crackbox listener. It does **not** automatically defend:

- **Transparent mode** (`crackbox/pkg/proxy/transparent.go:67-94`):
  dials the SO_ORIGINAL_DST IP directly, not the hostname. A rebinding
  attack on an allowlisted name resolves to a hostile IP via the DNS
  filter (forwarded upstream answer), and transparent mode would splice
  to that IP. Out-of-scope mitigation: a destination-IP policy layer on
  transparent. This spec does not change transparent's behavior; it does
  not regress it either.
- **Forward proxy** (CONNECT / HTTP): dials by name after the client's
  resolver returns an address, so the name is the unit of enforcement.
  The HTTP path remains the second gate; DNS is additive
  defense-in-depth.
- **Secrets broker** ([`5/13`](../5/13-ext-mcp.md)): the tool-level
  broker runs host-side, never touches egress; the DNS filter and the
  broker share no data path.

## Container-side wiring

`docker create --dns <ip>` writes `nameserver <ip>` into the container's
resolv.conf. `crackbox/pkg/run/run.go` already knows the proxy
container's IP and passes `--dns <proxyIP>` on the user container's
create args.

The arizuko-side change is **not** part of this spec: it needs
`EgressConfig.CrackboxIPOnNetwork(folder)` or equivalent, owned by
[`6/9`](9-crackbox-sandboxing.md).

## Config

Aligned with the existing `CRACKBOX_PROXY_ADDR` shape:

| Env / TOML key                                 | Default      | Purpose                                                          |
| ---------------------------------------------- | ------------ | ---------------------------------------------------------------- |
| `CRACKBOX_DNS_ADDR` / `proxy.dns_listen`       | `:53`        | UDP listen; empty disables                                       |
| `CRACKBOX_DNS_UPSTREAM` / `proxy.dns_upstream` | `1.1.1.1:53` | Forward target                                                   |
| `--dns-listen <addr>` flag                     | (unset)      | `crackbox proxy serve` override (distinct from Docker's `--dns`) |
| `--dns-upstream <addr>` flag                   | (unset)      | upstream resolver override                                       |

Precedence: flag > env > config. Empty-string disable matches the
existing `proxy.transparent_listen` pattern. Bind failure at startup is
fatal (`os.Exit(1)`), same as the other listeners; the DNS server owns
its `net.PacketConn` and `Close` ends `Serve`.

## Allowlist semantics

Verbatim `match.Host`. Exact name, subdomain (allow `example.com`
matches `api.example.com`), case-insensitive, trailing-dot stripped. IP
entries in the allowlist are skipped for names. `"*"` allows all. No new
wildcard syntax.

## Security properties

1. **Reduced allowlist leakage.** Both denied names and genuinely
   nonexistent names return NXDOMAIN, so the first-order distinction
   ("denied vs. unreachable") disappears. The synthesized NXDOMAIN has
   empty authority/additional sections; a determined attacker can still
   fingerprint it against a real upstream's response shape. Goal is to
   remove the cheap signal, not to be indistinguishable from upstream.
2. **No UDP reply spoofing.** Replies validated by source, ID, and
   question section.
3. **No ANY amplification through this daemon.** ANY is REFUSED.
4. **Bypass surface.** A client that ignores `/etc/resolv.conf` and
   queries an external resolver directly bypasses the filter. Mitigated
   in deployments where the container network blocks off-bridge UDP —
   `--internal` Docker networks (arizuko per-folder via
   `container/network.go:163`). The `crackbox run` standalone path uses
   `--internal=false` (`crackbox/pkg/run/run.go:60`) by design, since
   that path is dev/single-shot; the DNS filter is the only egress gate
   for names there, but not a sandbox boundary.
5. **Transparent-mode rebinding is unchanged.** See "Composition".
6. **UDP only.** No TCP/53 listener. Allowed names whose upstream answer
   sets the TC bit force clients to retry over TCP against whatever
   resolver `/etc/resolv.conf` would otherwise use — i.e. those lookups
   bypass crackbox. In `--internal` deployments TCP/53 is blocked anyway;
   in `crackbox run` (non-internal) it could succeed externally.
   Limitation, not a goal.

## Out of scope

- DoH / DoT. Mitigation belongs at the container egress (block UDP/853
  and known DoH IPs); separate spec.
- Internal split-horizon resolution.
- Recursive resolution.
- DNS-over-TCP.
- Transparent-mode destination-IP policy.
- arizuko consumer wiring (see "Scope"). Crackbox-internal only.

## Cross-references

- Openclaw: `refs/openclaw-managed-agents/docker/egress-proxy/proxy.mjs`
  — the shape was ported and the forwarder hardened (source/ID/question
  validation openclaw lacks).
- [`8-crackbox-standalone.md`](8-crackbox-standalone.md) — daemon shape,
  env naming, no-supervision rule.
- [`9-crackbox-sandboxing.md`](9-crackbox-sandboxing.md) — the arizuko
  egress consumer. Discovering the per-folder crackbox IP and adding
  `--dns` is follow-up work owned there.
- [`5/13`](../5/13-ext-mcp.md) — tool-level broker. Independent of
  egress; DNS filter and broker do not share state.

## Acceptance

Crackbox-local only; arizuko-side resolver pointing is asserted by `6/9`
once it lands.

- `go test ./crackbox/pkg/dns/...` green: allowlisted A/AAAA forwarded to
  a fake upstream; denied A/AAAA → NXDOMAIN echoed with original
  ID+question; QTYPE=ANY → REFUSED; reply from a non-upstream source
  addr → dropped; multi-question packet → dropped; compressed QNAME →
  dropped.
- `crackbox/test/egress_e2e_test.go` `TestE2E_Case9_DNSNXDomain` → PASS
  against the in-process `dns.Server`.
- `crackbox proxy serve` with default config opens UDP/53 in addition to
  the other listeners; SIGTERM closes it cleanly.
- `crackbox run --allow github.com -- getent hosts example.com` exits
  non-zero (NXDOMAIN); `... getent hosts github.com` exits zero.
