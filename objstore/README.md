# objstore

The object store arizuko's daemons address instead of per-daemon SQL. A second
engine is a second driver and no daemon changes (spec
[`6/37`](../specs/6/37-store-model.md), surface
[`6/40`](../specs/6/40-store-surface.md)).

**Nothing runs on this yet.** The interface, one driver and the conformance
suite exist; moving `authd` onto them is the next step, and no daemon imports
this package today.

## Two primitives

```go
Query(ctx, Query) ([]Item, error)
GuardedBatch(ctx, []Check, []Write) (committed bool, err error)
```

That is the whole interface. The versioned replace, the reachability guard and
the claim-and-return are PROTOCOLS — package-level functions over these two,
written once and shared by every driver — never further methods some engine
cannot carry.

`committed=false` with a nil error is a REFUSED batch: a condition did not hold.
It is a normal outcome, the answer `authd`'s `ClaimRefreshToken` already gives
as `won=false`, and it must never arrive as an error.

## The shape of an item

An item is `(partition, sort key, attributes)`. The partition is the coordinate
a caller FILTERS on — for arizuko the tenant root — because a folder subtree has
to sit inside one partition for a prefix to cover it. The sort key carries the
rest: the path below the tenant, or a `core.TS` timestamp where the caller reads
in time order.

Every attribute value is a string, and an absent attribute is the only kind of
missing. That is one rule, and it buys two things: `Absent`/`Present` mean
exactly the same on every engine, and a comparison is a TEXT comparison in every
dialect with no cast anywhere. A value that must ORDER is written fixed-width by
its writer — the discipline `core.TS` already imposes on time.

## Layout

| path           | what it holds                                                    |
| -------------- | ---------------------------------------------------------------- |
| `objstore.go`  | `Store`, `Item`, `Key` — the object model                        |
| `query.go`     | the read shape: key predicate, filters, order, limit, projection |
| `batch.go`     | the write shape, and the bounds every driver enforces            |
| `sqlstore/`    | the SQL driver: one generator, two dialects                      |
| `conformance/` | the test bodies every driver runs                                |

## The SQL driver is one driver with two emitters

`sqlstore` generates its statements, so SQLite and Postgres are a dialect switch
inside it rather than two implementations: placeholders, the row lock a check
takes, the document merge, and the DDL type of the document column. An entity is
one table of three columns, so schema is `CreateEntity` and nothing more — and
it is deliberately OFF the interface, because an engine with no DDL has nothing
to implement.

Two dialect differences the design expected do not exist, measured on PostgreSQL
17.11 and the vendored SQLite 3.51.2: `substr(text,int,int)` is a Postgres
function too, so containment is byte-identical on both; and `INSERT … ON
CONFLICT` is SQLite syntax too, so `INSERT OR IGNORE` is never emitted.

## Running the conformance suite

```bash
go test ./objstore/...                                   # SQLite
ARIZUKO_TEST_PG_DSN=postgres://user:pw@host:5432/db \
  go test ./objstore/...                                 # Postgres
go test ./objstore/... -race -run TestConformance         # the concurrent bodies
```

One environment variable moves the whole suite between engines, which is the
mechanism `db_utils` already owns. `make test` does not pass `-race`; the
concurrency bodies are only proven under the flag above.

The suite is the deliverable. Every driver runs the identical bodies, so an
operation a driver cannot express does not go on the interface — the portability
discipline a vendor API would have imposed for free.

Watched to fail, on both engines:

- drop the condition from the guarded write → `OneClaimWins…` reports 8 of 8
  winners and `ClaimAndInsert…` 6 of 6.
- widen containment to `LIKE` → `PrefixDoesNotWiden` leaks `aXb`, `aZZb` and, on
  SQLite only, the case-differing `A_B`. Postgres `LIKE` is case-sensitive, so
  only the `_`/`%` half of BUGS `L252` is engine-independent.
