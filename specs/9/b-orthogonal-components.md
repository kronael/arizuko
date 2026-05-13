---
status: planned
---

# Orthogonal shippable components

> Sibling tools live in arizuko's repo, not inside it. Each builds,
> tests, ships, and runs without arizuko. arizuko consumes them
> through a published surface — never by reaching into their
> internals.

## Problem

arizuko keeps growing tools that are generic at the protocol level
but tangled with arizuko's domain at the source level. The first
example shipped today as `egred/`: a forward HTTP/HTTPS proxy with
default-deny and per-source allowlist. The runtime artifact has
nothing to do with groups, grants, gated, MCP, or any other arizuko
concept — yet its source lives under `egred/`, its allowlist
resolver lives in `store/network.go` reading arizuko's
`messages.db`, and its matcher keys IPs to "folders" because that's
what gated hands it.

The user flagged this as a layering inversion. A tool that does
default-deny network egress for arbitrary HTTP clients should be
runnable on a developer laptop, on a CI box, in a Kubernetes pod —
anywhere — and arizuko should be one of its consumers, not its
owner.

The same problem is approaching for two more components: a general
messaging gateway extracted from `gated/`, and an MCP firewall that
audits or filters tool calls. Each deserves the same treatment.

This spec defines the pattern that keeps such components orthogonal
to arizuko: each is a sibling tool inside the arizuko repo, builds
standalone, runs standalone, and is consumed by arizuko through a
thin published surface.

## Principle

A shippable component is a sibling directory (`crackbox/`,
`gateway/` — future, not the current internal package — `mcpfw/`)
that:

1. Builds to its own binary and Docker image with its own Makefile
   and Dockerfile. `make -C <component> image` is enough.
2. Ships a CLI usable from a shell with no arizuko process
   running. The component's CLI is its primary public surface;
   what the binary's subcommands look like is the component's
   business, not this spec's.
3. Has its own `README.md` describing its public surface — CLI
   flags, HTTP API, Go interfaces consumers can import. Anything
   not listed there is private.
4. Stores its own state. Filesystem, embedded SQLite, in-memory —
   whatever fits. It does not read or write arizuko's `messages.db`.
5. Reads its own configuration from env vars and CLI flags. It
   does not import `core.Config` or the arizuko `.env` loader.
6. Imports zero arizuko-internal packages. The orthogonality test
   is mechanical:

   ```
   $ grep -rE 'github.com/[^/]+/arizuko/(store|core|gateway|api|chanlib|chanreg|router|queue|ipc|grants|onbod|webd|gated)' <component>/
   <empty>
   ```

   Shared utilities are fair game only if extracted into a
   separately-published package. Today, nothing qualifies —
   components either own their utilities or do without.

7. Is consumed by arizuko through one of three contracts: the
   component's CLI, its HTTP API, or a Go package on its public
   import path (`<component>/pkg/...`). Internal packages
   (`<component>/internal/`) are off-limits to arizuko code.

The mirror to follow is `ant/`. It is a TypeScript project living
in arizuko's repo, but it has its own `Makefile`, `Dockerfile`,
`package.json`, and `tsconfig.json`; arizuko consumes it as a
container image with a known stdin/stdout protocol. It does not
import Go from the parent. That same contract — own build, own
runtime, narrow protocol — is what every shippable component
follows.

## Layout pattern

Every shippable component gets the same skeleton:

```
<component>/
  README.md         public surface: CLI, HTTP API, Go imports
  Makefile          build, test, lint, image
  Dockerfile        ships its own image
  CHANGELOG.md      its own version history
  cmd/<bin>/main.go entrypoint(s); name matches the binary
  pkg/<area>/       importable Go packages (the public surface)
  internal/         private; NOT importable from arizuko
  testdata/         fixtures for standalone tests
```

Allowed to differ: components written outside Go (TypeScript like
`ant/`) bring their own toolchain layout but keep the same outer
contract — `Makefile`, `Dockerfile`, `README.md`, no imports of
arizuko-internal packages.

What does not belong:

- A `<component>/.env` reader that knows about arizuko's keys.
- A `<component>/migrations/` directory that touches `messages.db`.
- A `<component>/CLAUDE.md` describing arizuko conventions.
- A dependency on `github.com/kronael/arizuko/...` other than for
  test fixtures explicitly under the component's own `testdata/`.

## Domain vs mechanism

The line that decides what goes where: **arizuko owns domain — what
an identifier means and which rule applies when. The component owns
mechanism — how to enforce the rule once arizuko hands it over.**

| Concern                                                      | Owner     |
| ------------------------------------------------------------ | --------- |
| What an `id` represents (folder, JID, user, …)               | arizuko   |
| Hierarchy walks (folder ancestry, group tree, …)             | arizuko   |
| Grants, tier derivation, policy composition                  | arizuko   |
| Persisting policy rules (per-folder, per-user, audit-logged) | arizuko   |
| When to spawn an agent, when to send a message               | arizuko   |
| A flat per-`id` ruleset (allowlist, tool list, route table)  | component |
| Match a request against a ruleset                            | component |
| Open/close sockets, dial upstream, run a Docker network      | component |
| Persist runtime state (live ids, last-seen timestamps)       | component |

