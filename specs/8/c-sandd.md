---
status: deferred
---

# sandd — sandbox lifecycle daemon

> Deferred. For the upcoming KVM work, `gated` keeps spawn
> ownership and imports [`crackbox/pkg/host/`](a-crackbox-sandboxing.md)
> directly via a Backend interface. Extracting spawn into a
> separate `sandd` daemon stays a future privilege-isolation
> improvement — not on the immediate path.

## Why deferred

The original motivation for `sandd` was privilege isolation —
keep `gated` nonroot by moving the docker-socket and `/dev/kvm`
access into a dedicated minimal daemon. Sound reasoning, but
the practical cost is high right now:

- `gated` already mounts `docker.sock` (root-equivalent). Adding
  `/dev/kvm` + `CAP_NET_ADMIN` is incremental, not a new attack
  surface.
- Extracting requires a new daemon, new compose service, new
  socket protocol, new client/server pair, and migration of the
  egress-register code path. That's a lot of surface for a
  deferred-from-zero-visible-symptom security hardening.
- The library-only path (`gated` imports `crackbox/pkg/host/`)
  ships KVM faster and lets us validate the VM backend before
  deciding the daemon split is worth the cost.

When this UN-defers: when a real symptom shows up (multi-tenant
host, untrusted operator account, audit requirement) that
benefits from the gated/sandbox-spawn privilege split.

## Problem

`gated` today owns the Docker-spawn privilege: mounts `/var/run/docker.sock`,
shells out to `docker run`, manages the container lifecycle. That makes
`gated` effectively root on the host (the docker socket grants root-equivalent
access). Adding a second backend (KVM/qemu via the [crackbox
library](a-crackbox-sandboxing.md)) would compound the privilege surface.

The right move is **separation of privilege**: `gated` stays nonroot, owns
routing + persistence + decisions. A new minimal daemon `sandd` takes the
spawn privilege, exposes a tiny API for "run an agent in folder X with this
prompt," and that's it. `sandd` knows two backends; `gated` knows neither.

## Scope

`sandd` owns:

- The privileged primitive: spawn a sandbox, wait, reap.
- One pluggable Backend interface with two implementations:
  1. **Docker backend** — current path, `docker run` shells out.
  2. **Crackbox backend** — KVM/qemu via the [crackbox library](a-crackbox-sandboxing.md);
     library handles VM lifecycle + ensures egred (egress proxy) is up.
- Resource budget enforcement — concurrent-spawn cap, per-folder pool size.
- Reaping orphans on startup (containers/VMs left over from a prior crash).

`sandd` does NOT own:

- Folder/group/grant resolution (that's `gated`'s domain — `sandd`
  takes opaque strings).
- Outbound network policy (that's egred's job).
- Channel routing, persistence, MCP — none of arizuko's domain logic.
- Skill seeding, mount derivation — `gated` precomputes and passes
  the literal mount list.

## Wire shape

Unix socket at `$DATA_DIR/ipc/sand.sock`, owner-only access. JSON over
length-prefixed framing (same shape as `gated.sock`).

```
POST /v1/spawn
  → { backend, image, env, mounts, network_id, stdin, timeout, ... }
  ← { handle: "sand-abc123", pid_or_id, started_at }

POST /v1/exec/<handle>
  → exec one command in an existing sandbox (KVM only — for resident
    VMs; Docker backend errors with 405)
  ← { exit_code, stdout, stderr }

POST /v1/stop/<handle>
  ← { ok: true }

GET  /v1/state
  ← [{ handle, backend, folder_id, started_at, status }, …]

GET  /health
  ← { status: "ok"|"degraded", backend: "docker"|"crackbox" }
```

`gated` consumes via a `sandd.Client` Go package (parallel to
`crackbox.Client`). Today's `container.Runner` shrinks to ~10 lines:
build the spawn payload, POST to `sand.sock`, return the handle.

## Backend selection

One env var: `SAND_BACKEND=docker|crackbox`. Default `docker` for
back-compat. Per-folder override via `groups.sand_backend` (future,
not v1) once we want mixed-backend deployments.

Both backends use the same egress path: each spawn registers with egred
(via `egred.Client`) before the sandbox starts; unregisters on reap.
This is the same wire that today's `container/egress.go` does — moves
into `sandd` so the privilege boundary is clean.

## Privilege model

`sandd` runs as a dedicated user with the minimum privileges its
selected backend requires:

| Backend  | Required                                                  |
| -------- | --------------------------------------------------------- |
| docker   | member of `docker` group; mounts `/var/run/docker.sock`   |
| crackbox | root (or `kvm` group), `/dev/kvm` access, `CAP_NET_ADMIN` |

`gated` runs as a regular user, member of no privileged groups. Only
file it touches privileged-side is `sand.sock`, which is owner-only and
the owner is shared between `gated` and `sandd` users via group
membership.

## Footprint

| Aspect     | Number                                     |
| ---------- | ------------------------------------------ |
| Image size | ~25 MB (Go binary, both backends)          |
| Daemon RAM | ~10 MB + ~50 KB per active sandbox         |
| Code       | ~600 LOC (interface, two backends, socket) |
| Compose    | one new service per instance               |

## Migration from today

Today's `gateway/container/runner.go` is the source: `Run(in Input)`
that shells out to docker. Three steps:

1. **Extract** the docker-spawn logic into `sandd/backend/docker/`
   verbatim (same Go code, new home). Keep `Run`'s signature stable.
2. **Add the socket layer**: `sandd/cmd/sandd/main.go` listens on
   `sand.sock`, JSON-decodes spawn requests, calls the Docker backend.
3. **Switch `gated`**: replace direct `container.Run` calls with
   `sandd.Client.Spawn`. The Backend interface stays in `container/`
   for the unit tests to keep using.

The crackbox backend lands later as `sandd/backend/crackbox/`,
importing `crackbox/pkg/host/`.

## Out of scope for v1

- Crackbox backend (lands when [a-crackbox-sandboxing.md](a-crackbox-sandboxing.md) ships)
- Per-folder backend selection (single `SAND_BACKEND` env)
- Resident-VM pool management (lives in crackbox lib)
- Live-migrate or hot-attach to running sandboxes
- Multi-host / cluster mode

## Acceptance

- `gated` runs as a non-root user with no docker-socket access.
  `sandd` runs as the docker-group user; mount of `docker.sock` lives
  only in `sandd`'s compose service.
- `gated` calls `sandd.Client.Spawn` for every agent invocation.
  Docker backend produces identical container behavior to today.
- Compose generator emits `sandd` service alongside `gated`. Compose
  diff for v1: one new service, one new socket mount, one
  `SAND_BACKEND=docker` env.
- Reaping orphan-on-startup test: kill `sandd` mid-spawn, restart;
  the orphaned container is detected and reaped.

## Naming alternatives considered

`sandd` (chosen) — short, names the job (sandbox daemon).
Alternatives: `boxd` (clashes with mailbox), `runner` (overloaded),
`spawnd` (verb not noun), `nest` (cute but vague), `cell` (overloaded).
