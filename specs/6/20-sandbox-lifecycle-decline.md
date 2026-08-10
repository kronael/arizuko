---
status: draft
source: paradigmxyz/centaur@20f3021 (Apache-2.0) peel, 2026-08-10
---

# specs/6/20 — warm-pool sandbox lifecycle: decline

A costed decision NOT to adopt Centaur's sandbox lifecycle (warm pool,
desired/observed reconciliation, pause/resume, in-sandbox harness
server) against arizuko's per-turn ephemeral container. Their design is
good engineering for THEIR sandbox weight; arizuko's turn model makes
the whole plane unnecessary. Recorded so the next latency debate starts
from the structural reason, not a benchmark.

## What Centaur runs

A long-lived sandbox per conversation thread, k8s-only in practice, with
a full lifecycle plane. Paths into `paradigmxyz/centaur` at `20f3021`.

- **Portable state machine**:
  `Created|Running|Suspended|Stopped|Gone|Unknown` with one load-bearing
  gate — `can_open_io()` is true only for `Running`
  (`services/api-rs/crates/centaur-sandbox-core/src/lifecycle.rs:61-86`)
  — checked at every I/O attach and reattach site. Control plane holds a
  separate `DesiredSandboxState`; a small total function maps
  desired×observed → `Resume|Pause|Stop|ReportDrift`
  (`centaur-sandbox-manager/src/reconcile.rs:26-60`).
- **Warm pool with late-binding identity.** A replenisher keeps
  `target_size` (default 3) sandboxes pre-booted under a placeholder
  "bootstrap" egress-proxy principal; `claim()` pops one and rebinds the
  paired proxy to the real caller principal, blocking until the proxy
  confirms the swap so the first call can't fire under empty creds
  (`centaur-sandbox-manager/src/warm_pool.rs:14-19,64-118`;
  `iron_proxy.rs:726-784`; defaults
  `contrib/chart/values.yaml:379-384`). Sessions needing a non-default
  persona/toolset skip the pool and boot cold
  (`centaur-session-runtime/src/lib.rs:2576-2662`).
- **Idle → pause, aged → reap**: idle sandboxes are SIGSTOP'd/scaled
  down, a reaper force-stops anything older than `max_lifetime`
  (default 3 days) (`centaur-sandbox-manager/src/reaper.rs`).
- **In-sandbox harness**: `crates/harness-server` — not an HTTP server
  despite the name; a stdio NDJSON process (container `CMD
["harness-server","codex"]`, `services/sandbox/Dockerfile:301-303`)
  that wraps codex / Claude Code / amp as CLI subprocesses behind one
  protocol (`crates/harness-server/src/claude.rs:224-255` drives
  `claude --print --input-format stream-json --output-format
stream-json` with permissions bypassed — ALL enforcement lives
  outside the agent, in NetworkPolicy + proxy). Session state is
  in-memory; cross-restart continuity is delegated to each CLI's own
  resume.

Their own limits: the only backend with real isolation is k8s (the
local backend is a bare OS process, dev-only,
`centaur-sandbox-local/src/lib.rs:1-5`); the k8s interaction is
verified only by an all-`#[ignore]` e2e suite outside default CI; no
default CPU/memory limits anywhere.

## Why arizuko doesn't want this plane

The warm pool exists because their sandbox is HEAVY: a pod plus a
paired per-sandbox proxy pod plus two NetworkPolicies plus a
`tools-bootstrap` git-clone init container plus `uvx` installs. Booting
that per message would be absurd, so they pool it — and then need
claim/specialize, idle-pause, capacity eviction, a reaper, and drift
reporting to manage what pooling created. arizuko's turn container is
one `docker create` on a pre-built image with bind mounts
(`runed/docker.go:64-123`), torn down by `stop→kill→rm`
(`runed/docker.go:52-57`); the folder and DB carry all state across
turns (`5/A` §The primitives — containers are stateless and mount the
folder per turn), and mid-turn interaction reuses the LIVE container
via steer (`runed/docker.go:59-67`, `5/P`).

The structural blocker, beyond taste: **late-binding specialization
requires identity the runtime can rebind after boot.** Centaur's
sandbox identity is its proxy principal — a network-level fact they can
swap at claim time. An arizuko container's identity is its mount set
(the group home, the per-folder ipc socket) — fixed at `docker create`;
a warm generic container could never become a folder's container
without restructuring every mount into post-boot attachment. The pool
isn't just unnecessary here; it is unimplementable without giving up
the mount-boundary ownership model (`5/16` step 7).

Ephemerality is also the security story: a pooled sandbox lives days
(reaper default 3d) and accumulates whatever the agent wrote; a turn
container carries nothing across turns except what survives in the
folder, where it is diffable and owned.

## What survives the decline

- **`can_open_io` + desired/observed reconcile** are the right shapes
  for managing LONG-LIVED runtimes. arizuko has none today; if `6/9`
  (crackbox KVM sandboxing) ever wires into `runed` with VMs that
  outlive a turn, copy exactly these two: the one-boolean I/O gate and
  the total desired×observed plan function, both unit-testable against
  a fake backend.
- **harness-server answers `5`'s interop question, not a gap.**
  arizuko's equivalent exists: `ant/` is the in-container harness
  (`ant/src/index.ts` — prompt in, SDK query, `submit_turn` out) and
  `runed`'s `Runtime` is the host-side seam. Centaur is production
  evidence that wrapping foreign agent CLIs as stdio subprocesses works
  (the `6/1` wrap-what-you-run path) — and their in-memory
  `ThreadState` with resume delegated to each CLI confirms the thin
  wrapper needs no server. The recorded arizuko decision stands: the
  generic seam is the process boundary / MCP socket, not a
  TypeScript interface with N implementations (`5/K` deleted
  2026-08-06, −782 lines, `.diary/20260806.md:137-142`).
- **Enforcement outside the agent** — their permissions-bypassed CLI
  inside a NetworkPolicy+proxy cage is the same philosophy as
  container + crackbox + gated socket (`SECURITY.md` §Network egress
  isolation). Independent convergence, nothing to change.

## Decision

Decline the lifecycle plane. **Reopen trigger**: a runtime whose boot
cost is genuinely turn-dominant (KVM via `6/9`, or an image whose
seed/install phase grows past model latency) — then take the warm pool
WITH its consequences (claim seam, idle policy, reaper) as one unit,
and only for that runtime.

## Attribution

Analysis derives from reading `paradigmxyz/centaur` (Apache-2.0),
commit `20f3021`: `services/api-rs/crates/centaur-sandbox-{core,
manager,local,agent-k8s}/`, `crates/harness-server/`,
`services/sandbox/`, `contrib/chart/`. No code was copied.
