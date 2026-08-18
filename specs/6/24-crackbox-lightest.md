---
status: shipped
shipped: 2026-08-18
depends:
  [
    8-crackbox-standalone,
    16-daemon-standalone-matrix,
    17-egress-credential-proxy,
  ]
---

# specs/6/24 — crackbox, lightest

> Shipped 2026-08-18. Supersedes the shared-proxy half of `6/8`. Two
> corrections the build forced are marked in place: `--network container:`
> is an open bypass (measured), and a per-spawn proxy needs `--client` to
> stop a neighbour borrowing an allowlist it was not granted.

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

## Two substrates, one design

The per-spawn proxy is not Docker-shaped. Where it ATTACHES differs; what
it is does not.

**Container (today).** Start the proxy container first, wait for its
`/health`, then start the agent on the folder's `--internal` network with
`HTTP_PROXY` pointing at the proxy's address there. The proxy is attached
to that network AND to an uplink; the agent is attached to the internal
network only. Enforcement is topological: the agent's network has no route
out, so the proxy is not what stops it — the missing route is.

> **`--network container:<proxy>` does NOT work here — measured, not
> reasoned.** Sharing the proxy's network namespace was proposed as the pod
> primitive that removes the ordering race. It removes the enforcement
> instead: the proxy must reach the internet to forward, so its namespace
> carries a default route, and everything sharing that namespace inherits
> it. On this docker host, a container run with
> `--network container:<proxy>` and NO proxy environment fetched
> `http://example.com` directly, exit 0. `HTTP_PROXY` would become
> advisory — any raw socket, any IP literal, any client ignoring the env
> var walks straight out. The `--internal` network is the enforcement and
> must stay; the agent must never share the proxy's namespace or hold a
> second interface.

**VM (`6/9`, shipped but unwired).** A guest has exactly one virtual NIC —
a tap the host created. Put the proxy on the host side of that tap and
the guest's only route is through it, enforced below the kernel it runs.
Stronger than the namespace case by a category, not a degree.

**The VM case is also the simpler one**: there is nothing to register,
because the tap IS the identity. A registry exists to answer "which
tenant is this source IP?", and that question does not arise when the
answer is "the one on the other end of this tap".

Note `crackbox/pkg/host` already carries `EgressProxy` and registers the
VM with the shared registry on spawn (`host.go:104`), unregistering on
stop — so **the KVM path inherits the same unregister leak** described
above. Per-spawn removes it on both substrates.

Wiring KVM into `runed` is separate work and out of scope here: `runed`
spawns Docker today, `6/9` records `pkg/host` as shipped-but-unwired, and
`6/20` declined the warm-pool lifecycle that neighbours it. This spec only
requires that the design not foreclose the VM path — it does not.

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

**DECIDED 2026-08-18 — split three ways, and `crackbox` becomes a local
development command.**

| Piece       | Is                                                                                        | Used by                                 |
| ----------- | ----------------------------------------------------------------------------------------- | --------------------------------------- |
| `egress/`   | the forward proxy: library + small binary                                                 | `runed`, per spawn, in production       |
| `sandbox/`  | the KVM/qemu lifecycle library (`6/9`)                                                    | whatever wires VMs, when something does |
| `crackbox/` | **a user-facing command that stands up parts of the arizuko stack for local development** | a developer working on one piece        |

The first two are what production consumes: runed starts an `egress`
proxy per spawn and never runs `crackbox` at all. `crackbox` is the
developer's tool — stand up a sandboxed VM with filtered egress on your
own machine, or bring up a subset of the stack without the full
twenty-daemon compose.

**This retires the packaging problem rather than solving it.** crackbox
shares arizuko's `go.mod`, so a stranger cannot `go install` it — fatal
for a component meant to ship outward, irrelevant for a command used by
people who have already cloned the repo. The friction only existed
because the tool was pointed at the wrong audience.

It also settles the stranger question the same way the Anthropic finding
does (below): nothing here is shipped outward. `egress` and `sandbox` are
libraries this codebase consumes; `crackbox` is how a developer runs them
locally. If an outside consumer ever appears, they take a library, not a
bundle.

## The resulting layout

Three directories where there is one, all still inside arizuko's single
`go.mod` (root `CLAUDE.md` — siblings share the module; orthogonality is
enforced by the import graph, not by module boundaries):

