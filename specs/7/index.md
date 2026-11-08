---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world: consolidate processes where existing owners reuse their work and save measured memory.

| Spec                                                           | Status  | Summary                                                                                                |
| -------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------ |
| [26-single-world-deployment.md](26-single-world-deployment.md) | partial | vited and timed folded (built, tested); open: per-fold memory measurement, router-restart proof, 6/59. |
| [57-group-handler.md](57-group-handler.md)                     | partial | `agent` setting and Bun ant built and tested; open: real guest turn with ant and a fake handler.       |
| [58-group-posts.md](58-group-posts.md)                         | shipped | Agents message each other with `send`; ACLs check the target and routing delivers.                     |
