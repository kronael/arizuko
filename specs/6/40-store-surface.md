---
status: shipped
shipped: 2026-09-06
depends: [37-store-model, 38-storage-seam, ../5/33-paths-roles]
---

# specs/6/40 — the store surface: three operations

`6/37` names a middle tier between the opener and each daemon's own SQL, and
derives its candidates from repetition — an operation is admitted when the same
SQL appears in more than one package. That test admits the easy repeats and
excludes every operation that carries an invariant, because each of those
appears exactly once. `6/38` measured which transactions a key-value engine
cannot carry: group delete, the membership walk, and `resreg.Apply`. None of
them is in `6/37`'s list.

This spec derives the surface from invariants instead. **Three operations.**

## 1. Containment — is this coordinate inside that one

Every scoped read asks it, and `groupfolder.SQLSubtree` is the one answer, at
16 call sites:

```go
"(" + col + " = ? OR substr(" + col + ", 1, ?) = ?)"
```

A subtree is a **string prefix on the path column**, and nothing else — no
closure table, no adjacency list, no nested set. The separator is `/` between
folders and `#` before a topic, which is why one helper scopes
`routes.target`, where a topic hangs off a folder.

That representation is what makes the folder portable. A prefix on a path is
`begins_with` on a sort key, native to a key-value engine — but only if the
partition key sits ABOVE the folder, at the tenant root, so one query covers
the subtree. Choose the folder itself as the partition key and the same
predicate becomes a full scan. The prefix is also why the predicate must be
`substr`, not `LIKE`: `_` and `%` are wildcards, and `LIKE` is
case-insensitive, so a folder named `a_b` would otherwise match `aXb`
(BUGS `L122`).

## 2. The guarded transition — read, decide, write under a condition

Two sites today: invite redemption conditions its update on `used_count` and
`active` (`store/invites.go`), and a hold resolution conditions on `status`
(`routd/steer.go`). Both read outside any transaction and make the decision a
condition on the row the decision came from, so neither needs to hold a lock
across the read.

That is a conditional write, which every engine has. It stays on the surface
because it is the shape that keeps the other 19 multi-table transactions
`6/38` classified from needing an interactive one.

## 3. The versioned replace — make a scope's rows equal a document

`resreg.Apply` deletes a resource's rows by scope and inserts the manifest's,
per table, then recomputes a checksum. Group delete runs the same per-table
deletes over a folder subtree and inserts nothing. **They are one operation
with different arguments**: replace the rows matching a scope predicate with a
document, and version the result.

- Group delete is `replace(subtree(folder), ∅)`.
- `Apply` is `replace(exact(subsystem), manifest)`.

The two halves each spec worried about separately are the same half. The
generation that stops a recreated folder inheriting the last tenant's admin,
and the checksum that makes a package install transactional, are both "version
the result".

**The collapse is a proposal, and the code does not do it today.**
`store.DeleteGroup` runs four table-specific predicates and carries no version;
`resreg.Apply` scopes on an exact resource value and compare-and-swaps a
subsystem-wide checksum (`resreg/engine.go:301-316`, `:786-879`). Making them
one operation is work, not an observation, and `L198` is the open half of it.

This is the operation a key-value engine cannot carry as one write. The scope
is unbounded, so the row set must be read before it can be deleted, and a row
added in between survives. That is the price `6/38` costed, and it is now one
price rather than two.

## The audited write commits in a GuardedBatch, not an intent log

`GuardedBatch` is a bounded all-or-nothing write, so **a mutation and its audit
row go in one call and the guarantee `runAudited` gives today survives
unchanged**: no write without its audit, no audit without its write. `6/38`
already measured that this fits — the 24 `runAudited` callers are in its
19-site "fits as a bounded conditional batch" group precisely because none of
them reads inside the transaction.

**Central and per-owner become the same design.** Four SQLite files force
audit to be per-owner: a transaction cannot span them, so each daemon must keep
its own. A document store has no such boundary — `TransactWriteItems` carries
up to 100 actions ACROSS TABLES in one account and region, and the isolation
the four files gave is now a partition key. So one audit table partitioned by
owner is the same thing as four audit tables, and a mutation in routd's table
can commit with an audit row in the shared one, in a single `GuardedBatch`.

That answers the question the audit research had to reject on today's
substrate. A central audit store written synchronously is only a distributed
transaction while the stores are separate; once there is one backend it is one
transaction, and centralisation costs nothing. The write stays atomic AND the
record is already in one place — no shipper, no outbox, no reconciliation.

