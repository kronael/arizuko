---
status: draft
relates-to:
  [
    ../6/59-local-daemons,
    57-group-handler,
    58-group-posts,
    26-single-world-deployment,
  ]
---

# specs/7/27 — minimum arizuko

Arizuko is a tool to set up and host agents in the runtime. The minimum is
what a fresh instance with only the core packages must do. Each item cites the
spec that owns its mechanism.

## Acceptance

1. An agent writes a file in its workspace, and the operator reads that file
   over `/dav/` (`davd/`).
2. An agent sends to a granted group with `send`, and that group's handler
   runs ([58-group-posts](58-group-posts.md), [57-group-handler](57-group-handler.md)).
   A send to an ungranted group fails.
3. An agent adds a skill in its workspace, and its next turn uses that skill
   (`ant/skills/`, group skills take precedence).
4. An operator adds a package by its `run` command, and routd finds it by its
   socket ([6/59-local-daemons](../6/59-local-daemons.md)).

## Boundaries

Each group keeps its own files, identity and grants. The minimum does not
permit shared credentials, another group's workspace, or agent code inside a
core process.
