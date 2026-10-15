---
status: draft
depends: [../6/42-dynamo-over-postgres, ../6/43-shared-store-server]
relates-to:
  [
    23-agent-habitat,
    25-publish-plan,
    27-minimum-arizuko,
    ../6/41-qemu-substrate,
  ]
---

# specs/7/26 — single-world deployment

A single operator needs a deployment with few moving parts. The core modules
run in one binary and one process. Their ownership boundaries remain explicit.
One world can contain many agents, groups, channels, and concurrent turns.

## Composition

The binary starts and stops its core modules together. Modules keep their
resource ownership and call explicit interfaces. External MCP and REST clients
reach the same handlers and authorization checks.

Internal calls do not require loopback HTTP, per-module containers, or service
credentials merely to preserve a code boundary. An external package can retain
its own process when its runtime or isolation requires one.

Agent execution keeps its isolation boundary. Combining core modules does not
place untrusted agent code inside the core process or widen workspace access.
The execution contract remains [6/41-qemu-substrate](../6/41-qemu-substrate.md).

## Storage

Persistence uses the Dynamo-compatible API. A deployment selects its storage
endpoint independently of core composition. The PostgreSQL-backed service and
a managed Dynamo-compatible endpoint answer to the same storage contract.
Provider compatibility requires tests against the selected endpoint.

The object-store port remains `Query` plus `GuardedBatch`. Table ownership,
conditional writes, and transaction constraints remain explicit. Combining
modules does not authorize one module to bypass another module's resource API.

This composition fits a long-running host or cloud instance with the required
execution substrate. It does not require a separate function deployment for
each module. A Dynamo-compatible database alone does not prove that the whole
application can run inside a function runtime.

## Acceptance boundaries

A minimal deployment supplies [27-minimum-arizuko](27-minimum-arizuko.md).
Package installation uses [25-publish-plan](25-publish-plan.md).
Existing permission, delivery, execution, and workspace isolation checks apply
to the combined process. Shutdown must stop modules and their owned work.

The current installation uses separate daemons. This spec defines a target
composition, not an available installation mode.
