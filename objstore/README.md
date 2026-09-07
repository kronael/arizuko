# objstore

The object store arizuko's daemons address instead of per-daemon SQL. A second
engine is a second driver and no daemon changes (spec
[`6/37`](../specs/6/37-store-model.md), surface
[`6/40`](../specs/6/40-store-surface.md)).

**Three daemons and one library run on this.** `authd/authstore`,
`emaid/emaistore`, `chanlib/chanstore` and `runed/runstore` hold no SQL at all,
and `store` has begun: `pane_sessions` and `secret_use_log` are entities inside
a package whose other tables are still statements, which is what a PARTIAL move
looks like — one implementation per table at every moment, never a fallback.

emaid is the first that reaches storage ONLY through this package: nothing
holds a statement against `emaid.db` any more, because chanlib's own two tables
became entities with it. chanlib is the widest single move — its entities land
in EIGHT adapter databases, one per channel — and it is the first mover that is
a library rather than a daemon, so the driver is picked by the opener the
adapters share. `authd` still hands its pool to `audit` and `store`, so the
daemon around `authstore` stays SQLite-only even though `authstore` itself does
not.

runed is the move measured on a second ENGINE end to end: 91 of its 114 tests
failed on `AUTOINCREMENT` against PostgreSQL 17.11 before it, and none do after.
It is also the one whose three entities take three DIFFERENT partitions —
`spawns` one constant, `session_log` the folder, `circuit_breaker` the folder as
the whole coordinate — because they answer three different questions. authd took
one constant for a whole daemon and `6/37` step 3 writes that up as a mistake.

## Two primitives

```go
Query(ctx, Query) ([]Item, error)
GuardedBatch(ctx, []Check, []Write) (committed bool, err error)
```

That is the whole interface. What arizuko needs beyond it is a PROTOCOL — a
package-level function over these two, written once and shared by every driver
— never a further method some engine cannot carry.

| protocol                | file            | the caller that asks for it                                      |
| ----------------------- | --------------- | ---------------------------------------------------------------- |
| `Increment`             | `protocol.go`   | `store/invites.go`: `used_count = used_count + 1 … RETURNING`    |
| `Replace`               | `replace.go`    | `resreg.Apply`, whose 417-row krons scope cannot be one batch    |
| `ReadConsistent`        | `consistent.go` | `resreg.ExportSnapshot`, N reads that have to describe ONE state |
| `AddEdge` / `Reachable` | `reach.go`      | the membership cycle check, written twice (BUGS `L256`)          |

`committed == false` with a nil error is a REFUSED batch: a condition did not
hold. It is a normal outcome, the answer `authd`'s `ClaimRefreshToken` already
gives as `won=false`, and it must never arrive as an error.

**The claim-and-return is not written.** `store.DueTasks` is `UPDATE
scheduled_tasks SET status='firing' WHERE status='active' AND next_run <= ?
RETURNING …` — `Query` plus a per-row guarded batch, which is the form
`routd/pending_actions.go:421` already uses in SQL. It goes in with the daemon
that owns it; nothing here needs it first.

## The version marker

Three of the four rest on one item per scope, because all three are UNBOUNDED —
the row set, the read set or the graph frontier is not known before the call
begins, so none of them fits in one all-or-nothing batch. The marker carries
`version` (a value every writer changes), `holder` (present only while a replace
runs, and its value is the fencing token) and `since`.

`EnsureScope` creates it where a daemon already calls `CreateEntity`, and a
BOUNDED writer adds `Bump(scope)` to its own batch so that an export can see it.
`ReadConsistent` detects exactly the writers that carry that `Bump` and no
others — a discipline rather than a guarantee the engine gives, asserted as such
in the suite so nobody reads more into it.

**What `Replace` costs `resreg.Apply`, said plainly.** Apply runs every DELETE
and INSERT in ONE SQL transaction today, so a failure part-way rolls the
subsystem back to its pre-image — the rollback `L44` depends on. A marker-gated
sequence has none. A replace that dies half-way leaves the scope part-written
with its claim held, and recovery is to run it again, never to undo it. That is
what buys the unbounded row set, and it is the price `6/38` costed.

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

| path            | what it holds                                                     |
| --------------- | ----------------------------------------------------------------- |
| `objstore.go`   | `Store`, `Item`, `Key` — the object model                         |
| `query.go`      | the read shape: key predicate, filters, order, the caller's bound |
| `batch.go`      | the write shape, and the bounds every driver enforces             |
| `ordered.go`    | the declared widths every driver refuses a value against          |
| `protocol.go`   | `Increment` — the counter, and what a protocol looks like         |
| `marker.go`     | `Scope`, `EnsureScope`, `Bump` — the version marker three share   |
| `replace.go`    | `Replace` — the versioned replace, and the claim it writes under  |
| `consistent.go` | `ReadConsistent` — the multi-read that retries a moved scope      |
| `reach.go`      | `Reachable`, `AddEdge` — the graph walk and its version guard     |
| `sqlstore/`     | the SQL driver: one generator, two dialects                       |
| `conformance/`  | the test bodies every driver runs                                 |

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

The protocols were watched the same way, and **the mutation is what sized the
code**: two conjuncts survived being deleted, so they were deleted. The sixteen
that turn a body red are the claim's compare-and-swap, its held-scope refusal,
the fence on every batch a replace writes, the plan's version guard, the finish
compare-and-swap, keeping a claim after a partial write, the batch bound, the
whole-set duplicate check, `EnsureScope`, `Bump`'s changed value, the second
marker read, the retry, the held and missing refusals on a consistent read, the
graph's version guard, its retry, the transitive frontier and the walk's own
seed. A body that could not go red for one of them was rewritten until it could:
two of them first passed against broken code because the interleave they needed
never happened, which is why `slip` forces every one of them.
