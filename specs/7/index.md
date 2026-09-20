---
status: queued
---

# specs/7 — the cloud track

Putting arizuko in the cloud comes AFTER splitting it into parts, which is
phase 6's job: you cannot deploy a part you have not separated. That order is
the whole reason this is its own phase rather than work inside 6.

The part is a **package** — a process plus a declaration of how it hooks
into the system — and every component is one (`25`, settled 2026-09-05).
The three specs before it each answer one question a part raises on its
way to the cloud: what language it may be (`22`), what it is a participant
in (`23`), how the agent inside it finds its skills (`24`). What a part
stores is settled before it ships alone, so the store model is
[`6/37`](../6/37-store-model.md).

[`25-publish-plan.md`](25-publish-plan.md) is the package contract: the
outsider test, what a manifest can and cannot declare (measured), and
every component judged against it. One package passes today (`litellm`);
the nine adapters fail on two name maps in `compose.go`.

[`24-skill-search.md`](24-skill-search.md) asks what a skill-discovery index costs and what a miss would look like.

[`23-agent-habitat.md`](23-agent-habitat.md) records the reframing — three verbs, the two-standard asymmetry, the package as the participant shape, and what the outside world already calls it.

[`22-rust-track.md`](22-rust-track.md) answers what, if anything, moves to Rust: the isolation boundary only, never on the cloud path — and a package may be Rust because the contract is a process.

## Specs

| Spec                                       | Status   | Covers                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ------------------------------------------ | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [22-rust-track.md](22-rust-track.md)       | draft    | Rust only at the isolation boundary and never on the cloud path; the package contract makes a package's language invisible; `egress/match` is core's, so the Rust unit in `egress` is the proxy                                                                                                                                                                                                                                   |
| [23-agent-habitat.md](23-agent-habitat.md) | draft    | the three verbs, the ingress/egress asymmetry, the package as the participant shape, the field by name and by property                                                                                                                                                                                                                                                                                                            |
| [24-skill-search.md](24-skill-search.md)   | draft    | skill discovery has no miss signal; the harness listing budget is window-dependent; a miss counter before any retrieval                                                                                                                                                                                                                                                                                                           |
| [25-publish-plan.md](25-publish-plan.md)   | defected | the package contract and the outsider test; a manifest declares ten resources through `install` and one through `add`; five hooks are names in Go (BUGS `L199`, `L200`); every component measured against it                                                                                                                                                                                                                      |
| [26-lambda-target.md](26-lambda-target.md) | draft    | **what arizuko must stop assuming about its host**: the store move already answered persistence, so what blocks a serverless deployment is `docker run` and the `SO_PEERCRED` turn credential; three shapes, and the chosen one makes the SUBSTRATE the seam, so `6/41`'s microVM-per-turn is the prerequisite and Lambda MicroVMs (8 h, not 15 min) are one operator of that contract; six of nine daemons need nothing designed |

## Ties

`6/16` (the ship-in-parts matrix — what a part must satisfy to ship alone) ·
`5/28` (the package manager `25` holds to its contract) · `5/34` (the wire
contract every adapter package satisfies) · `6/28` (the cockpit anchor, for
what the operator surface expects of a store) · `5/A` (positioning) · the
relational-invariant finding in `21` itself.
