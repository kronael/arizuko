# objstore

The object store every arizuko daemon addresses. A second engine is a second
driver and no daemon changes. Who owns which table: `ARCHITECTURE.md` § Data
ownership. How to run the server: `INSTALL.md` § The store server.

<!-- twin:store -->

```
┌──────────────────────────────────────────────────────────────┐
│ daemons: routd, runed, authd, onbod, dashd, webd, adapters   │
│ each through its store layer: store, runstore, authstore,    │
│ chanstore, emaistore, dashstore                              │
└──────────────────────────────┬───────────────────────────────┘
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ objstore.Store: Query, GuardedBatch                          │
│ protocols over the two: Increment, Replace, ReadConsistent,  │
│ AddEdge and Reachable                                        │
└──────────────────────────────┬───────────────────────────────┘
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ dynstore: a table is <owner>.<entity>; the daemon signs with │
│ its own key; Query and TransactWriteItems only               │
└──────────────────────────────┬───────────────────────────────┘
                               │ DynamoDB protocol over TLS,
                               │ DYNAMODB_URL
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ ExtendDB: one account per instance; one IAM user per daemon, │
│ its policy bounded to the owners store.StoreReach lists      │
└───────────────┬──────────────────────────────┬───────────────┘
                ▼                              ▼
┌──────────────────────────────┐ ┌─────────────────────────────┐
│ PostgreSQL <data-db>: one    │ │ <data-db>_catalog: accounts,│
│ table per DynamoDB table     │ │ IAM, the table registry,    │
│                              │ │ settings                    │
└──────────────────────────────┘ └─────────────────────────────┘
```

<!-- /twin -->

**Everything runs on this.** `store`, `authd/authstore`, `runed/runstore`,
`emaid/emaistore` and `chanlib/chanstore` hold no SQL: every table in the tree is
an entity, and every entity is served by `dynstore` against a DynamoDB-protocol
server. One driver, one implementation per table, never a fallback.

A registered `resreg` resource is an entity too, which took a second threshold:
a table `arizuko apply`/`export`/`plan`/`get`/`archive` reads could not become
one until the engine stopped assuming every resource is an N-column table (BUGS
`L259`). A resource DECLARES its shape and the engine dispatches on the
declaration (`resreg/entity.go`).

Partitions are per entity, not per daemon: runed's three take three DIFFERENT
ones — `spawns` a constant, `session_log` the folder, `circuit_breaker` the
folder as the whole coordinate — because they answer three different questions.
authd took one constant for a whole daemon, and every non-point read there is
a whole-partition read.

## Two primitives

```go
Query(ctx, Query) ([]Item, error)
GuardedBatch(ctx, []Check, []Write) (committed bool, err error)
```

That is the whole interface. What arizuko needs beyond it is a PROTOCOL — a
package-level function over these two, written once and shared by every driver
— never a further method some engine cannot carry.

| protocol                | file            | the caller that asks for it                                                    |
| ----------------------- | --------------- | ------------------------------------------------------------------------------ |
| `Increment`             | `protocol.go`   | `store/invites.go`: a redemption moves `used_count` and must see the new value |
| `Replace`               | `replace.go`    | `resreg.Apply`, whose 417-row krons scope cannot be one batch                  |
| `ReadConsistent`        | `consistent.go` | `resreg.ExportSnapshot`, N reads that have to describe ONE state               |
| `AddEdge` / `Reachable` | `reach.go`      | the membership cycle check, written twice (BUGS `L256`)                        |

`committed == false` with a nil error is a REFUSED batch: a condition did not
hold. It is a normal outcome, the answer `authd`'s `ClaimRefreshToken` already
gives as `won=false`, and it must never arrive as an error.

**The guarded transition is the pattern the two primitives exist for.** Read
the row, decide, then write under a condition on the row the decision came
from: invite redemption conditions on the `used_count` it read, a hold
resolution on `status`. The shape is a guarded BATCH, one condition plus a
bounded set of writes, so the write that acts on the decision and the writes
it implies land together or not at all.

**The claim-and-return is not on the interface.** `store.DueTasks` is the one
shape that would ask for it — every active task read, then a status write per
row conditioned on the status the read saw — and the caller writes it out of
the two primitives. Nothing here needs a third.

## The version marker

Three of the four rest on one item per scope, because all three are UNBOUNDED —
the row set, the read set or the graph frontier is not known before the call
begins, so none of them fits in one all-or-nothing batch. The marker carries
`version` (a value every writer changes), `holder` (present only while a replace
runs, and its value is the fencing token), `intent` (what that holder is
writing) and `since`.

`EnsureScope` creates it where a daemon already calls `CreateEntity`, and a
BOUNDED writer adds `Bump(scope)` to its own batch so that an export can see it.
`ReadConsistent` detects exactly the writers that carry that `Bump` and no
others — a discipline rather than a guarantee the engine gives, asserted as such
in the suite so nobody reads more into it.

