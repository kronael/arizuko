---
status: draft
depends:
  [../6/7-environment-contracts, ../5/16-daemon-split, ../5/33-paths-roles]
---

# specs/7/21 — the storage seam

The component contract (`6/7`) says a daemon must "own and migrate your
schema" and never says **through what**. Today the answer is ~614 direct
`database/sql` call sites across 107 files and no `Store` interface
anywhere. That is why any backend question reads as a rewrite rather
than an implementation.

This spec states the seam: the operation set, what is deliberately NOT in
it, and how the invariants that currently live in SQL survive the move.

## The first draft was wrong, and how

Recorded because the error is instructive, not for penance. The draft
argued arizuko was "already nearly non-relational" from two numbers: 5
foreign keys across 61 tables, and "45 transactions, all single-table".

The second number was produced by reading the first statement after each
`BeginTx`. Reading the **bodies** gives roughly 27 of 43 as multi-table,
and they are precisely the security invariants:

| Site                        | Tables                       | What a partial write costs                             |
| --------------------------- | ---------------------------- | ------------------------------------------------------ |
| `dashd/groups_admin.go:431` | groups, acl, routes          | a re-created folder inherits the last tenant's admin   |
| `store/invites.go:270`      | invites, acl                 | grant lands, usage count doesn't → infinite redemption |
| `onbod/main.go:923`         | user_profiles, groups, acl   | TOCTOU: admin on someone else's world                  |
| `store/pairing.go:105`      | route_tokens, acl_membership | a paired identity with two parents                     |
| `routd/db.go:305`           | messages, idempotency_keys   | a permanently `in_flight` ledger row                   |

`resreg/resreg.go:616` wraps **every** mutating resreg action with
`audit.EmitInTx`, so multi-table is architecturally mandated, not
incidental.

The lesson generalises: a low foreign-key count is not evidence of a
document design. These invariants live in **transactions**, not in
constraints, so a schema audit that reads only DDL cannot see them.

## The operation set

Derived by starting from DynamoDB's data plane and deleting what arizuko
does not need — rather than inventing a minimal set and then blaming
DynamoDB for its limits, which is what the first draft did.

**Kept:**

| Operation  | Shape                                                              |
| ---------- | ------------------------------------------------------------------ |
| `Get`      | (partition, sort) → doc                                            |
| `Query`    | partition + sort-key range, ordered, limited                       |
| `Put`      | whole doc, optional condition                                      |
| `Update`   | set/remove named fields, optional condition                        |
| `Delete`   | (partition, sort), optional condition                              |
| `Transact` | N writes across tables AND partitions, per-item conditions, atomic |

