---
status: draft
depends: [7-environment-contracts, ../5/16-daemon-split]
---

# specs/6/21 — the storage seam

The component contract (`6/7`) says a daemon must "own and migrate your
schema" and never says **through what**. Today the answer is 534 direct
`database/sql` call sites across 94 files and no `Store` interface
anywhere. That is the whole reason any backend question — cloud, NoSQL,
a hosted store — reads as a rewrite instead of an implementation.

This spec states the seam: the operation set a daemon may depend on, the
SQLite implementation behind it, and what conformance means. It does not
propose changing the database. It proposes making the database a choice.

## Why the design is already nearly non-relational

Measured, not assumed (2026-08-16, `main`):

| Property                 | Count                | Reading                                      |
| ------------------------ | -------------------- | -------------------------------------------- |
| foreign keys / 61 tables | 5                    | keyed documents, not a relational model      |
| real SQL joins           | 10                   | and 4 are trivial two-table lookups          |
| transactions             | 45, all single-table | local atomicity, never coordination          |
| 2PC / saga / dist. lock  | **0**                | cross-daemon consistency is idempotency keys |
| `LIKE`/glob predicates   | 174                  | policy tables, small, belong in memory       |

The last two rows carry the argument. Cross-daemon consistency is
already solved **above** the database — `chanlib.deliverOnce` is
claim → post → settle against one local table, and `X-Idempotency-Key`
collapses into `messages.id` on the way in (`6/7` §Delivery). Nothing
distributed needs emulating, so a backend must provide only a local
atomic write. That is what makes storage swappable, and it was decided
years before anyone asked the storage question.

## The operation set

A daemon may depend on exactly these. Anything else is a leak.

| Operation    | Shape                                    | Why it is in the set                      |
| ------------ | ---------------------------------------- | ----------------------------------------- |
| `Get`        | (partition, sort) → doc                  | the majority of the 534 sites             |
| `Put`        | doc → ()                                 | ditto                                     |
| `PutIf`      | conditional on absence or on a version   | `deliverOnce`'s claim; the migration lock |
| `Query`      | partition + sort range, ordered, limited | message history, audit reads              |
| `BatchWrite` | N docs, atomic within one partition      | the 45 transaction sites                  |
| `Delete`     | (partition, sort) → ()                   |                                           |

**The partition key is the folder.** Not a new key space — the folder is
already the isolation boundary for egress, ACL, secrets, web mounts and
memory. The seam admits the coordinate system the platform already has
(`5/33`).

`PutIf` is the one operation that must be explicit rather than emerging
from get-then-put: `deliverOnce`'s correctness is "claim only if absent",
and a read followed by a write is a different, racy statement.

### Deliberately excluded

- **No `LIKE`, no glob, no predicate push-down.** A seam that exposes
  them can only ever be implemented by SQL engines. Policy tables
  (grants, routes, config) are small and read constantly: load them into
  memory and match in Go, which is what a scan would do anyway, minus a
  round trip.
- **No joins.** The 4 trivial ones become two `Get`s. The 5 recursive-CTE
  ACL walks (`store/membership.go`, `resreg/resources/membership.go`,
  `routd/acl_resource.go`) become an iterative walk up a shallow tree —
  correct, but a round trip per level on the authorization hot path.
  Measure before assuming it is free.

## The two gaps this makes honest

A seam does not solve these. It converts them from hidden couplings into
declared ones, which is the point.

1. **Full-text search.** `messages_fts` is an FTS5 virtual table with
   `bm25()` ranking and `snippet()` highlighting (`store/messages.go:763`,
   `routd/reads.go:111`). SQLite implements `Search`; a key-value store
   implements it with nothing and needs a search engine beside it. This is
   a capability gap at any price, not a query to rewrite.
2. **rowid.** 38 sites use `AUTOINCREMENT` / `LastInsertId` / rowid, and
   the FTS join is `ON m.rowid = f.rowid`, so rowid is load-bearing for
   search specifically. A non-SQLite backend moves these to ULIDs —
   mechanical, but it touches all 38 and the FTS index with them.

## Ownership survives unchanged

The seam is a **Go interface, not a wire protocol**. Each daemon
constructs its own `Store` over its own file, so ownership stays what
`5/16` step 7 made it: a mount boundary a daemon physically cannot cross,
not a convention. In-process, no network hop, no new process.

A shared storage daemon on the network is therefore rejected: it degrades
ownership from a filesystem guarantee to an auth-token promise, adds a
single point of failure, and deletes SQLite's one real advantage. Only
libSQL's per-namespace-file mode preserves the boundary, and it buys
nothing a solo instance needs.

## Migrations become ensure-table

Most of the 86 migrations exist because SQL needs `ALTER TABLE` to add a
column. Behind a document seam a writer adds the field and readers
tolerate its absence, so those stop being migrations. What survives:

- **structural** — "ensure table X with this partition/sort key", declarative
  and idempotent;
- **backfill** — a job, not a schema change;
- **the migration lock** — `db_utils/db_utils.go`'s `BEGIN EXCLUSIVE`
  (which serialises two daemons racing the same migration) becomes a
  `PutIf` on a lock row.

**The cost is real and belongs in the record.** A migration is how you
_know_ every row has a field. Without one you buy permanent optionality:
every reader handles every historical shape forever. Schemaless does not
remove the work, it moves it from deploy time to read time. Adopt
ensure-table for structure; keep a written backfill for anything a reader
would otherwise have to guess about.

## Conformance: an oracle, not a claim

The seam's semantics must match a real DynamoDB API rather than our
assumptions about one. Two implementations exist and either serves as a
CI oracle:

- **ExtendDB** — AWS, Apache-2.0, May 2026. Implements the DynamoDB wire
  protocol with pluggable storage backends (Postgres first). v0.1; AWS
  says wait on production use.
- **Dynoxide** — `nubo-db/dynoxide`, MIT OR Apache-2.0. Clean-room Rust,
  DynamoDB engine over SQLite, runs in-process, as a 3 MB binary, or as
  WASM. Its authors scope it to local dev, testing and CI.

They are **test oracles, never infrastructure** — which is also why their
not-yet-production status does not matter here. This is `anteval`'s move
applied to storage: conformance is an executable, not a paragraph.

`DynamoDB Local` is rejected outright. AWS documents it as
development-only and it stores through **sqlite4java** — deploying it
locally buys SQLite plus a JVM plus a network hop.

## What this does not decide

Whether to ever run a non-SQLite backend. The seam's value is standalone:
it gives the component contract the missing half of "own your schema",
and it deletes the 534-call-site coupling that makes every storage
question unanswerable. A cloud backend becomes an implementation someone
can write the day traffic argues for one — and not before.

## Ties

`6/7` component contract (this is its missing half) · `5/16` per-daemon DB
ownership · `5/33` folder as the coordinate system · `5/9` conformance-as-
executable.
