---
status: draft
relates-to: [../7/57-group-handler, ../7/26-single-world-deployment]
---

# specs/8/60 — minimum arizuko

Arizuko is a tool to set up and host agents in the runtime. The minimum is
what a fresh instance with only the core packages must do. Each item cites the
spec that owns its mechanism.

## Acceptance

1. An agent writes a file in its workspace, and the operator reads that file
   over `/dav/` (`davd/`).
2. An agent sends to a granted group with `send`, and that group's handler
   runs (group `send` in `ipc/README.md`, [57-group-handler](../7/57-group-handler.md)).
   A send to an ungranted group fails.
3. An agent adds a skill in its workspace, and its next turn uses that skill
   (`ant/skills/`, group skills take precedence).
4. An operator installs an outside package through `arizuko package add`.
   Its declared artifacts use the existing package lifecycle in
   [EXTENDING.md](../../EXTENDING.md#installing-packages). A channel registers
   through the existing authenticated adapter contract. Socket discovery is
   outside this minimum; it needs a separately approved service contract.

## Boundaries

Each group keeps its own files, identity and grants. The minimum does not
permit shared credentials, another group's workspace, or agent code inside a
core process.
