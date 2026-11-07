---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world: the core modules share one binary and one process.

| Spec                                                           | Status | Summary                                                                                      |
| -------------------------------------------------------------- | ------ | -------------------------------------------------------------------------------------------- |
| [26-single-world-deployment.md](26-single-world-deployment.md) | draft  | One core process with Dynamo-compatible storage and isolated agent execution.                |
| [27-minimum-arizuko.md](27-minimum-arizuko.md)                 | draft  | Writable files, messaging and skills define the functional minimum.                          |
| [57-group-handler.md](57-group-handler.md)                     | draft  | The group `agent` setting selects an executable; ant runs under Bun.                         |
| [58-group-posts.md](58-group-posts.md)                         | draft  | Agents message each other with `send`; ACLs and routing replace delegate and escalate.       |
| [59-local-daemons.md](59-local-daemons.md)                     | draft  | A package declares one run command; daemons listen in /run/arizuko and need no registration. |
