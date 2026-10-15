---
status: draft
relates-to: [25-publish-plan, 26-single-world-deployment, 27-minimum-arizuko]
---

# specs/7/23 — the agent habitat

Arizuko gives agents a shared environment for files, messages, permissions,
and execution. The environment's boundaries do not require separate processes.

## Shared model

Adapters normalize external events into messages. Routing selects a group and
topic. Grants determine what the resulting turn can read, change, or send.
A folder identifies a workspace; its path does not confer authority.

The turn is temporary. Workspace files, conversation state, grants, and audit
records have their own lifetimes. Execution isolation remains explicit even
when the core modules share a process.

Inbound messages share one representation. Outbound actions remain individually
permissioned tools. An adapter translates platform addresses and receipts;
it does not implement a second conversation workflow.

## Delivery

A turn produces one final reply. An explicit reply suppresses delivery of the
same final prose. When the agent sends no explicit reply, the router can deliver
its final prose through the normal reply path. Progress does not count as a
final reply. Delivery failures must reach the caller.

The router owns delivery state. Each adapter owns its platform transport.
This separation applies to threads, reactions, edits, and ordinary messages.

## Composition

[The minimum](27-minimum-arizuko.md) defines the functional floor.
[Packages](25-publish-plan.md) supply capabilities and skills through one contract.
[Single-world deployment](26-single-world-deployment.md) composes the core
modules in one process. A package does not need its own process.

## Code boundaries

- `routd/` owns routing and turn delivery.
- `runed/` owns execution and workspace mounts.
- `resreg/` supplies one resource handler for MCP and REST.
- `auth/` owns permission evaluation; adapters retain transport credentials.

These ownership boundaries remain meaningful inside one binary.
