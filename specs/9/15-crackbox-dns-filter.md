---
status: spec
depends: [9-crackbox-standalone, 10-crackbox-arizuko]
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

Openclaw's reference does this in ~80 LOC
(`refs/openclaw-managed-agents/docker/egress-proxy/proxy.mjs:220-251`,
helpers in `dns.mjs`). Port the shape; tighten the bits the
reference handwaves.

## Scope

This spec covers **only** the crackbox-internal DNS server and its
wiring into `crackbox proxy serve` / `crackbox run`. Two related
consumer changes are out of scope here, each owns its own change:

- **arizuko spawn path** (`container/egress.go`, `container/runner.go`):
  needs to discover the crackbox container's IP on each per-folder
  network and pass `--dns <ip>` to `docker create`. The current
  egress path uses the `crackbox` Docker DNS alias for
  `HTTPS_PROXY` but never resolves it to an IP
  ([`container/egress.go:13-44`](../../container/egress.go),
  [`container/network.go:177`](../../container/network.go)).
  Tracked as a follow-up under [`9/10`](10-crackbox-arizuko.md).
- **transparent-mode rebinding boundary**: see below.

Inside scope: a UDP/53 listener bundled with `crackbox proxy serve`
plus a `--dns` flag added to `crackbox run`.

## Mechanism

`crackbox proxy serve` opens an additional UDP listener. Per query:

1. Parse the first question's QNAME. Anything that does not parse
   cleanly → drop silently. No FORMERR; less actionable for
   scanners. Includes QDCOUNT > 1.
2. **`QTYPE == ANY` → REFUSED**, regardless of allowlist. ANY is
   not useful for libc resolution and would make the daemon a
   potential reflector for large upstream answers.
3. `Registry.Allow(src, qname)` — the existing per-source-IP check
   (`crackbox/pkg/admin/registry.go:139`), backed by `match.Host`
   (`crackbox/pkg/match/match.go:46-64`). Subdomain semantics
   identical to the HTTP path.
4. **Allow** → forward to the configured upstream resolver
   (§"Forwarder hygiene"); relay the reply back.
5. **Deny / unregistered source** → synthesize NXDOMAIN echoing the
   original ID + question.

## Forwarder hygiene

The Openclaw forwarder opens a fresh UDP socket per query and
relays whichever reply arrives first (`proxy.mjs:236-250`). Ported
naively this lets a same-network attacker race a forged reply into
the ephemeral socket. Tighten:

1. **Source-address check.** The reply must come from the
   configured upstream addr (`net.UDPAddr` equality on IP+port).
   Anything else is dropped and the wait continues until timeout.
2. **ID + question match.** The reply's first 12 bytes (ID + flags
   stripped of QR) must match the query's, and the echoed
   question section must byte-equal the query's. Drop on mismatch.
3. **Per-query socket, bounded lifetime.** One UDP socket per
   in-flight query, closed on first valid reply or after a fixed
   timeout (3 s, matches openclaw). No state between queries.
4. **No EDNS rewriting.** Bytes go through verbatim in both
   directions.

This is `net.DialUDP` to the upstream + a single read with deadline.
Maybe 25 LOC; cheaper than running a connected udp.PacketConn pool.

## Where it lives

- `crackbox/pkg/dns/` (new) — `server.go` (listener, handler,
  NXDOMAIN synth, question parse), `forward.go` (per-query
  dial+read with validation), `server_test.go`. Layout mirrors
  `pkg/proxy/`.
- `crackbox/cmd/crackbox/main.go` (`cmdProxy`) — new
  `dns.NewServer(...)` constructed alongside `proxy.New(...)`,
  exposing `Serve(net.PacketConn) error` and `Close() error`
  (matches the shape of the transparent listener at
  `main.go:152-165`).

No new binary. `cmd/egred/` is still upcoming per spec 9; when it
lands it inherits the same wiring.

## Wire

RFC 1035 header (12 bytes: ID, flags, QDCOUNT, ANCOUNT, NSCOUNT,
ARCOUNT) + one question (length-prefixed labels + null, QTYPE,
QCLASS). **Pointer compression (0xC0) in QNAMEs is rejected as
malformed.** Real resolvers do not compress queries (only
responses); rejecting keeps the parser tight and removes a class of
pointer-loop bugs. The parser's contract is binary: clean
`(name, qtype)` or the caller drops the packet.

NXDOMAIN response: copy header+question verbatim, set QR=1
RCODE=3, preserve RD echo, zero AN/NS/AR. No SOA in authority —
the cut is exactly the name asked (RFC 8020-aligned). No glue.

## Composition with other paths

The DNS filter helps any client whose `/etc/resolv.conf` points at
the crackbox listener. It does **not** automatically defend:

- **Transparent mode** (`crackbox/pkg/proxy/transparent.go:67-94`):
  dials the SO_ORIGINAL_DST IP directly, not the hostname. A
  rebinding attack on an allowlisted name resolves to a hostile IP
  via the DNS filter (forwarded upstream answer), and transparent
  mode would splice to that IP. Out-of-scope mitigation: a
  destination-IP policy layer on transparent. This spec does not
  change transparent's behavior; it does not regress it either.
