---
status: draft
---

# specs/9 — data model

What arizuko's state _is_, tier by tier, and what the agent passively sees.

Phase 9 opened in 2026-05 as a three-action platform program: MCP+REST
unification, data-model sharpening, and git-as-truth. Two of the three have
since resolved elsewhere, and what remains is the data model itself.

| Spec                                                       | Status | Hook                                                                                                                                          |
| ---------------------------------------------------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------------------- |
| [2-data-model.md](2-data-model.md)                         | draft  | the cold / warm / hot tier model — the vocabulary `CLAUDE.md` and `specs/CLAUDE.md` lean on for "every cold-tier entity is a resreg resource" |
| [7-configurable-autocalls.md](7-configurable-autocalls.md) | draft  | operator-extensible `<autocalls>`: a DB-backed resreg resource layered over the four builtins, with two bounded kinds (`template`, `query`)   |

## Where the other two actions went

**MCP+REST unification** was pulled forward into active phase 5 —
[`5/17-openapi-mcp.md`](../5/17-openapi-mcp.md) is the mechanism,
[`5/16-mcp-rest-unification.md`](../5/16-mcp-rest-unification.md) the
rollout. It is shipped, not pending.

**Git as truth is rejected, not deferred.**
[`5/8-yaml-manifests.md`](../5/8-yaml-manifests.md) decided the opposite:
the SQLite DB is authoritative and YAML manifests are a transport
dump/import — `pg_dump`/`pg_restore` for the cold tier — with no DB→YAML
sync, no startup-apply, no SIGHUP reload. Committing an `export` dump to git
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
