---
status: shipped
shipped: 2026-08-18
depends: [16-daemon-standalone-matrix]
---

# specs/6/24 — crackbox, lightest

Per-folder egress is enforced by the smallest thing that can: **one forward
proxy per spawn, statically configured, forward mode only.** It is roughly
**250–300 lines** and no new dependency. That is why it is a cut rather than
an adoption: at that size, taking tinyproxy or iron-proxy would add a C or
third-party dependency, a config renderer and process supervision to replace
less code than it costs. The wheel here is `net/http` — already in use, not
reinvented.

## The shape

- runed starts a proxy alongside each spawned container with that
  folder's allowlist baked in, and it dies with the turn.
- The container gets `HTTP_PROXY`/`HTTPS_PROXY` pointing at it.

The proxy parses `CONNECT`, matches the host against a fixed list, and
splices or refuses; `match/` does the allowlist globbing. The host comes
from `r.Host` — the `CONNECT host:443` request line the client sends when
`HTTP_PROXY` points at the proxy — so nothing peeks at SNI. The proxy never
terminates TLS: no CA, no key material and no request-body access, which is
what keeps 250 lines auditable by someone who does not trust us.

runed builds the `EgressConfig` from its own config plus the folder's
static list, once, at spawn (`runed/docker.go:127`); there is no mid-run
update anywhere in the tree.

### Why per-spawn

Docker reuses source IPs. A shared proxy keyed on source IP needs the
consumer to register before traffic and unregister on exit, and a missed
unregister — runed dying between a container exiting and the unregister —
hands the next tenant on that IP the previous folder's allowlist. Per-spawn
has no shared state to go stale and no IP to reuse.

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
> reasoned.** Sharing the proxy's network namespace removes the
> enforcement: the proxy must reach the internet to forward, so its
> namespace carries a default route, and everything sharing that namespace
> inherits it. On this docker host, a container run with
> `--network container:<proxy>` and NO proxy environment fetched
> `http://example.com` directly, exit 0. `HTTP_PROXY` would become
> advisory — any raw socket, any IP literal, any client ignoring the env
> var walks straight out. The `--internal` network is the enforcement and
> must stay; the agent must never share the proxy's namespace or hold a
> second interface.

**VM (`6/9`).** A guest has exactly one virtual NIC — a tap the host
created. Put the proxy on the host side of that tap and the guest's only
route is through it, enforced below the kernel it runs. Stronger than the
namespace case by a category, not a degree.

**The VM case is also the simpler one**: there is nothing to register,
because the tap IS the identity. A registry exists to answer "which
tenant is this source IP?", and that question does not arise when the
answer is "the one on the other end of this tap".

## Three parts

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

**The packaging question does not arise.** crackbox shares arizuko's
`go.mod`, so a stranger cannot `go install` it — fatal for a component
meant to ship outward, irrelevant for a command used by people who have
already cloned the repo.

It also settles the stranger question the same way the Anthropic finding
does (below): nothing here is shipped outward. `egress` and `sandbox` are
libraries this codebase consumes; `crackbox` is how a developer runs them
locally. If an outside consumer ever appears, they take a library, not a
bundle.

## The layout

Three directories, all inside arizuko's single `go.mod` (root `CLAUDE.md` —
siblings share the module; orthogonality is enforced by the import graph,
not by module boundaries):

```
egress/            the proxy: forward-only, static allowlist, one client
  proxy.go         parse CONNECT, match, splice; /health
  match/           allowlist globbing
  cmd/egress/      the binary runed starts per spawn
  test/            end-to-end over real sockets
sandbox/           the KVM/qemu lifecycle library
crackbox/          the local-development command
  cmd/crackbox/    `crackbox run [--kvm]` — a sandbox + filtered egress
```

**DNS is not sealed.** A DNS query never appears in a CONNECT line, so the
proxy cannot see it, and the `--internal` network does not stop it: a
container on one still resolves external names through docker's embedded
resolver (`127.0.0.11`), which forwards from the HOST namespace. Measured
2026-08-30 — `getent hosts example.com` on an `--internal` network with no
proxy env returned real addresses, exit 0, while `curl` to the same host
timed out. TCP is sealed; UDP/53 is not. The residual channel is BUGS
`L136`.

runed starts the spawn's proxy with its allowlist and sets `HTTP_PROXY`;
the `EgressConfig` it builds at spawn (`runed/docker.go:127`) is the input.

### `--client` and `/health`

**`--client <ip>`: the proxy serves exactly one address.** Per-spawn
proxies share the folder's network with the folder's other spawns, and
those spawns do NOT all hold the same allowlist — `elevated` is per-turn
and one-shot (`routd/dispatch.go:123`), so a concurrent elevated turn has
an unrestricted `*`. Without the pin, a non-elevated spawn could send its
CONNECT to the elevated spawn's proxy and inherit it. This is a bind
scope, not a registry: nothing is registered or looked up, and a second
client is refused rather than served from another rule set.

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

## Setup

There is no long-lived egress container: runed starts a proxy per spawn
from the egress image and stops it with the turn.

| Piece                               | Role                                                                  |
| ----------------------------------- | --------------------------------------------------------------------- |
| `EGRESS_IMAGE`                      | **the master switch**; unset reads as "egress not configured"         |
| `EGRESS_UPLINK_NETWORK`             | the proxy's uplink; compose writes `<project>_default`                |
| `EGRESS_NETWORK_PREFIX` / `_SUBNET` | per-folder networks                                                   |
| `network_rules` table               | source of the allowlist; `arizuko network allow/deny` writes the rows |
| per-folder semantics                | glob per folder, `*` = open                                           |

**A stale `.env` cannot silently disable filtering.** An instance that
sets `CRACKBOX_ADMIN_API` and not `EGRESS_IMAGE` would spawn unfiltered
while looking healthy, so runed REFUSES TO START in that state, naming
both vars. Loud, once, at deploy — not per turn, and never silent.

**Filtering is opt-in.** An instance with no egress image configured
spawns unfiltered — `EgressConfig.active()` gates the whole path.

## Why the parts stay small

Anthropic ships sandboxing and network restriction inside Claude Code
itself, plus the Agent SDK and hosted Managed Agents. A standalone
egress-filtering proxy competes with a feature the stranger already has,
free, in the tool they are already using. Packaging polish does not change
that.

What arizuko has that Anthropic does not is the COMPOSITION — folder
multitenancy with per-folder egress, ACL, secrets, memory and web mounts,
across ten-plus channels, self-hosted on the operator's own data. The
parts are not the product. That is the reason to make the parts small and
boring rather than to market them.

## Ties

`6/9` (`sandbox/`, the other half) · `6/16` (ship one, not seven) · BUGS
`L48` (the proposal and its measurements) · BUGS `L136` (the DNS channel
this design does not close).
