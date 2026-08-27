---
status: queued
---

# specs/7 — the cloud track

Putting arizuko in the cloud comes AFTER splitting it into parts, which is
phase 6's job: you cannot deploy a part you have not separated. That order is
the whole reason this is its own phase rather than work inside 6.

[`21-storage-seam.md`](21-storage-seam.md) is the one spec here — the `Store`
operation set derived from DynamoDB and pruned to what arizuko actually needs.
Its finding is that the seam cannot be a key-value interface: ~27 of 43
transactions are multi-table and each prevents a specific privilege escalation,
so transactions stay in the seam while aggregates and FTS stay out of it.

## Specs

| Spec                                     | Status | Covers                                                                                                                                                                                   |
| ---------------------------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [21-storage-seam.md](21-storage-seam.md) | draft  | the `Store` operation set derived from DynamoDB and pruned; transactions kept (the invariants are multi-table); aggregates and FTS explicitly OUT of the layer; per-table partition keys |

## The cockpit left on 2026-08-27

This phase used to carry two tracks that shared a number and nothing else: the
operator cockpit and the cloud. The cockpit is platform work — it observes and
controls the parts — so it moved to phase 6 as `6/28`–`6/33`, and the digits
now say what the work order already said: **6 splits arizuko into parts, 7 puts
the parts in the cloud.**

The moves are recorded in [`../RETIRED.md`](../RETIRED.md), so a reference to
`7/1` still resolves. Those six numbers are burnt: this phase numbers new work
from `22` up, never back into them.

## Ties

`6/16` (the ship-in-parts matrix — what a part must satisfy to ship alone) ·
`6/28` (the cockpit anchor, for what the operator surface expects of a store) ·
`5/A` (positioning) · the relational-invariant finding in `21` itself.
