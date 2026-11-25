---
status: queued
---

# specs/7 — single-world deployment

One operator runs one world: consolidate processes where existing owners reuse their work and save measured memory.

Enterprise hardening remains in [phase 8](../8/index.md).

| Spec                                                           | Status  | Summary                                                                                                                                                                                                                                           |
| -------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [26-single-world-deployment.md](26-single-world-deployment.md) | partial | vited and timed folded; checks 1 to 3 and router restart tested; open (live): per-fold memory, check 5, the same-run serve-and-fire proof; onbod/dashd stay separate (L688, L689).                                                                |
| [57-group-handler.md](57-group-handler.md)                     | partial | `agent` setting, Bun ant and terminal handler failure (exit 78) built; the static handler and the missing handler have tests; the gateway refuses the model call, routd reconciles caps before the mint; open (live): a real guest turn with ant. |
| [60-pi-bridge.md](60-pi-bridge.md)                             | partial | Pi MCP tools and native providers implemented; the borrowed Codex login is written back to the host; ten smoke cases and the unit tests cover the bridge outside the image; open: the fresh-image gate and the live turn through routd (live)     |
| [61-smaller-orthogonal-core.md](61-smaller-orthogonal-core.md) | draft   | Measured cuts for ant environment integration, optional OAuth brokerage, and core component ownership.                                                                                                                                            |
