---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world. The core modules share one binary and one
process, with explicit boundaries for state, permissions, and execution.
A world can contain many groups and agents.

These specs describe the target composition. The current deployment uses
separate daemons. Enterprise hardening remains in [phase 8](../8/index.md).

| Spec                                                           | Status  | Covers                                                                               |
| -------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------ |
| [23-agent-habitat.md](23-agent-habitat.md)                     | draft   | Shared message, routing, permission, and turn model                                  |
| [25-publish-plan.md](25-publish-plan.md)                       | partial | Native packages, package import adapters, and skill discovery; defects L199 and L200 |
| [26-single-world-deployment.md](26-single-world-deployment.md) | draft   | One core process with Dynamo-compatible storage and isolated agent execution         |
| [27-minimum-arizuko.md](27-minimum-arizuko.md)                 | draft   | Functional minimum: writable files, messaging, and skills                            |