- **Forward proxy** (CONNECT / HTTP): dials by name after the
  client's resolver returns an address, so the name is the unit of
  enforcement. The HTTP path remains the second gate; DNS is
  additive defense-in-depth.
- **Spec 11 selective MITM**: same as forward proxy — name-based,
  reads the same `Registry`. Cross-ref:
  [`9/11 §"Spec format"`](11-crackbox-secrets.md).

## Container-side wiring

`docker create --dns <ip>` writes `nameserver <ip>` into the
container's resolv.conf.

`crackbox/pkg/run/run.go:100-109` already knows the proxy
container's IP (`proxyIP` at `run.go:73-76`); add `--dns <proxyIP>`
to the user container's create args.

The arizuko-side change is **not** one-line and is **not** part of
this spec. It needs `EgressConfig.CrackboxIPOnNetwork(folder)` or
equivalent. Owned by [`9/10`](10-crackbox-arizuko.md).

## Config

Aligned with existing `CRACKBOX_PROXY_ADDR` shape
(`crackbox/README.md:121-128`):

| Env / TOML key                                 | Default      | Purpose                                                          |
| ---------------------------------------------- | ------------ | ---------------------------------------------------------------- |
| `CRACKBOX_DNS_ADDR` / `proxy.dns_listen`       | `:53`        | UDP listen; empty disables                                       |
| `CRACKBOX_DNS_UPSTREAM` / `proxy.dns_upstream` | `1.1.1.1:53` | Forward target                                                   |
| `--dns-listen <addr>` flag                     | (unset)      | `crackbox proxy serve` override (distinct from Docker's `--dns`) |
| `--dns-upstream <addr>` flag                   | (unset)      | upstream resolver override                                       |

Precedence: flag > env > config (`crackbox/cmd/crackbox/main.go:63-71`).
Empty-string disable matches the existing
`proxy.transparent_listen` pattern (`pkg/config/config.go:32`,
`:166`); requires the same `Defaults`/`Load`/`validate` triple to
recognize the new key. README env-var table grows two rows.

Lifecycle: the DNS server owns its `net.PacketConn`. `Serve`
returns when `Close` is called, mirroring how
`transparentLis.Close()` ends `ServeTransparent`
(`crackbox/pkg/proxy/transparent.go:31-42`). Bind failure at
startup is fatal (`os.Exit(1)`), same as the other listeners.

## Allowlist semantics

Verbatim `match.Host`. Exact name, subdomain (allow `example.com`
matches `api.example.com`), case-insensitive, trailing-dot
stripped. IP entries in the allowlist are skipped for names. `"*"`
allows all. No new wildcard syntax.

## Decisions

- **Forwarder, not recursor.** Upstream does recursion.
- **REFUSED for ANY.** Defense-in-depth against amplification;
  zero loss for normal libc traffic.
- **Drop malformed and multi-question silently.** One policy, one
  code path.
- **Source-IP and ID/question validation on UDP replies.**
  Strictly stronger than the openclaw reference; the minimum to
  be safe against same-LAN spoofing.
- **UDP only in v1.** No TCP/53 listener. Allowed names whose
  upstream answer sets the TC bit will force clients to retry over
  TCP against whatever resolver `/etc/resolv.conf` would have used
  in absence of the filter — i.e. those lookups bypass crackbox.
  In `--internal` deployments TCP/53 to external resolvers is
  blocked anyway, so the lookup just fails; in `crackbox run`
  (non-internal) it could succeed externally. Limitation, not a
  goal.
- **No cache, no EDNS rewriting.** Stateless.

## Security properties

1. **Reduced allowlist leakage.** Both denied names and genuinely
   nonexistent names return NXDOMAIN, so the first-order distinction
   ("denied vs. unreachable") disappears. The synthesized NXDOMAIN
   has empty authority/additional sections; a determined attacker
   can still fingerprint it against a real upstream's response
   shape. Goal here is to remove the cheap signal, not to be
   indistinguishable from the upstream.
2. **No UDP reply spoofing.** Replies validated by source, ID, and
   question section.
3. **No ANY amplification through this daemon.** ANY is REFUSED.
4. **Bypass surface.** A client that ignores `/etc/resolv.conf`
   and queries an external resolver directly bypasses the filter.
   Mitigated in deployments where the container network blocks
   off-bridge UDP — `--internal` Docker networks
   (arizuko per-folder via `container/network.go:163`). The
   `crackbox run` standalone path uses `--internal=false`
   (`crackbox/pkg/run/run.go:60`) — by design, since that path
   is dev/single-shot; the DNS filter is the only egress gate for
   names there, but not a sandbox boundary.
5. **Transparent-mode rebinding is unchanged.** See "Composition".

## Out of scope

- DoH / DoT. Mitigation belongs at the container egress
  (block UDP/853 and known DoH IPs); separate spec.
- Internal split-horizon resolution.
- Recursive resolution.
- DNS-over-TCP.
- Transparent-mode destination-IP policy.
- arizuko consumer wiring (see "Scope"). Crackbox-internal only.

## Touches

- `crackbox/pkg/dns/{server,forward,server_test}.go` (new)
- `crackbox/pkg/config/config.go` — `proxy.dns_listen`,
  `proxy.dns_upstream`, with the same empty-disable handling as
  `transparent_listen`
- `crackbox/cmd/crackbox/main.go` (`cmdProxy`) — start + shutdown
  the DNS server alongside proxy/admin/transparent; new flags
- `crackbox/pkg/run/run.go` — Docker `--dns <proxyIP>` on
  `docker create` (this is Docker's flag, not the crackbox one)
- `crackbox/README.md` — two new env-var rows
- `crackbox/test/egress_e2e_test.go` —
  `TestE2E_Case9_DNSNXDomain` flipped from skip to pass against
  the in-process DNS server (no external resolver dependency)

## Cross-references

- Openclaw: `refs/openclaw-managed-agents/docker/egress-proxy/proxy.mjs:220-251`
  - `.../dns.mjs` (porting the shape, hardening the forwarder).
- [`9/9-crackbox-standalone.md`](9-crackbox-standalone.md) — daemon
  shape, env naming, no-supervision rule.
- [`9/10-crackbox-arizuko.md`](10-crackbox-arizuko.md) — consumer.
  Discovering the per-folder crackbox IP and adding `--dns` is
  follow-up work owned there.
- [`9/11-crackbox-secrets.md`](11-crackbox-secrets.md) — selective
  MITM. DNS filter sits in front for forward-proxy clients;
  transparent path independent.

## Acceptance

Crackbox-local only. Arizuko-side resolver pointing is asserted by
9/10 once it lands.

- `go test ./crackbox/pkg/dns/...` green; covers:
  - allowlisted A and AAAA → forwarded to a fake upstream;
  - denied A and AAAA → NXDOMAIN echoed with original ID+question;
  - QTYPE=ANY → REFUSED regardless of allowlist;
  - reply from a non-upstream source addr → dropped;
  - multi-question packet → dropped (no response);
  - compressed QNAME → dropped.
- `crackbox/test/egress_e2e_test.go` case 9
  (`TestE2E_Case9_DNSNXDomain`) → PASS against the in-process DNS
  server, asserting the same four allow/deny/ANY/spoof outcomes
  through the package's public Server type.
- `crackbox proxy serve` with default config opens UDP/53 in
  addition to existing listeners; SIGTERM closes it cleanly.
- `crackbox run --allow github.com -- getent hosts example.com`
  exits non-zero (NXDOMAIN); `... getent hosts github.com`
  exits zero.

## Refinement notes

Pass 1: minimality + orthogonality + spec 11 composition (see
prior draft).

Pass 2 (after oracle, structural rework):

- **Scope**: split arizuko consumer plumbing out. Made
  `--dns <crackbox-ip>` for arizuko an explicit follow-up under
  9/10 instead of "one-line". The crackbox container's IP on each
  per-folder network is not known to `container/egress.go` today.
- **ANY**: changed from "forward verbatim" to **REFUSED**.
  Removes amplification surface; zero loss for normal traffic.
- **Forwarder hygiene**: added explicit reply validation (source
  IP, ID, echoed question). Openclaw's reference doesn't do this;
  porting naively would be unsafe on a multi-tenant LAN.
- **FORMERR vs drop**: unified — everything we don't parse cleanly
  (incl. QDCOUNT > 1) is silently dropped. One policy, one path.
- **Transparent-mode rebinding**: added a "Composition" section
  saying transparent dials by orig-dst IP not by name; DNS filter
  does not defend that path; out-of-scope mitigation lives in a
  destination-IP policy on transparent.
- **Lifecycle**: spelled out that the DNS server owns its
  `PacketConn` and `Close` ends `Serve`; bind failure fatal.
- **`--internal` claim**: scoped narrowly. arizuko's per-folder
  networks ARE `--internal`; `crackbox run` is NOT. The DNS filter
  is not a sandbox boundary in the latter.
- **Config**: noted that empty-disable needs the same
  `Defaults`/`Load`/`validate` triple as `transparent_listen`.

Pass 3 (after oracle, one-polish):

- Acceptance scoped purely crackbox-local; arizuko resolver wiring
  acceptance left to 9/10.
- Renamed crackbox CLI flag to `--dns-listen` / `--dns-upstream` so
  it doesn't collide with Docker's `--dns`.
- Pointer-compressed QNAMEs explicitly rejected (real resolvers
  don't compress queries; removes a parser-edge class).
- Narrowed "no enumeration" claim to "reduced allowlist leakage";
  acknowledged synthesized NXDOMAIN can still be fingerprinted
  against upstream shape.
- TC-bit / TCP-fallback bypass called out as a v1 limitation in
  the non-`--internal` case.
