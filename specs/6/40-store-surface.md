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

## The audited write: an intent log needs three more things

`runAudited` has 20 call sites across 10 files and is the largest single reason
this system opens transactions. Replacing it with three single-row writes —
pending audit row, data row carrying the audit id, then mark it committed — is
the right shape, because each write is atomic alone and that is what a
key-value engine gives. It does not work as stated, and a review found four
ways it fails.

**A delete leaves nothing to carry the back-reference.** `store.DeleteGroup`
is an audited delete across four tables; after it, no row exists to name the
audit id, so a sweep cannot tell a completed delete from an abandoned one. Any
audited delete has to leave a tombstone carrying the id, which makes every
audited delete a soft delete with reclamation behind it. That is the price, and
it is not small.

**The event is built lazily, so the pending row cannot hold it.**
`store/audit_helpers.go:10-28` says it outright: the `audit.Event` is
constructed inside the mutation so callers can include last-insert-id or
affected-rows. A pending row written first cannot carry fields that do not
exist yet, so the record is two shapes — an INTENT (actor, action, target)
written first, and an OUTCOME completed at step 3.

**A later writer erases the marker.** The data row holds only the newest
writer's audit id. If A writes, crashes before committing its audit, and B then
overwrites the row, the sweep sees A pending with no row naming it and deletes
it — though A's write happened and was superseded. The back-reference is a
liveness proof, not a history proof, and an audit log is a history.

**A sweep can race a live writer.** A sweep that observes no marker yet, delete
the pending audit, and let the writer then commit its data, produces an
unaudited write. The sweep needs a lease or an age bound longer than any write
can take, which reintroduces a timeout as a correctness parameter.

So the honest statement: the intent log removes the transaction for inserts and
updates, at the cost of tombstoned deletes, a two-shape audit row, and a
time-bounded sweep. Whether that trade is worth taking is a decision, and it is
`L241`.

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

## Portability, stated once

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
