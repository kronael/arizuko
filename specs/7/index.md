---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world: consolidate processes where existing owners reuse their work and save measured memory.

| Spec                                                           | Status | Summary                                                                                                 |
| -------------------------------------------------------------- | ------ | ------------------------------------------------------------------------------------------------------- |
| [26-single-world-deployment.md](26-single-world-deployment.md) | draft  | proxyd serves files without reload; routd owns the task ticker; further folds require measured savings. |
| [57-group-handler.md](57-group-handler.md)                     | draft  | The group `agent` setting selects an executable; ant runs under Bun.                                    |
| [58-group-posts.md](58-group-posts.md)                         | draft  | Agents message each other with `send`; ACLs and routing replace delegate and escalate.                  |
