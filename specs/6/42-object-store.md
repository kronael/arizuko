---
status: draft
depends: [40-store-surface, 38-storage-seam, 37-store-model]
---

# specs/6/42 — the object store: daemons hold no SQL

Decision (maintainer): **arizuko's storage is an object store behind an
interface.** A daemon addresses objects by key. SQL exists in exactly one
place — the interface's SQLite driver — and a second driver for another engine
changes no daemon.

This **reverses** [`6/38`](38-storage-seam.md)'s refusal ("arizuko moves to
Postgres and writes no `Store` interface"). That refusal costed the interface
against a Postgres target, where it buys nothing because Postgres is already
`database/sql`. Against an object-store target it is not an indirection over
the engine, it IS the engine's shape, and the daemons stop carrying SQL at all.

## Why this is cheap to start, and it is the reason to start now

The vendored driver is **SQLite 3.51.2 with `jsonb()`** — measured, not
assumed: `SELECT json_extract(jsonb('{"a":{"b":7}}'), '$.a.b')` returns `7` on
the engine running today.

So the object store does not need a new engine to exist. It is implemented over
the SQLite already deployed, proven against real daemons, and only then does a
DynamoDB driver become a second implementation of a working interface. Nothing
crosses a point of no return to begin, and no live data moves.

That inverts `6/37`'s order. Porting 30 migration files to Postgres was the
prerequisite for an engine arizuko may not want; this needs none of it.

## What each daemon holds

A daemon holds a storage package and nothing else touching persistence. Today
that rule has one clean example and two clear violations, measured over
non-test files:

| Package       | Files with SQL | Outside a storage file                                         |
| ------------- | -------------- | -------------------------------------------------------------- |
| `webd`        | 0              | 0 — already the target shape                                   |
| `store`       | 25             | 25, and they ARE the layer                                     |
| `routd`       | 16             | 12                                                             |
| `dashd`       | 13             | **13 — no storage layer at all; page handlers query directly** |
| `resreg`      | 4              | 4                                                              |
| `onbod`       | 3              | 3                                                              |
| `cmd/arizuko` | 3              | 3                                                              |
| `authd`       | 3              | 2                                                              |
| `runed`       | 2              | 1                                                              |
| `proxyd`      | 1              | 1                                                              |

`webd` already speaks only to `store`, which is the proof the shape works.
`dashd` is the far end: every page writes its own queries.

**A test enforces it**, the way `db_utils/opener_test.go` fails the build on a
stray `sql.Open`. SQL outside a storage package is a build failure, so the rule
cannot drift back.

## What the interface carries

[`6/40`](40-store-surface.md) derives the operations from invariants, and they
are what the interface must express — not a vendor's verb list:

- **containment** — a path prefix; `begins_with` on a sort key
- **the guarded batch** — a condition on one object plus a bounded set of writes
- **the versioned replace** — replace a scope's objects with a document
- **the reachability guard** — an unbounded graph read deciding a write
- **the claim-and-return** — a conditional update over a set that returns it

Three do not fit an object store as one operation, and `6/38` already costed
each: the versioned replace needs a per-scope version every writer bumps and
still fails past a batch limit; the reachability guard needs a materialised
ancestor closure maintained on every membership write; `resreg.Apply` needs a
lock object and a sequence of bounded batches, which gives up the pre-image
rollback it carries today (`L44`).

**That price is the decision, and it is now taken.** The SQLite driver
implements all five natively, so nothing is lost while it is the only driver;
the three redesigns are owed by whoever writes the second one.

## The migration apparatus

Seven owners and 156 versioned DDL files are a relational discipline. An object
store has no DDL past creating a table and its keys, so under a second driver
that apparatus does not port — it is replaced by the interface's own schema
knowledge, and the reviewability that versioned files provide has to come from
somewhere else. This spec does not answer that. It is the largest open question
behind the second driver, and `L228` and `L195` both feed it.

## Order

1. The storage-layer rule and its guard: SQL only inside a daemon's storage
   package, enforced by a test. `dashd` first, since it has no layer at all.
2. The interface, derived from `6/40`'s five operations, with the SQLite driver
   behind it — SQL moves, it does not change.
3. `L195` settled: `store/`'s 88 migration files are a second declaration of
   routd's schema that nothing runs, and they must not be carried into this.
4. A second driver, and with it the three redesigns and the DDL question.

Steps 1 to 3 change no engine and no data, and revert with `git revert`. Step 4
is the point of no return.

## Ties

`6/40` (the operations) · `6/38` (the refusal this reverses, and the costed
redesigns it measured) · `6/37` (whose Postgres order this displaces) ·
`sqlite_jsonb_document_store` · BUGS `L195`, `L228`.
