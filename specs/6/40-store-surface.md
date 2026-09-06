---
status: draft
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

This is the operation a key-value engine cannot carry as one write. The scope
is unbounded, so the row set must be read before it can be deleted, and a row
added in between survives. That is the price `6/38` costed, and it is now one
price rather than two.

## The audited write is an intent log, not a transaction

`runAudited` has 20 call sites across 10 files and is the largest single reason
this system opens transactions. It does not need one:

1. Write the audit row **pending**.
2. Write the data row, carrying the audit row's id.
3. Mark the audit row **committed**.

Three single-row writes, each atomic on its own, which is exactly what a
key-value engine gives.

**The back-reference in step 2 is load-bearing.** Without it, a crash between
steps 2 and 3 leaves a completed write whose audit row is still pending, and a
sweep that deletes pending rows destroys the audit for a write that happened —
the one outcome the audit exists to prevent. A sweep cannot distinguish "this
failed" from "this succeeded and we died before the stamp" unless the data row
says so.

With the back-reference the sweep is a reconciliation, decidable from the rows
alone: a pending audit whose data row exists is committed; a pending audit with
no data row is deleted. The cost is one column on every audited table, and the
gain is that the same marker serves `resreg.Apply`'s in-progress state, so the
system has one mechanism where `6/37` redesign 3 assumed two.

## What this surface is not

It is not a storage abstraction, and it presents no vendor's shape. Each
operation is named in arizuko's terms and each has a SQL implementation today.
Tier 3 — a query one daemon issues — stays raw SQL against the pool, and a
passthrough over `database/sql` remains the indirection `6/37` rejects.

## Portability, stated once

| Operation          | Key-value engine                                                                     |
| ------------------ | ------------------------------------------------------------------------------------ |
| containment        | native: `begins_with` on a sort key under a tenant partition                         |
| guarded transition | native: a conditional write                                                          |
| versioned replace  | NOT native: unbounded scope, read-then-delete, and the read is not part of the write |
| audited write      | native once it is an intent log with the back-reference                              |

One operation of the three blocks a key-value move, and the same operation is
the one needing `pg_advisory_xact_lock` on Postgres. That coincidence is the
finding: `replace` is not a storage call, it is a protocol over a lock and a
sequence of bounded writes, and it should be named as one.

## Ties

`6/37` (this replaces its tier-2 candidate list) · `6/38` (the transaction
census these three are derived from) · `5/33` (the path is the coordinate) ·
BUGS `L122` (why the prefix is `substr`), `L44` (the pre-image rollback the
versioned replace carries), `L198` (the group delete with two halves).