What remains after the move is durability off the box, which is a different
requirement: retention, tamper evidence and an export the operator can hold.
The archive answers that, and it answers it for one table instead of four.

So the intent log is NOT the replacement for the audited write. It is needed
only where the write set is UNBOUNDED and therefore cannot be one batch: the
versioned replace over a subtree, and `resreg.Apply` over a subsystem. Those
two already have protocols of their own, and the marker they need is the same
mechanism, so it is one thing serving two callers rather than a scheme imposed
on forty.

**One audit row shape, two behaviours: `pending` then `done`.** The state
lives ON THE AUDIT ROW, not as a back-reference from the data row, and that
single choice is what makes it work.

- **Bounded write.** The batch carries the mutation and the audit row together.
  Whether the row is born `done`, or born `pending` and marked `done` in the
  same batch, the intermediate state is never observable — the transaction
  makes it invisible. Nothing changes for the 24 `runAudited` callers.
- **Unbounded write** — the subtree purge and `resreg.Apply`. The writes span
  several batches, so `pending` is real and observable, and it is exactly the
  marker those two protocols need. The audit row IS the in-progress marker;
  there is no second mechanism.

Putting the state on the audit row rather than on the data row removes three
failures a back-reference design has. A delete leaves nothing behind to carry a
back-reference, and here it does not need to. A later writer overwrites a data
row's marker, and here there is no marker on the data row to overwrite. The
`audit.Event` is built lazily inside the mutation
(`store/audit_helpers.go:10`) so a pending row cannot hold its outcome fields —
and here it is not asked to: `pending` records the intent, `done` records the
outcome, which is what those two states are for.

What remains is the fourth: a sweep must tell an abandoned `pending` from one
still in flight. That is a deadline on the audit row itself, so the judgement is
made from the row and needs no lookup into the data. It applies to two
protocols, not to the forty ordinary writes.

## What this surface is not

It is not a storage abstraction, and it presents no vendor's shape. Each
operation is named in arizuko's terms and each has a SQL implementation today.
Tier 3 — a query one daemon issues — stays raw SQL against the pool, and a
passthrough over `database/sql` remains the indirection `6/37` rejects.

## Three is not enough — two more the census missed

A review against the code found two invariants no operation above expresses.

**The reachability guard.** `store.AddMembership` walks the ancestor closure
with `WITH RECURSIVE up(p)` and refuses the edge if the child already appears
in it — a graph reachability test, decided by an unbounded read, under the same
lock as the insert. A path prefix cannot express it: membership is a DAG over
folders, not a tree, so a parent need not be a prefix of its child. This is the
operation `6/38` costed as needing a materialised ancestor closure maintained
on every membership write.

The same walk is on the READ path: `auth.Authorize` resolves a principal's
grants through `store.Ancestors` (`auth/authorize.go:134`, `:308`), and
`routd/sibling_db.go:312` walks it again to ask whether a caller is an
operator. Measured on all three live instances it costs nothing — 58 edges on
krons pointing at exactly two parents, `role:operator` and `role:member`, and
no parent is ever itself a child, so the recursion never recurses. The one
apparent DAG is an identity ALIAS (`telegram:user/… → google:…`) sharing a
table with role assignment, not a deep hierarchy.

**Left as it is, deliberately.** Measured on all three live instances it costs
nothing: 58 edges on krons pointing at exactly two parents, `role:operator` and
`role:member`, and no parent is ever itself a child, so the recursion never
recurses. The one apparent DAG is an identity ALIAS
(`telegram:user/… → google:…`) sharing a table with role assignment.

Resolving at token-mint time and revoking from the recorded issued set would
take the walk off the authorize path. It is an OPTIMISATION — the walk still
runs at mint, the revocation scan still runs, and this operation stays in the
census either way — so it is recorded in
[`15/h`](../15/h-authorization-cache.md) with its measurements and its reopen
trigger, not scheduled here.

**The claim-and-return.** `store.DueTasks` is
`UPDATE scheduled_tasks SET status='firing' WHERE status='active' AND next_run
<= ? RETURNING …` — a conditional update over an unbounded set that returns
what it claimed, which is a work-queue lease. A key-value engine has neither
the predicate update nor the return, so it becomes query-then-conditional-write
per item, and the claim stops being atomic across the set.

Two corrections to operation 2 as well: invite redemption and `/always`
condition ONE row and atomically write OTHERS, so the operation is a guarded
BATCH — a condition check plus a bounded set of writes — not a single-row
conditional write. That shape is native (`TransactWriteItems` allows a
`ConditionCheck` on an item it does not modify), but the bound is 100 actions.

