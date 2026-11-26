---
status: draft
---

# specs/9 — data model and later features

What arizuko's state is, tier by tier, and which unsigned features wait for later approval.

Phase 9 opened in 2026-05 as a three-action platform program: MCP+REST
unification, data-model sharpening, and git-as-truth. Two of the three have
since resolved elsewhere. Persistent machines, identity-held accounts and
local service discovery also wait here for explicit contract approval.

| Spec                                                               | Status  | Hook                                                                                                                                                                                                                                          |
| ------------------------------------------------------------------ | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [2-data-model.md](2-data-model.md)                                 | draft   | the cold / warm / hot tier model — the vocabulary `CLAUDE.md` and `specs/CLAUDE.md` lean on for "every cold-tier entity is a resreg resource"                                                                                                 |
| [7-configurable-autocalls.md](7-configurable-autocalls.md)         | draft   | operator-extensible `<autocalls>`: a DB-backed resreg resource layered over the four builtins, with two bounded kinds (`template`, `query`)                                                                                                   |
| [20-group-machine-size.md](20-group-machine-size.md)               | draft   | each group sizes its own guest with `mem`, `smp` and `timeout` keys in its `container_config`, tmpfs derived from `mem`, the host check per turn against `MemAvailable` (`L513`); build when a measured group needs more than 2 GB or 2 vCPUs |
| [21-state-transitions.md](21-state-transitions.md)                 | partial | Every work-producing write schedules its next transition; due keys replace polling scans.                                                                                                                                                     |
| [54-agent-machine.md](54-agent-machine.md)                         | draft   | Persistent root and optional background processes; 8–15 engineering days estimated; storage, reset and power contracts need approval.                                                                                                         |
| [55-identity-held-credentials.md](55-identity-held-credentials.md) | draft   | Canonical personal payer and model enrollment; 12–20 engineering days estimated; billing, token exposure and internal funding need approval.                                                                                                  |
| [56-identity-held-tools.md](56-identity-held-tools.md)             | draft   | Explicit human-account selection for tools; 5–8 engineering days estimated after the identity join; consent, audit and revocation need approval.                                                                                              |
| [59-local-daemons.md](59-local-daemons.md)                         | draft   | Shared package declarations and socket discovery; 15–25 engineering days estimated; bootstrap, store enrollment and remote authorization need approval.                                                                                       |

## Where the other two actions went

**MCP+REST unification** was pulled forward into active phase 5 —
`resreg/README.md` is the mechanism,
`resreg/README.md` the
rollout. It is shipped, not pending.

**Git as truth is rejected, not deferred.**
`resreg/README.md` decided the opposite:
the SQLite DB is authoritative and YAML manifests are a transport
dump/import — `pg_dump`/`pg_restore` for the cold tier — with no DB→YAML
sync, no startup-apply, no SIGHUP reload. Committing a `dump` to git
is fine; a continuously-synced git working tree is not the model.

## Deleted 2026-08-02

| was                                     | why                                                                                                                                                                                        |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `3-git-as-truth.md`                     | the rejected approach above, and built on "the gateway is the only git writer" — `gated`/gateway was deleted at v0.50.0. No git-writing code was ever written.                             |
| `4-data-ingestion-curation-eventing.md` | an open-questions doc premised on git-as-truth shipping, which self-disclaimed as not a vehicle for future work.                                                                           |
| `6-functions.md`                        | 574 lines on a `functions` primitive spawned by a new `fnspd` host daemon bridging **containerized `gated`** to systemd-user. `gated` is gone and `fnspd` never existed outside this file. |

If the functions primitive revives, it needs a fresh spec against
`routd`/`runed`, not a revert — the execution model it assumed no longer
exists.
