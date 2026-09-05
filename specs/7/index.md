---
status: queued
---

# specs/7 — the cloud track

Putting arizuko in the cloud comes AFTER splitting it into parts, which is
phase 6's job: you cannot deploy a part you have not separated. That order is
the whole reason this is its own phase rather than work inside 6.

[`25-publish-plan.md`](25-publish-plan.md) names the four components whose import graph already lets them ship, and the two that cannot.

[`24-skill-search.md`](24-skill-search.md) asks what a skill-discovery index costs and what a miss would look like.

[`23-agent-habitat.md`](23-agent-habitat.md) records the reframing — three verbs, the two-standard asymmetry and what the outside world already calls it.

[`22-rust-track.md`](22-rust-track.md) answers what, if anything, moves to Rust: the isolation boundary only, and never on the cloud path.

[`21-storage-seam.md`](21-storage-seam.md) is the storage decision: move
to Postgres, no `Store` interface. Its census finds 26 of 47 transactions
multi-table, each a privilege-escalation guard; the cloud target is managed
Postgres, reached by a connection string. A DynamoDB-shaped layer is the
other branch, and it costs three invariants a redesign first — group delete,
membership, `resreg.Apply` — which the spec measures so the choice is a
decision, not a discovery.

## Specs

| Spec                                     | Status  | Covers                                                                                                                                                                                                                   |
| ---------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [21-storage-seam.md](21-storage-seam.md) | shipped | decision: Postgres, no `Store` interface; 26 of 47 transactions are multi-table; the cloud target is managed Postgres (a DSN); the DynamoDB branch is costed — three security invariants need a redesign before any seam |

## The cockpit left on 2026-08-27

This phase used to carry two tracks that shared a number and nothing else: the
operator cockpit and the cloud. The cockpit is platform work — it observes and
controls the parts — so it moved to phase 6 as `6/28`–`6/33`, and the digits
now say what the work order already said: **6 splits arizuko into parts, 7 puts
the parts in the cloud.**

Those six numbers are burnt: this phase numbers new work from `22` up, never
back into them.

## Ties

`6/16` (the ship-in-parts matrix — what a part must satisfy to ship alone) ·
`6/28` (the cockpit anchor, for what the operator surface expects of a store) ·
`5/A` (positioning) · the relational-invariant finding in `21` itself.