## The seam is TWO primitives, and the rest are protocols

A design review against the code cut the five to two. Everything below stands
as the analysis that produced them; this is the interface.

```
Query(shape)                 // shape carries an exact or prefix predicate
GuardedBatch(checks, writes) // conditions on items it may not modify, plus writes
```

Each driver implements those two and nothing else. The other three are
PROTOCOLS — package-level functions written once, over the two primitives,
shared by every driver:

- **Containment is not an operation, it is a predicate.**
  `groupfolder.SQLSubtree` returns `WHERE` text that callers embed inside other
  reads and mutations (`groupfolder/folder.go:183`, `routd/reads.go:109`,
  `store/folder_scope.go:33`). So `Query` takes an exact-or-prefix predicate
  argument; containment never needed a method of its own.
- **The versioned replace** is `Query`, a start-marker CAS, N guarded writes,
  then a finish CAS. It cannot be one batch because `resreg.Apply` processes an
  unbounded row set (`resreg/engine.go:819`, `:834`). It also has exactly ONE
  domain consumer — `Apply` itself; `store.DeleteGroup` is an unversioned
  subtree purge (`store/groups.go:113`). One caller is a function, not an
  interface member.
- **The reachability guard** is a graph `Query` plus a version-guarded batch.
- **The claim-and-return** is `Query` plus a per-row guarded batch, which is
  the form `routd/pending_actions.go:421` already uses.

**The constraint this exposes, and it is not negotiable.** Exact set-wide
atomicity — "these N rows change together or none do", for unbounded N —
cannot have an identical DynamoDB driver. The interface therefore defines
per-item claims and marker-gated replace, and every caller is written to those
semantics on BOTH drivers. A Postgres driver that quietly offers stronger
guarantees would let callers depend on something the other driver cannot honour,
which is the drift the conformance suite exists to catch.

**Five shapes the two primitives do not yet cover**, each measured: plural
condition checks (`store/pairing.go:114`), unbounded bulk mutation
(`store/secrets.go:372`), computed post-images (`store/routes.go:58`),
generated ids, and result-dependent audit writes — `runAudited` builds its
`audit.Event` after the mutation so it can carry affected-rows
(`store/audit_helpers.go:10`). They are normalised by client-supplied ids,
precomputed audit data, paging, and CAS batches. Schema migration stays
driver-local.

## Portability, stated once

**First, what this table is not.** arizuko is NOT key-value ready, and `6/38`
decided not to be: "arizuko moves to Postgres and writes no `Store` interface".
The seam is `db_utils.Open`, which returns a `*sql.DB` — a SQL handle. No
key-value engine can sit behind that type, so the question is never "does the
seam carry DynamoDB", it is "what would a rewrite cost". This table is that
estimate.

Two things make the estimate larger than the table alone suggests. The
migration apparatus — seven owners, 156 versioned DDL files, keyed
`(service, version)` — has no counterpart in a key-value engine, which has no
DDL past creating a table and its keys. It would be deleted, not ported, and
with it goes the mechanism that makes a schema change reviewable. And the four
owner databases are four consistency boundaries chosen because a transaction
cannot span them; a key-value engine has one boundary per item, so every
cross-table invariant in the census below becomes application code.

| Operation          | Key-value engine                                                                     |
| ------------------ | ------------------------------------------------------------------------------------ |
| containment        | native: `begins_with` on a sort key under a tenant partition                         |
| guarded batch      | native: a `ConditionCheck` plus writes, bounded at 100 actions                       |
| versioned replace  | NOT native: unbounded scope, read-then-delete, and the read is not part of the write |
| reachability guard | NOT native: an unbounded graph read decides the write                                |
| claim-and-return   | NOT native: no predicate update, no return over a set                                |
| audited write      | native only with tombstoned deletes, a two-shape row, and a time-bounded sweep       |

Three of the five block a key-value move, not one. The versioned replace is
also the operation needing `pg_advisory_xact_lock` on Postgres, and that
coincidence stands: `replace` is not a storage call, it is a protocol over a
lock and a sequence of bounded writes, and it should be named as one.

The first draft of this spec claimed three operations and that the audited
write folded in cleanly. Both were wrong, and the code said so — the sections
above record what the review found rather than the shape that was tidier.

## Ties

`6/37` (this replaces its tier-2 candidate list) · `6/38` (the transaction
census these three are derived from) · `5/33` (the path is the coordinate) ·
BUGS `L122` (why the prefix is `substr`), `L44` (the pre-image rollback the
versioned replace carries), `L198` (the group delete with two halves).
