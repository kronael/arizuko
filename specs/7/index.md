---
status: queued
---

# specs/7 — identity credentials and tools

Model credentials and tool accounts have separate ownership and authorization rules.
The core agent and habitat model lives in [phase 6](../6/54-agent-habitat.md).

Package import adapters in spec 25 and composition drafts 26 and 27 remain deferred to phase 8.

| Spec                                                               | Status  | Covers                                                                                                                                           |
| ------------------------------------------------------------------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| [25-publish-plan.md](25-publish-plan.md)                           | partial | Native packages, package import adapters, and skill discovery; defect L200                                                                       |
| [26-single-world-deployment.md](26-single-world-deployment.md)     | draft   | One core process with Dynamo-compatible storage and isolated agent execution                                                                     |
| [27-minimum-arizuko.md](27-minimum-arizuko.md)                     | draft   | Functional minimum: writable files, messaging, and skills                                                                                        |
| [28-identity-held-credentials.md](28-identity-held-credentials.md) | draft   | A user's credential pays for that user's turn; delivery, enrollment and internal funding await owner decisions                                   |
| [29-identity-held-tools.md](29-identity-held-tools.md)             | queued  | Tools carried by an OAuth identity and whose account a call acts as; the agent's own login stays the default and acting as a human stays a grant |
