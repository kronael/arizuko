---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world: consolidate processes where existing owners reuse their work and save measured memory.

Enterprise hardening remains in [phase 8](../8/index.md).

| Spec                                                           | Status  | Summary                                                                                                                                                                                  |
| -------------------------------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [26-single-world-deployment.md](26-single-world-deployment.md) | partial | vited and timed folded; router restart tested; open: per-fold memory, check 5; onbod/dashd wait (L688, L689).                                                                            |
| [57-group-handler.md](57-group-handler.md)                     | partial | `agent` setting, Bun ant and terminal handler failure (exit 78) built and tested; non-model handler proven in a real KVM guest (TestGuestTurnOverCtl); open: a real guest turn with ant. |
| [58-group-posts.md](58-group-posts.md)                         | shipped | Agents message each other with `send`; ACLs check the target and routing delivers.                                                                                                       |
| [60-pi-bridge.md](60-pi-bridge.md)                             | partial | Pi MCP tools and native providers implemented; image smoke, chassis integration, live acceptance and shared-login renewal remain deferred                                                |
| [61-smaller-orthogonal-core.md](61-smaller-orthogonal-core.md) | draft   | Measured cuts for ant environment integration, optional OAuth brokerage, and core component ownership.                                                                                   |
