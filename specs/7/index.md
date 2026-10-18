---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world. The core modules share one binary and one
process, with explicit boundaries for state, permissions, and execution.
A world can contain many groups and agents.

These specs describe the target composition. The current deployment uses
separate daemons. Enterprise hardening remains in [phase 8](../8/index.md).

| Spec                                                               | Status  | Covers                                                                                                                                           |
| ------------------------------------------------------------------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| [23-agent-habitat.md](23-agent-habitat.md)                         | draft   | Shared message, routing, permission, and turn model                                                                                              |
| [25-publish-plan.md](25-publish-plan.md)                           | partial | Native packages, package import adapters, and skill discovery; defect L200                                                                       |
| [26-single-world-deployment.md](26-single-world-deployment.md)     | draft   | One core process with Dynamo-compatible storage and isolated agent execution                                                                     |
| [27-minimum-arizuko.md](27-minimum-arizuko.md)                     | draft   | Functional minimum: writable files, messaging, and skills                                                                                        |
| [28-identity-held-credentials.md](28-identity-held-credentials.md) | queued  | A user's model subscription pays for the turn that user initiates; assembles 5/15, 5/14, 5/31, 5/32, 5/13                                        |
| [29-identity-held-tools.md](29-identity-held-tools.md)             | queued  | Tools carried by an OAuth identity and whose account a call acts as; the agent's own login stays the default and acting as a human stays a grant |
