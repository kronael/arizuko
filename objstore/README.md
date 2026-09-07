# objstore

The object store arizuko's daemons address instead of per-daemon SQL. A second
engine is a second driver and no daemon changes (spec
[`6/37`](../specs/6/37-store-model.md), surface
[`6/40`](../specs/6/40-store-surface.md)).

**One daemon runs on this.** `authd/authstore` holds no SQL at all; the other
eight are still to move.

## Two primitives

```go
Query(ctx, Query) ([]Item, error)
GuardedBatch(ctx, []Check, []Write) (committed bool, err error)
```

That is the whole interface. The versioned replace, the reachability guard, the
claim-and-return and the counter are PROTOCOLS — package-level functions over
these two, written once and shared by every driver — never further methods some
engine cannot carry.

`committed == false` with a nil error is a REFUSED batch: a condition did not
hold. It is a normal outcome, the answer `authd`'s `ClaimRefreshToken` already
gives as `won=false`, and it must never arrive as an error.

## What gets on the surface

**A shape goes on the interface only when a PRODUCTION call site needs it and
the caller cannot build it out of what is already here.** Not a test, not a
conformance body, not a helper this package wrote for itself. The surface was
widened once and trimmed back the same day, and the rule is written down rather
than re-derived (spec `6/40` §"What gets on the surface").

What survived, and the caller that kept it:

| shape                                    | who asks for it                                                                                            |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `<`, `<=`, `>`, `>=` on the sort key     | the poll cursor, history, bot activity, the retry feed (`routd/db.go:315`, `reads.go:28`, `:1181`, `:406`) |
| `begins_with` on the sort key            | containment — every folder-scoped read                                                                     |
| `Write.Remove`                           | `store/onboarding.go:262`, a status set and five admission fields removed in one write                     |
| `Cond` on an item a batch does not touch | authd's family check, `refresh_tokens.go`                                                                  |
| `ItemAbsent` on a `Put`                  | create-if-absent, every conditional put authd makes                                                        |
| 100 actions, one per item                | DynamoDB's `TransactWriteItems` bound, so a caller cannot write a batch one engine alone runs              |

What did not: `BETWEEN` (no two-sided range anywhere in `store`, `routd`,
`onbod`, `dashd` or `resreg`), the exposed page (`After` / `More` /
`Examined`), the attributed refusal, and the conditional whole-item replace.
Each was consumed by exactly one thing — this package's own protocol helpers
and its own tests.

**The atomic counter is deliberately absent.** DynamoDB's in-place increment
needs a `Number` attribute (`SET Price = Price + :p`; `ADD` "supports only
number and set data types") and every value here is a string. So it is
`objstore.Increment`, a compare-and-swap protocol: read the value, write with
the read value as the condition, retry the lost race. That gives back the
`RETURNING` half for free, because the caller computed the new value.

## The shape of an item

An item is `(partition, sort key, attributes)`. The partition is the coordinate
a caller FILTERS on — for arizuko the tenant root — because a folder subtree has
to sit inside one partition for a prefix to cover it. The sort key carries the
rest: the path below the tenant, or a `core.TS` timestamp where the caller reads
in time order.

Every attribute value is a string, and an absent attribute is the only kind of
missing. That is one rule, and it buys two things: `Absent`/`Present` mean
exactly the same on every engine, and a comparison is a TEXT comparison in every
dialect with no cast anywhere.

**The rule is enforced, not trusted.** A value that must ORDER is fixed-width,
and a writer says so once with `OrderedSort` / `OrderedAttr`; every driver then
refuses a key, an attribute, a filter value or a cursor of another width, the way
`checkIdent` refuses a name it would write into a statement. Left to discipline
the rule is already broken in live data: `messages.timestamp` on krons carries
seven widths across 24,013 rows, and at the 32 pairs that share a second across
two of them byte order disagrees with time order — BUGS `L97`, read off the rows
rather than off the code.

## A read is the WHOLE selection

`Query` answers everything it selects, bounded only by the caller's own `Limit`,
which counts the items RETURNED. An engine that caps a single read — DynamoDB at
1 MB, measured before the filter — walks its own cursor to produce that. The cap
does not go away; it becomes the driver's problem, which is where it belongs,
and no caller carries a cursor it would never have used. `authstore` is the
proof: one partition for the entire daemon, so every non-point read is a
whole-partition scan, and it asks for all of it.

`Where` is still not an index. On an engine whose page cap applies before the
filter, a filter that keeps one row in ten thousand still reads ten thousand —
it just does so inside the driver. A selective predicate belongs on the sort
key, where it costs nothing.

## Layout

| path           | what it holds                                                     |
| -------------- | ----------------------------------------------------------------- |
| `objstore.go`  | `Store`, `Item`, `Key` — the object model                         |
| `query.go`     | the read shape: key predicate, filters, order, the caller's bound |
| `batch.go`     | the write shape, and the bounds every driver enforces             |
| `ordered.go`   | the declared widths every driver refuses a value against          |
| `protocol.go`  | `Increment` — what the two primitives compose into                |
| `sqlstore/`    | the SQL driver: one generator, two dialects                       |
| `conformance/` | the test bodies every driver runs                                 |

## The SQL driver is one driver with two emitters

`sqlstore` generates its statements, so SQLite and Postgres are a dialect switch
inside it rather than two implementations: placeholders, the row lock a check
takes, the document merge and removal, how one attribute is extracted, and the
DDL type of the document column. An entity is one table of three columns, so
schema is `CreateEntity` and nothing more — and it is deliberately OFF the
interface, because an engine with no DDL has nothing to implement.

A SQL engine has no page cap of its own, so this driver pages by not needing to:
one statement, every row. `Limit` is therefore a plain `LIMIT` and the filter is
a `WHERE` term the engine applies before counting — one renderer feeds both a
read's filter and a write's condition, so a fix to a comparison cannot land on
one path and miss the other.

Two dialect differences the design expected do not exist, measured on PostgreSQL
17.11 and the vendored SQLite 3.51.2: `substr(text,int,int)` is a Postgres
function too, so containment is byte-identical on both; and `INSERT … ON
CONFLICT` is SQLite syntax too, so `INSERT OR IGNORE` is never emitted.

## Running the conformance suite

```bash
go test ./objstore/...                                    # SQLite
ARIZUKO_TEST_PG_DSN=postgres://user:pw@host:5432/db \
  go test ./objstore/...                                  # Postgres
go test ./objstore/... -race -run TestConformance          # the concurrent bodies
```

One environment variable moves the whole suite between engines, which is the
mechanism `db_utils` already owns. `make test` does not pass `-race`; the
concurrency bodies are only proven under the flag above.

The suite is the deliverable. Every driver runs the identical bodies, so an
operation a driver cannot express does not go on the interface — the portability
discipline a vendor API would have imposed for free.

**A body earns its place by being able to FAIL**, watched, against a driver
broken on purpose: each key operator swapped for its neighbour, containment
widened to `LIKE`, the filter dropped, `Limit` and the sort direction dropped,
the read truncated so it stops answering the whole selection, the `REMOVE` half
dropped (in the generator and in the Postgres renderer), `ON CONFLICT DO
NOTHING` turned into an upsert, the counter's compare-and-swap dropped, its
companion writes dropped, and both width checks disabled. A mutation that breaks
the STATEMENT rather than the answer does not count — a body that goes red
because Postgres refused to parse the query has proved nothing.
