# objstore

The object store arizuko's daemons address instead of per-daemon SQL. A second
engine is a second driver and no daemon changes (spec
[`6/37`](../specs/6/37-store-model.md), surface
[`6/40`](../specs/6/40-store-surface.md)).

**One daemon runs on this.** `authd/authstore` holds no SQL at all; the other
eight are still to move.

## Two primitives

```go
Query(ctx, Query) (Page, error)
GuardedBatch(ctx, []Check, []Write) (Outcome, error)
```

That is the whole interface. The versioned replace, the reachability guard, the
claim-and-return, the paged walk and the counter are PROTOCOLS — package-level
functions over these two, written once and shared by every driver — never
further methods some engine cannot carry.

`Outcome.Committed == false` with a nil error is a REFUSED batch: a condition
did not hold. It is a normal outcome, the answer `authd`'s `ClaimRefreshToken`
already gives as `won=false`, and it must never arrive as an error.

## Narrow is not the same as narrower than the weakest engine

Every shape here is one DynamoDB documents, and the AWS page is cited beside it
in the source. A shape the weakest engine HAS and this interface lacks is not
portability — it is a hole nine daemons work around.

| shape                                                               | where DynamoDB has it                                           |
| ------------------------------------------------------------------- | --------------------------------------------------------------- |
| `=`, `<`, `<=`, `>`, `>=`, `BETWEEN`, `begins_with` on the sort key | key condition expression                                        |
| `Page.More` / `Query.After`                                         | `LastEvaluatedKey` / `ExclusiveStartKey`                        |
| the page bound BEFORE the filter                                    | the 1 MB cap, "applies before any FilterExpression"             |
| `Limit` counts what is EXAMINED                                     | the page cap "to the items it reads before applying the filter" |
| `Write.Remove`                                                      | `REMOVE` in an update expression                                |
| a `Cond` on a `Put`                                                 | the transactional `Put`'s condition expression                  |
| `Outcome.Refused`                                                   | `CancellationReasons` + `ReturnValuesOnConditionCheckFailure`   |
| 100 actions, one per item                                           | `TransactWriteItems`                                            |

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

## A read is a PAGE

`Query` returns what fits in `MaxPageBytes` (1 MB, DynamoDB's) plus a cursor,
and the bound is measured on the items the read EXAMINED, before `Where` narrows
them. Three consequences a SQL driver would not produce on its own:

- a page can return ZERO items and still have more to come;
- `Limit` bounds what is examined, so a filtered read with `Limit: 3` that
  matches nothing in the first three returns nothing and a cursor;
- `Where` is not an index — a filter that keeps one row in ten thousand still
  walks ten thousand. A selective predicate belongs on the sort key.

`objstore.QueryAll` walks the pages when a caller genuinely wants the whole
selection. `authstore` uses it for its whole-partition scans, which is what
`authstore.Partition = "authd"` — one partition for the entire daemon — makes
every non-point read.

## Layout

| path           | what it holds                                                     |
| -------------- | ----------------------------------------------------------------- |
| `objstore.go`  | `Store`, `Item`, `Key`, `Page`, `Outcome` — the object model      |
| `query.go`     | the read shape: key predicate, filters, order, page bound, cursor |
| `batch.go`     | the write shape, and the bounds every driver enforces             |
| `ordered.go`   | the declared widths every driver refuses a value against          |
| `protocol.go`  | `QueryAll` and `Increment` — what the two primitives compose into |
| `sqlstore/`    | the SQL driver: one generator, two dialects                       |
| `conformance/` | the test bodies every driver runs                                 |

## The SQL driver is one driver with two emitters

`sqlstore` generates its statements, so SQLite and Postgres are a dialect switch
inside it rather than two implementations: placeholders, the row lock a check
takes, the document merge and removal, the stored document's byte size, how a
boolean renders, and the DDL type of the document column. An entity is one table
of three columns, so schema is `CreateEntity` and nothing more — and it is
deliberately OFF the interface, because an engine with no DDL has nothing to
implement.

The filter is emitted as a SELECTED COLUMN rather than a `WHERE` term. A `WHERE`
would let the engine discard rows the page bound still has to count, so the same
read would come back complete on SQL and partial on a key-value engine. As a
column the engine still evaluates the comparison — a projection still leaves what
it does not name inside the engine — and the driver still learns the verdict per
row.

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

**A body earns its place by being able to FAIL**, and 25 mutations have been
watched do it on both engines: each key operator swapped for its neighbour, the
cursor made inclusive and made direction-blind, the page bound disabled, the
bound moved to AFTER the filter, `Limit` stopped from ending a page, the filter
verdict ignored, the REMOVE half dropped (in the generator and in the Postgres
renderer), a conditional `Put` fallen back to an upsert, a refusal stripped of
its attribution and of its pre-image, the counter's compare-and-swap dropped,
and both width checks disabled. A mutation that breaks the STATEMENT rather than
the answer does not count: the Postgres filter column has to still bind its
argument and still match everything.