```
egress/            the proxy: forward-only, static allowlist, one client
  proxy.go         parse CONNECT, match, splice; /health (from pkg/proxy)
  match/           allowlist globbing                    (from pkg/match, unchanged)
  cmd/egress/      the binary runed starts per spawn
  test/            end-to-end over real sockets          (from crackbox/test)
sandbox/           the KVM/qemu lifecycle library        (from crackbox/pkg/host)
crackbox/          the local-development command
  cmd/crackbox/    `crackbox run [--kvm]` — a sandbox + filtered egress
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

### Two things the build added, and why

**`--client <ip>`: the proxy serves exactly one address.** Per-spawn
proxies share the folder's network with the folder's other spawns, and
those spawns do NOT all hold the same allowlist — `elevated` is per-turn
and one-shot (`routd/dispatch.go:123`), so a concurrent elevated turn has
an unrestricted `*`. Without the pin, a non-elevated spawn could send its
CONNECT to the elevated spawn's proxy and inherit it. The shared model
closed this by keying on source IP; per-spawn has to close it too. This is
a bind scope, not a registry: nothing is registered or looked up, and a
second client is refused rather than served from another rule set.

**`GET /health`, and the start order it buys.** The proxy answers 200 only
once its listener is bound and its allowlist loaded. runed polls it and
starts the agent ONLY on a 200; on timeout the spawn fails. A TCP connect
would not do — it succeeds the moment the port is bound. The probe is
answered before the `--client` check, since runed is not the client, and
origin-form `GET /health` is unambiguously a probe: proxy traffic arrives
in absolute form.

An orphan proxy would hold a live allowlist on a folder network, so runed
sweeps them by label at startup — a runed that died mid-turn cannot have
run its own teardown.

## Setup, and what changes for a live instance

Today an instance runs ONE long-lived container — `arizuko_crackbox_<inst>`
is live on krons, sloth and marinade — wired by a compose fragment, with
its admin URL and secret in the instance `.env`. runed registers each
spawn's IP against it.

After the cut there is **no long-lived crackbox container**. runed starts
a proxy per spawn from the same image and stops it with the turn.

| Piece                               | Today                          | After                                       |
| ----------------------------------- | ------------------------------ | ------------------------------------------- |
| `arizuko_crackbox_<inst>` container | always up                      | gone                                        |
| compose service                     | emitted, profile-gated         | not emitted; the image is run, not served   |
| `CRACKBOX_ADMIN_API` / `_SECRET`    | how runed reaches the registry | gone with the registry                      |
| `EGRESS_CRACKBOX` / `_PROXY_URL`    | container name, proxy URL      | gone; each spawn's proxy has its own        |
| `EGRESS_IMAGE`                      | —                              | **new, and the master switch**              |
| `EGRESS_UPLINK_NETWORK`             | —                              | **new**; compose writes `<project>_default` |
| `EGRESS_NETWORK_PREFIX` / `_SUBNET` | per-folder networks            | **unchanged**                               |
| `network_rules` table               | source of the allowlist        | **unchanged** — same rows, same meaning     |
| per-folder semantics                | glob per folder, `*` = open    | **unchanged**                               |

The operator-visible surface is therefore _smaller_, not different: the
same `arizuko network allow/deny` commands write the same rows, and the
same folders get the same reach. Live krons rules today include
instance-global `anthropic.com` / `api.anthropic.com` and per-folder
entries, some of which are `*` (open) — all of which mean exactly what
they mean now.

**Migration on upgrade**, in order — all three live instances (krons,
sloth, marinade) set `CRACKBOX_ADMIN_API` today, so all three need it:

1. `make images` — builds `arizuko-egress`. Do this FIRST: without the
   image every spawn fails.
2. In the instance `.env`, replace `CRACKBOX_ADMIN_API=...` with
   `EGRESS_IMAGE=arizuko-egress:latest`.
3. `arizuko generate` — rewrites `env/runed.env` and drops the crackbox
   service from the compose file.
4. `sudo systemctl restart arizuko_<inst>` — compose removes the
   now-undeclared `arizuko_crackbox_<inst>` container.

Nothing in the data dir changes and `network_rules` is never touched, so a
rollback is restoring the two `.env` lines.

**A stale `.env` cannot silently disable filtering.** `EGRESS_IMAGE` unset
reads as "egress not configured", so an instance upgraded without step 2
would spawn unfiltered while looking healthy. runed therefore REFUSES TO
START when `CRACKBOX_ADMIN_API` is set and `EGRESS_IMAGE` is not, naming
both vars. Loud, once, at deploy — not per turn, and never silent.

**The inert path stays inert.** An instance with no egress image
configured spawns exactly as today, unfiltered — `EgressConfig.active()`
gates the whole path. Filtering must remain opt-in at the same point it
is opt-in now, or upgrading silently changes what existing instances can
reach.

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