**What `Replace` costs `resreg.Apply`, said plainly.** A marker-gated sequence
has no pre-image to roll back to. A replace that dies half-way leaves the scope
part-written with its claim held, and recovery is to run it again, never to
undo it. That is what buys the unbounded row set, and it is the price paid
for it.

**Roll-forward is what gives the claim back.** Nothing releases one: a released
half-written scope reads as settled and correct, which is the silent corruption
the claim exists to stop. So a `Claim` names the WORK — the intent, derived from
the document — and a replace whose intent matches the holder's takes the claim
over and finishes it. That is a proof about the document, never about the dead
replace's process: a slow replace on another host and a dead one look identical,
so `since` stays what an OPERATOR reads and never a predicate this package
evaluates. `Claim.Take` is where the operator asserts what cannot be inferred,
and it costs what the intent match buys — rows outside the taking document's own
delete set stay part-written. Without this, one failed `arizuko apply` disabled
a subsystem's config lane for good (`L267`).

## What gets on the surface

**A shape goes on the interface only when a PRODUCTION call site needs it and
the caller cannot build it out of what is already here.** Not a test, not a
conformance body, not a helper this package wrote for itself. The surface was
widened once and trimmed back the same day, and the rule is written down rather
than re-derived.

What survived, and the caller that kept it:

| shape                                    | who asks for it                                                                                            |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `<`, `<=`, `>`, `>=` on the sort key     | the poll cursor, history, bot activity, the retry feed (`routd/db.go:315`, `reads.go:28`, `:1181`, `:406`) |
| `begins_with` on the sort key            | containment — every folder-scoped read                                                                     |
| `Write.Remove`                           | `store/onboarding.go:262`, a status set and five admission fields removed in one write                     |
| `Cond` on an item a batch does not touch | authd's family check, `refresh_tokens.go`                                                                  |
| `ItemAbsent` on a `Put`                  | create-if-absent, every conditional put authd makes                                                        |
| `Merge`                                  | create-or-merge, at five call sites that spelled it as two actions on one item                             |
| `Write.Init`                             | `added_at` on a group, `added_by` on an edge — facts a merge must not restate                              |
| 100 actions, one per item                | DynamoDB's `TransactWriteItems` bound, so a caller cannot write a batch one engine alone runs              |

What did not: `BETWEEN` (no two-sided range anywhere in `store`, `routd`,
`onbod`, `dashd` or `resreg`), the exposed page (`After` / `More` /
`Examined`), the attributed refusal, and the conditional whole-item replace.
Each was consumed by exactly one thing — this package's own protocol helpers
and its own tests.

**`Merge` is the op the surface was missing, and it earned its place the way
the rule above asks.** `Update` means must-exist, because the refusal IS the
compare-and-swap; that left create-or-merge with no spelling, so `putSecret`,
`putGroup`, `SetLastReply`, `RecordProactiveFire` and `putMessage`'s
chat-open each wrote it as a create-if-absent plus the `Update` its refusal
selected. `ValidateBatch` forbids two actions on one item, so each of them held
a transaction to run the pair, and none of their writes could ride in anybody
else's batch. `Merge` merges its attributes in, takes `Remove` away, writes
`Init` only when it CREATES the item, and never refuses — DynamoDB's
`UpdateItem`, `Init` being its `if_not_exists(attr, :v)` half.

**It is not a `Put`, and that is the trap it exists to close.** A `Put` is a
whole-item replace, so an unconditional one erases every attribute the writer
did not name — a secrets item carries six of which surrogate OAuth owns four,
none of them the metadata writer's. `AMergeKeepsWhatTheWriterDidNotName` is the
body that names the erased credential when somebody reaches for a `Put` here.

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

## Modelling a table as items

Five rules decide an entity's coordinate, and each entity file argues its
choice against them:

1. Partition by the coordinate a read filters on, and count the writes that
   coordinate concentrates.
2. Count the queries that carry no partition key before you design. Each one
   is a whole-partition read, or a second entity that answers it.
3. A foreign key becomes an embed or a duplicated key. Choose one and say
   which: there is no join to fall back on.
4. Never order by a generated id. Order by a fixed-width `core.TS` value.
5. Separate history from state. A log that only grows and a row that changes
   in place take different coordinates.

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
| `replace.go`    | `Replace` — the versioned replace and the claim it rolls forward  |
| `consistent.go` | `ReadConsistent` — the multi-read that retries a moved scope      |
| `reach.go`      | `Reachable`, `AddEdge` — the graph walk and its version guard     |
| `dynstore/`     | the DynamoDB driver: one request per shape, and the endpoint env  |
| `conformance/`  | the test bodies a driver runs                                     |

## The DynamoDB driver

