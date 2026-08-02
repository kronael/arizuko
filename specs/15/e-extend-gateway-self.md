---
status: draft
---

# Agent self-modification of the platform

The root agent runs full Claude Code but can only **read** platform source;
every change to the daemons needs a human `make build && make image`. This
spec is the direction for closing that loop, not a design — the scope choice
below is unmade.

## Strawmen

- **Plugin directory** — `plugins/{actions,handlers,channels}/` loaded at
  startup, mounted read-write. Upstream updates never touch it. Narrow
  blast radius; only reaches what the plugin interface exposes.
- **Staging area** — the agent writes proposals to a staging tree the
  operator reviews and applies (`diff → approve → apply → rebuild`). This is
  self-modification without live-patching risk: nothing the agent writes is
  running until a human says so.
- **Agent branch + CI** — read-write mount of the full repo, agent commits to
  `agent/<instance>`, CI tests and builds, human reviews the PR. Widest
  reach, and the only one where the review artifact is a normal PR.

The staging and branch strawmen are the same idea at two review granularities
(a local diff vs a PR); the plugin directory is a different, narrower bet.

## What has to be decided before this is a spec

- **Scope** — plugin surface only, or the whole repo.
- **Testing** — a staging instance, hot reload, or unit tests in CI.
- **Rollback** — immutable images and a re-pin, or a revert commit.
- **Who** — root agent alone, or root agent plus explicit operator approval;
  and whether approval is per-change or a standing grant.
- **Conflict resolution** against upstream when the agent's tree diverges.

Until scope is picked, the rest cannot be answered — each strawman implies a
different rollback and a different review artifact.
