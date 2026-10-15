---
status: draft
relates-to:
  [
    23-agent-habitat,
    25-publish-plan,
    26-single-world-deployment,
    ../5/33-paths-roles,
  ]
---

# specs/7/27 — minimum arizuko

The minimum defines what an agent receives. It does not prescribe how many
processes supply those capabilities.

## Functional floor

1. A writable workspace, reachable as files by the agent and the operator.
2. Messaging that supports replies, delegation, and escalation within granted scope.
3. Skills that the agent can discover and extend inside its workspace.

`davd/` exposes workspace files through WebDAV. `ipc/` supplies messaging
and delegation tools. `ant/skills/` supplies bundled skills, while a group's
own skills take precedence. These are existing capabilities.

## Boundaries

One operator can own many agents and groups. Each group keeps its own files,
identity, and grants. A single owner still needs authorization for public
endpoints and isolation for untrusted agent execution.

The minimum is a functional acceptance boundary, not permission to bypass
those controls. A package cannot satisfy it by sharing credentials or exposing
another group's workspace.

## Relationship to the other specs

[The habitat](23-agent-habitat.md) defines the shared conversation and turn model.
[Packages](25-publish-plan.md) install capabilities and expose skills.
[Single-world deployment](26-single-world-deployment.md) composes the core
modules into one process. This spec defines the capabilities that composition
must preserve.
