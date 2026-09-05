---
status: queued
---

# specs/7 — the cloud track

Putting arizuko in the cloud comes AFTER splitting it into parts, which is
phase 6's job: you cannot deploy a part you have not separated. That order is
the whole reason this is its own phase rather than work inside 6.

The part is a **package** — a process plus a declaration of how it hooks
into the system — and every component is one (`25`, settled 2026-09-05).
The four specs before it each answer one question a part raises on its
way to the cloud: what it stores (`21`), what language it may be (`22`),
what it is a participant in (`23`), how the agent inside it finds its
skills (`24`).

[`25-publish-plan.md`](25-publish-plan.md) is the package contract: the
outsider test, what a manifest can and cannot declare (measured), and
every component judged against it. One package passes today (`litellm`);
the nine adapters fail on two name maps in `compose.go`.

[`24-skill-search.md`](24-skill-search.md) asks what a skill-discovery index costs and what a miss would look like.

[`23-agent-habitat.md`](23-agent-habitat.md) records the reframing — three verbs, the two-standard asymmetry, the package as the participant shape, and what the outside world already calls it.

[`22-rust-track.md`](22-rust-track.md) answers what, if anything, moves to Rust: the isolation boundary only, never on the cloud path — and a package may be Rust because the contract is a process.

[`21-storage-seam.md`](21-storage-seam.md) is the storage decision: move
to Postgres, no `Store` interface. Its census finds 26 of 47 transactions
multi-table, each a privilege-escalation guard; the cloud target is managed
Postgres, reached by a connection string. A DynamoDB-shaped layer is the
other branch, and it costs three invariants a redesign first — group delete,
membership, `resreg.Apply` — which the spec measures so the choice is a
decision, not a discovery.

## Specs

| Spec                                       | Status   | Covers                                                                                                                                                                                                                   |
| ------------------------------------------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [21-storage-seam.md](21-storage-seam.md)   | shipped  | decision: Postgres, no `Store` interface; 26 of 47 transactions are multi-table; the cloud target is managed Postgres (a DSN); the DynamoDB branch is costed — three security invariants need a redesign before any seam |
| [22-rust-track.md](22-rust-track.md)       | draft    | Rust only at the isolation boundary and never on the cloud path; the package contract makes a package's language invisible; `egress/match` is core's, so the Rust unit in `egress` is the proxy                          |
| [23-agent-habitat.md](23-agent-habitat.md) | draft    | the three verbs, the ingress/egress asymmetry, the package as the participant shape, the field by name and by property                                                                                                   |
| [24-skill-search.md](24-skill-search.md)   | draft    | skill discovery has no miss signal; the harness listing budget is window-dependent; a miss counter before any retrieval                                                                                                  |
| [25-publish-plan.md](25-publish-plan.md)   | defected | the package contract and the outsider test; a manifest declares ten resources through `install` and one through `add`; five hooks are names in Go (BUGS `L199`, `L200`); every component measured against it             |

## Ties

`6/16` (the ship-in-parts matrix — what a part must satisfy to ship alone) ·
`5/28` (the package manager `25` holds to its contract) · `5/34` (the wire
contract every adapter package satisfies) · `6/28` (the cockpit anchor, for
what the operator surface expects of a store) · `5/A` (positioning) · the
relational-invariant finding in `21` itself.