Every mutation takes an optional **condition over the current document's
values** — not merely over its existence — and may return the old or new
document. That is `ConditionExpression` + `ReturnValues`, and it is what
the ~20 compare-and-swap state machines in the codebase need
(`routd/pending_actions.go:303`'s approval gate is `WHERE id=? AND
status=?`; `chanlib/dedup.go`'s release is `DELETE … WHERE
state='inflight'`).

**Deleted from DynamoDB's set:** `Scan` (a table whose partition key is
right never needs it), `BatchWriteItem` (not atomic, no conditions —
`Transact` supersedes it), `BatchGetItem`, `TransactGetItems`.

`Put` and `Update` both stay although `Put` is nearly `Update` with a
full field map: "replace the whole document" and "set these fields" have
different concurrency semantics under a condition, and collapsing them
would force every partial write through read-modify-write — the race
this seam exists to prevent.

## Transactions are kept, not replaced by ordering

`Transact` maps to one SQLite transaction and to DynamoDB
`TransactWriteItems` (cross-table, cross-partition, conditional, ≤100
items). The constraint the first draft designed around does not exist.

**Write-ordering is not a substitute**, and the reasoning is recorded so
it is not relitigated. Ordering a sequence to "fail toward less
permission" genuinely works for invite redemption and pairing. It fails
as a general rule for three reasons:

1. **Not every table in a transaction is a permission.** The group-delete
   transaction includes `routes`, and `router.go:269`
   `IsAuthorizedRoutingTarget` is pure path arithmetic that never reads
   `acl`. Deleting grants first does not close ingress — the surviving
   route still delivers into the folder.
2. **The repair path is privileged and unattended.** Ordering needs a
   sweeper to finish half-done work, running with operator authority,
   unable to distinguish "crashed mid-delete" from "legitimate group with
   no grants yet".
3. **It is unenforceable.** No test fails when someone reorders two
   lines. A transaction is machine-checked; an ordering convention is a
   comment. Root `CLAUDE.md` requires the fix that makes a bad state
   impossible-by-construction, and ordering makes it merely unlikely.

Where ordering IS free, prefer it — it costs nothing and narrows the
window. Never rely on it alone.

## What is NOT in this layer

**Aggregates.** No `COUNT`, `SUM`, `GROUP BY`, no ordering by an
aggregate. dashd's usage pages
(`dashd/usage_page.go:170,193,218`), `store/inspect.go:20` and
`store/messages.go:286` need them today. They do not belong in a storage
interface: a store that aggregates is a query engine, and only SQL
engines could implement it.

They move up, into **materialised counters maintained on write** —
incremented inside the same `Transact` as the write that changes them, so
the counter cannot drift from the rows. This pays on write instead of on
read, which is the correct trade for values read on every dashboard load
and changed once per message. Reading a counter is a `Get`.

Deriving an aggregate by reading every row through `Query` is forbidden
for unbounded tables (`messages`, `audit_log`) — it turns a page render
into a table scan.

**Ordered cross-partition scans.** `routd/db.go:325`'s poll feed and the
scheduler's due-task sweep (`store/tasks.go:136`) read across every
partition ordered by time. Each needs a redesign — a dedicated
time-ordered partition, or a queue — before it can sit behind this
interface. Named here so neither is discovered late.

**Full-text search.** See Gaps.

## Partition keys are per-table

The first draft said "the partition key is the folder". It is not: 15 of
routd's 29 tables have no `folder` column. Each table declares its own,
and the ten key spaces already in use are the schema stating its access
pattern, not a defect.

| Table              | Partition                 | Sort         |
| ------------------ | ------------------------- | ------------ |
| `messages`         | `chat_jid`                | `timestamp`  |
| `acl`              | world (see below)         | the row's PK |
| `secrets`          | `(scope_kind, scope_id)`  | key          |
| `idempotency_keys` | `endpoint`                | `key`        |
| `audit_log`        | `folder`, `""` when unset | `id`         |
| `outbound_dedup`   | `idempotency_key`         | —            |

**`acl` partitions by world, not folder.** Its `scope` column holds
globs, so no folder owns a row. `container/runner.go:58` `worldOf`
already computes a world as the top path segment and already returns
`""` for an elevated turn that spans all worlds. The rule:

- scope's first segment is a literal (`acme/**`) → partition `acme`
- scope starts with a wildcard (`**`, `*/eng`) → partition `""`

An authorization read for folder F queries exactly two partitions —
`worldOf(F)` and `""` — never a scan. `5/5` R2 already settled that a
world is a folder-shape predicate rather than a table, so this introduces
no new entity.

**No authorization state is cached.** `auth.Authorize` reads
`policyRows()` live on every call, which is why a revocation takes effect
on the next request. An in-memory copy was proposed and rejected: acl is
39 rows on a live instance, so a cache saves a trivial scan, and any
freshness check costs the same read it was meant to avoid. Anything that
must remain true is read, not cached.

## The document model

Rows become JSON documents, and this is a real change to how the code
treats data — not a re-encoding. A table is
`(partition, sort, doc, version)`; `doc` is opaque to the store.

Consequences to accept deliberately:

- **Field access moves into Go.** No `json_extract` in a predicate
  (`store/groups.go:332` does this today), no `datetime()` normalisation
  in a comparison (`store/tasks.go:139` relies on it to order non-UTC
  timestamps correctly). Values must be stored already normalised,
  because the store will not normalise them at read time.
- **`ON DELETE CASCADE` disappears.** All 5 foreign keys use it, and
  `store/groups.go:89` deletes a group with no explicit child cleanup
  because the database does it. Each cascade becomes an explicit
  `Transact` member.
- **Triggers disappear.** See Gaps.
- **`rowid` ordering disappears.** `audit/query.go:97` pages on the
  `AUTOINCREMENT` id specifically because "id order IS time order, and it
  is the one that admits an exact cursor". A client-generated ULID trades
  a database-assigned total order for one subject to clock skew between
  writers; the audit cursor must be re-derived, not merely re-typed.

## Gaps — capabilities with no answer in this layer

1. **Full-text search.** `messages_fts` is an FTS5 virtual table, and it
   is maintained by **database triggers** (`messages_fts_ai/au/ad`,
   `routd/migrations/0001`). Behind a `Store`, `Put` silently stops
   indexing unless every writer performs a second write — converting a
   schema-enforced invariant into per-call-site discipline, which is the
   drift this project bans. Search must become an explicit index write
   inside the same `Transact`, or move to a search component.
2. **Uniqueness over a predicate.** `authd/migrations/0002` is
   `UNIQUE(active) WHERE active = 1`, and it is what stops two concurrent
   rotations from splitting the trust root into two signers. No store has
   this primitive; it becomes a conditional write against a dedicated
   singleton document.
3. **Whole-subsystem compare-and-swap.** `resreg/engine.go:742`
   recomputes a checksum over every row of every resource in a subsystem
   and swaps the lot atomically. It needs serialisable read-of-everything
   plus write-of-everything. `Transact`'s 100-item limit does not reach
   it at scale.

## Migration

One cutover per instance, not an incremental dual path: 49 live tables
across six owner databases become documents in a single release. The
deliverable that makes it possible is the **key map** — for each table,
its partition and sort key — of which the table above is the start.

Old tables are retained for exactly one release. Nothing reads them; they
are the rollback, so a failed cutover restores from a table still present
rather than from a backup.

The 145 SQL migrations across seven embedded sets do NOT vanish. 96 of
them do real work beyond `ALTER TABLE` — 32 rewrite data, and some must:
`0077-invites-hash-at-rest` replaces plaintext invite tokens with hashes,
where the point is that the old shape stops existing. "Readers tolerate
the field's absence" covers additive backfills and nothing else.

## Conformance

The seam's semantics must match a real DynamoDB API rather than our
assumptions. **ExtendDB** (AWS, Apache-2.0, DynamoDB wire protocol over
pluggable backends) and **Dynoxide** (`nubo-db/dynoxide`, MIT/Apache-2.0,
DynamoDB over SQLite, in-process) each serve as a CI oracle. They are
test oracles, never infrastructure, which is also why their
not-for-production status is irrelevant here. `DynamoDB Local` is
rejected: AWS documents it development-only and it stores through
sqlite4java.

`store/seam/seamtest` holds a table-driven suite any implementation is
run against — the `5/9` move applied to storage: conformance is an
executable, not a paragraph.

## Ties

`6/7` component contract (this is its missing half) · `5/16` per-daemon DB
ownership · `5/33` grants and scope globs · `5/5` R2 (a world is a
predicate, not a table) · `5/9` conformance-as-executable.