`dynstore` renders each shape as one request. A read is a key condition —
`begins_with` for a prefix — plus a `FilterExpression` for `Where` and a
`ProjectionExpression` for `Project`, and the driver walks `LastEvaluatedKey`
until the caller's `Limit` is met, so the engine's page cap is the driver's
problem and never the caller's. A separator-bearing prefix (`Key.Sep`) is
narrowed in the driver, because a filter may not name a key attribute and a key
condition takes one sort-key term. A write batch is one `TransactWriteItems` of
every check and write; a cancellation whose reasons carry a
`ConditionalCheckFailed` comes back as a refusal, not an error.

The driver speaks six operations: `Query`, `TransactWriteItems`,
`CreateTable`, `DescribeTable`, `DeleteTable` and `ListTables`. It never
calls `GetItem`, `Scan` or `BatchWriteItem`. A batch of one write is still a
`TransactWriteItems` of one, so a refusal has one shape, and
`ValidateQuery` refuses a read with no partition key, which would be a full
scan.

Every attribute name goes through an alias (`#n0`) and every value through a
placeholder (`:v0`), because arizuko attribute names such as `status`,
`value`, `state`, `name`, `size` and `key` are DynamoDB reserved words. Every value is type `S`. The
batch bound in `batch.go`, 100 actions and one action per item, is DynamoDB's
`TransactWriteItems` bound; a server that is to stand in for DynamoDB must
also keep its item size and key length limits.

The table name carries the owner in front of the entity (`Prefix`, so
`routd.acl`), which is how one endpoint holds every owner of an instance and a
batch still stays inside one of them. `env.go` is what a daemon reads to get
there: `DYNAMODB_URL`, `AWS_REGION`, its own access key pair, and `STORE_CA` —
the endpoint's certificate, base64, because a daemon has its environment and no
file of its own.

## Running the conformance suite

```bash
go test ./objstore/...                                     # the local endpoint
ARIZUKO_TEST_DYNAMO_ENDPOINT=https://127.0.0.1:18443 \
  ARIZUKO_TEST_DYNAMO_CA=/path/to/cert.pem \
  go test ./objstore/...                                   # the PostgreSQL deployment
go test ./objstore/... -race -run TestConformance          # the concurrent bodies
```

`dyntest` starts `amazon/dynamodb-local` itself unless
`ARIZUKO_TEST_DYNAMO_ENDPOINT` names one already running. Amazon's build answers
`CreateTable` in about 4 ms, which is what makes it the default: the ExtendDB
development build charges a one-second control-plane delay per table, and a
suite that makes an owner's thirty-three entities per fixture pays that delay
hundreds of times. **Run the suite against the deployment too** — the two
readings of the protocol are what keep the driver written to the protocol
instead of to one server. `make test` does not pass `-race`; the concurrency
bodies are only proven under the flag above.

The suite is the deliverable. A driver runs the identical bodies against every
server, so an operation a server cannot express does not go on the interface —
the portability discipline a vendor API would have imposed for free.

**A body earns its place by being able to FAIL**, watched, against a driver
broken on purpose: each key operator swapped for its neighbour, containment
widened, the filter dropped, `Limit` and the sort direction dropped, the read
truncated so it stops answering the whole selection, the `REMOVE` half dropped,
the absent-item condition turned into an upsert, the counter's compare-and-swap
dropped, its companion writes dropped, and both width checks disabled. A
mutation that breaks the REQUEST rather than the answer does not count — a body
that goes red because the server refused to parse it has proved nothing.

The protocols were watched the same way, and **the mutation is what sized the
code**: two conjuncts survived being deleted, so they were deleted. The eighteen
that turn a body red are the claim's compare-and-swap, its held-scope refusal,
the fence on every batch a replace writes, the plan's version guard, the finish
compare-and-swap, keeping a claim after a partial write, the intent a takeover
has to match, the intent a claim must carry, the batch bound, the
whole-set duplicate check, `EnsureScope`, `Bump`'s changed value, the second
marker read, the retry, the held and missing refusals on a consistent read, the
graph's version guard, its retry, the transitive frontier and the walk's own
seed. A body that could not go red for one of them was rewritten until it could:
two of them first passed against broken code because the interleave they needed
never happened, which is why `slip` forces every one of them.

## Coverage

`go test ./objstore/ -cover` reports 19.3% (2026-09-14) and the number is an
artefact: `ordered_test.go` is the only test file in the root package, because
everything else here is a protocol a DRIVER has to run. Measured through it:

```
go test ./objstore/dynstore/ -coverpkg=github.com/kronael/arizuko/objstore
→ 81.0% of statements in github.com/kronael/arizuko/objstore
```

`dynstore` lands where its own suite does — 80.4%. `conformance` and `dyntest`
are test libraries and report 0.0% by construction: they have no tests of their
own, they ARE the tests.

`make test` passes no `-race`, so it proves nothing about the concurrency
bodies. Name the flag when you report this package green.