Concretely for the egress case: arizuko walks the folder tree,
collects rules from `network_rules`, and hands the component a flat
list keyed by an opaque id. The component matches incoming
connections against that list. It never sees `folder`, never reads
`messages.db`, never knows the tree existed.

Persistence has one nuance worth calling out. arizuko stores the
**rules** (per-domain-object, joined with grants, audit-logged).
The component may store **runtime state** (which id is currently
live, when it last connected). Mixing the two — letting the
component persist arizuko's per-folder rules — collapses the
boundary. Don't.

## Component catalog

One paragraph per component. The actual public surface, CLI,
shipping plan, and footprint live in each component's own spec
under `specs/9/`. This file is the pattern, not the plans.

### crackbox

Sandbox-and-egress umbrella component. Two distinct things ship
under this name:

- **`egred`** — forward HTTP/HTTPS proxy daemon. Per-source-IP
  allowlist, admin API, runnable standalone or under `crackbox
proxy serve`. Stateless about what's behind the source IPs. See
  [`specs/9/9-crackbox-standalone.md`](9-crackbox-standalone.md).
- **`crackbox/pkg/host/`** — Go library for KVM/qemu sandbox
  lifecycle. Spawns VMs, manages privileges, ensures egred is up.
  Imported by `sandd` (for arizuko deployments) and by the
  `crackbox run --kvm` CLI (for laptop one-shots). See
  [`specs/9/12-crackbox-sandboxing.md`](12-crackbox-sandboxing.md).

The two halves ship in the same component so `crackbox run` can
compose them into a one-shot sandboxed-execution CLI without an
extra dependency. arizuko consumes them separately: long-lived
egred container in compose; the host library will be imported by
sandd when KVM backend lands. The proxy half is consumer-side
shipped today; the host library is planned (next phase).

[`specs/9/10-crackbox-arizuko.md`](10-crackbox-arizuko.md)
covers the today-and-tomorrow consumer pattern and the planned
[`sandd`](c-sandd.md) extraction. KVM lands as one backend behind
the `ContainerRuntime` seam — see
[`specs/6/R-genericization.md`](../6/R-genericization.md) §
_ContainerRuntime — pluggable sandbox backends_.

### messaging-gateway (future, extracted from gated)

Generic message router. Owns the channel registry, normalized
inbound/outbound types, route table, and outbound dispatch. Knows
nothing about groups, grants, folders, agents, or sessions. No
spec yet — extraction starts when a second consumer appears or
when `gated/` outgrows its current envelope.

### mcp-firewall (future, new)

Transparent MCP proxy that sits between an agent and its MCP
servers and filters JSON-RPC tool calls by a ruleset. arizuko
owns the per-folder tool policy (derived from grants); the
firewall takes a flat rule list. No spec yet.

## Boundaries and anti-patterns

What goes wrong when these are violated:

- **Importing arizuko-internal types into a component.** Every
  domain type (`Folder`, `Grant`, `Tier`, `JID`) you accept in a
  component's signature couples it to arizuko forever. Components
  take strings (`id string`, `host string`, `tool string`) and
  return strings.
- **Adding a domain flag to a component.** `--id` is right;
  `--folder` is arizuko leaking through.
- **Sharing a SQLite database across boundaries.** Each component
  that persists state owns its DB file. arizuko's `messages.db`
  stays in arizuko.
- **Pulling component config from arizuko's `.env`.** The
  component's binary reads its own env vars. arizuko's compose
  generator sets those env vars when wiring the container.

## Versioning

One go.mod for the whole arizuko tree, including components. Each
component's `README.md` declares its public surface. Internal
packages (`<component>/internal/`) carry no stability guarantee
even between point releases.

`CHANGELOG.md` per component documents that component's surface
changes. The root `CHANGELOG.md` continues to track arizuko-wide
changes and references component changelogs by path.

Docker image tags follow arizuko's tag (`arizuko-<component>:vX.Y.Z`,
matching `arizuko:vX.Y.Z`). Components stay in arizuko's single
`go.mod` — we don't split them into separate Go modules. External
users consume components through their CLI and Docker image, not as
imported Go libraries.

## Acceptance

The pattern is in force when, for every component listed in the
catalog above:

- `grep -rE 'github\.com/[^/]+/arizuko/(store\|core\|gateway\|api\|chanlib\|chanreg\|router\|queue|ipc\|grants\|onbod|webd|gated)' <component>/` returns
  nothing outside the component's own `package` declarations.
- `make -C <component> build && make -C <component> test` passes
  on a host with no arizuko process and no arizuko data directory.
- `<component>/README.md` is sufficient to use the binary;
  reading arizuko's docs is unnecessary.
- arizuko consumes the component only through its CLI, its HTTP
  API, or its `pkg/` import paths.
