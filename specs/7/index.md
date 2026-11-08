---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world: consolidate processes where existing owners reuse their work and save measured memory.

Enterprise hardening remains in [phase 8](../8/index.md).

| Spec                                                           | Status  | Summary                                                                                                                                              |
| -------------------------------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| [26-single-world-deployment.md](26-single-world-deployment.md) | partial | vited and timed folded (built, tested); open: per-fold memory measurement, router-restart proof, 6/59.                                               |
| [57-group-handler.md](57-group-handler.md)                     | partial | `agent` setting and Bun ant built and tested; non-model handler proven in a real KVM guest (TestGuestTurnOverCtl); open: a real guest turn with ant. |
| [58-group-posts.md](58-group-posts.md)                         | shipped | Agents message each other with `send`; ACLs check the target and routing delivers.                                                                   |
| [60-pi-bridge.md](60-pi-bridge.md)                             | partial | Pi MCP tools and native providers implemented; image smoke, chassis integration, live acceptance and shared-login renewal remain deferred            |
