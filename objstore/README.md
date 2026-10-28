# objstore

objstore implements the object store interface that every arizuko daemon addresses through its store layer. The directory also holds the protocols written over the interface, the DynamoDB driver and the conformance suite that every driver must pass. A second engine is a second driver, and no daemon changes. `ARCHITECTURE.md` section "Data ownership" says who owns which table. `INSTALL.md` section "The store server holds every table of the instance" says how to run the server.

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
│ its own key; data calls: Query, TransactWriteItems           │
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

**Everything runs on this store.** `store`, `authd/authstore`, `runed/runstore`, `emaid/emaistore` and `chanlib/chanstore` hold no SQL. Every table in the tree is an entity, and `dynstore` serves every entity against a DynamoDB-protocol server. There is one driver and one implementation per table, and no fallback.

A registered `resreg` resource is an entity too. A resource declares its shape, and the engine dispatches on the declaration (`resreg/entity.go`).

Partitions are per entity and not per daemon. The three entities of runed take three different partitions, because they answer three different questions. `spawns` uses a constant, `session_log` uses the folder, and `circuit_breaker` uses the folder as the whole coordinate. authd uses one constant partition for the whole daemon, so every read there that is not a point read is a whole-partition read.

## Why the store has this shape

1. **The engine is a server, not a library.** ExtendDB over PostgreSQL speaks the DynamoDB wire protocol. arizuko is one client of it, through a driver on the official SDK. No component holds a state of record on local disk. The protocol has transactions and conditional writes. It has a second implementation, and the same driver reaches the Amazon service when you change one `BaseEndpoint`.
2. **There is no database layer.** `objstore.Store` has two methods, `Query` and `GuardedBatch` (`objstore/objstore.go`). They come from the five operations that arizuko needs: containment by path prefix, the guarded batch, the versioned replace, the reachability guard and the claim-and-return. A caller states a coordinate and a condition and never a plan, so the engine can change without a rewrite. `TestNoPackageOpensSQL` in `tests/components/imports_test.go` fails the build if any package reaches `database/sql`.
3. **A deployment is an account, not a database.** The `tables` registry is keyed by `(account_id, table_name)`. So one server can hold every instance on a host and keep them apart by account. A PostgreSQL role is not the tenancy boundary and must not serve as one.
4. **The engine splits its own state in two.** `<data-db>` holds one PostgreSQL table per DynamoDB table. `<data-db>_catalog` holds accounts, IAM, the table registry and `settings`. The two differ in size, in read pattern and in what a restore destroys. So a control-plane read never contends with a scan of a tenant, and a restore of a tenant cannot take the account registry with it. The `connection_string` names the **catalog**, which is the root of discovery. `settings` carries the name and connection string of the data database, with the encryption key of the deployment.

## Two primitives

```go
Query(ctx, Query) ([]Item, error)
GuardedBatch(ctx, []Check, []Write) (committed bool, err error)
```

That is the whole interface. What arizuko needs beyond it is a protocol: a package-level function over these two methods, written once and shared by every driver. It is never a further method that some engine cannot carry.

| protocol                | file            | the caller that asks for it                                                                         |
| ----------------------- | --------------- | --------------------------------------------------------------------------------------------------- |
| `Increment`             | `protocol.go`   | `store` counters, the `used_count` of an invite and the folder counts, which must see the new value |
| `Replace`               | `replace.go`    | `resreg.Apply`, whose row set can be larger than one batch                                          |
| `ReadConsistent`        | `consistent.go` | `resreg.ExportSnapshot`, N reads that have to describe one state                                    |
| `AddEdge` / `Reachable` | `reach.go`      | the membership cycle check in `store/membership.go`                                                 |

`committed == false` with a nil error is a refused batch: a condition did not hold. That is a normal outcome and never an error. `authd` already gives it as `won=false` in `ClaimRefreshToken`.

**The guarded transition is the pattern that the two primitives exist for.** Read the row and decide. Then write under a condition on the row that the decision came from. Invite redemption conditions on the `used_count` that it read, and a hold resolution conditions on `status`. The shape is a guarded batch: one condition plus a bounded set of writes. So the write that acts on the decision and the writes that it implies land together or not at all.

**The claim-and-return is not on the interface.** `store.DueTasks` is the one shape that would ask for it. It reads every active task and then writes a status per row, conditioned on the status that the read saw. The caller writes it out of the two primitives, and nothing here needs a third.

## The version marker

Three of the four protocols rest on one item per scope: `Replace`, `ReadConsistent` and `AddEdge`. All three are unbounded. The row set, the read set or the graph frontier is not known before the call begins, so none of them fits in one all-or-nothing batch. The marker carries four attributes.

- `version` is a value that every writer changes.
- `holder` is present only while a replace runs, and its value is the fencing token.
- `intent` is what that holder is writing.
- `since` is the start time of the holder.

`EnsureScope` creates the marker where a daemon already calls `CreateEntity`. A bounded writer adds `Bump(scope)` to its own batch so that an export can see the write. `ReadConsistent` detects exactly the writers that carry that `Bump` and no others. That is a discipline and not a guarantee of the engine, and the suite asserts it as such so that nobody reads more into it.

**What `Replace` costs `resreg.Apply`.** A marker-gated sequence has no pre-image to roll back to. A replace that dies half-way leaves the scope part-written with its claim held. Recovery is to run it again and never to undo it. That is the price of an unbounded row set.

**Roll-forward gives the claim back.** Nothing releases a claim. A released half-written scope would read as settled and correct, and the claim exists to stop that silent corruption. So a `Claim` names the work: the intent, derived from the document. A replace whose intent matches the holder takes the claim over and finishes it. That is a proof about the document and never about the process of the dead replace. A slow replace on another host and a dead one look the same. So `since` stays what an operator reads, and this package never evaluates it as a predicate. `Claim.Take` is where the operator asserts what the engine cannot infer. It costs what the intent match buys: rows outside the delete set of the taking document stay part-written. Without roll-forward, one interrupted config write would disable the config lane of a subsystem for good.

## What goes on the interface

**A shape goes on the interface only when a production call site needs it.** The caller must also be unable to build the shape from the existing parts. A test, a conformance body or a helper that this package wrote for itself does not count.

The shapes on the interface, and the caller that keeps each:

| shape                                    | who asks for it                                                                                        |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `<`, `<=`, `>`, `>=` on the sort key     | the poll cursor, history, bot activity and the retry feed in `routd`                                   |
| `begins_with` on the sort key            | containment, in every folder-scoped read                                                               |
| `Write.Remove`                           | `store/onboarding.go`: a status set and five admission fields removed in one write                     |
| `Cond` on an item a batch does not touch | the family check of authd in `refresh_tokens.go`                                                       |
| `ItemAbsent` on a `Put`                  | create-if-absent, in every conditional put of authd                                                    |
| `Merge`                                  | create-or-merge, for callers that otherwise spell it as two actions on one item                        |
| `Write.Init`                             | `added_at` on a group and `added_by` on an edge, which are facts that a merge must not restate         |
| 100 actions, one per item                | the `TransactWriteItems` bound of DynamoDB, so a caller cannot write a batch that only one engine runs |

These shapes are not on the interface: `BETWEEN`, the exposed page (`After`, `More`, `Examined`), the attributed refusal and the conditional whole-item replace. No two-sided range exists in `store`, `routd`, `onbod`, `dashd` or `resreg`. Only the protocol helpers and the tests of this package would use the other shapes.

**`Merge` fills the gap that `Update` leaves.** `Update` means must-exist, because its refusal is the compare-and-swap. So create-or-merge needs its own spelling. Written as two actions, a create-if-absent plus the `Update` that its refusal selects, the pair would need its own transaction. `ValidateBatch` forbids two actions on one item. None of its writes could then ride in the batch of another caller. `Merge` merges its attributes in, takes `Remove` away, writes `Init` only when it creates the item, and never refuses. It is the `UpdateItem` of DynamoDB, with `Init` as its `if_not_exists(attr, :v)` half.

**`Merge` is not a `Put`, and that is the trap it closes.** A `Put` replaces the whole item. An unconditional `Put` erases every attribute that the writer did not name. A secrets item carries four attributes that surrogate OAuth owns beside the encrypted value. The conformance body `AMergeKeepsWhatTheWriterDidNotName` names the erased credential when somebody reaches for a `Put` there.

**The atomic counter is deliberately absent.** The in-place increment of DynamoDB needs a `Number` attribute, as in `SET Price = Price + :p`. `ADD` supports only number and set data types. Every value here is a string. So the counter is `objstore.Increment`, a compare-and-swap protocol: read the value, write with the read value as the condition, and retry a lost race. The caller computed the new value, so the `RETURNING` half comes free.

## The shape of an item

An item is `(partition, sort key, attributes)`. The partition is the coordinate that a caller filters on. For arizuko it is the tenant root, because a folder subtree has to sit inside one partition for a prefix to cover it. The sort key carries the rest: the path below the tenant, or a `core.TS` timestamp where the caller reads in time order.

Every attribute value is a string, and an absent attribute is the only kind of missing value. This one rule gives two results. `Absent` and `Present` mean the same on every engine, and a comparison is a text comparison in every dialect with no cast.

**The rule is enforced and not trusted.** A value that must order is fixed-width. A writer says so once with `OrderedSort` or `OrderedAttr`. Every driver then refuses a key, an attribute, a filter value or a cursor of another width. Discipline alone fails: two timestamp widths in one column make byte order disagree with time order for the rows that share a second.

## Modelling a table as items

Five rules decide the coordinate of an entity, and each entity file argues its choice against them.

1. Partition by the coordinate that a read filters on, and count the writes that this coordinate concentrates.
2. Count the queries that carry no partition key before you design. Each one is a whole-partition read, or a second entity that answers it.
3. A foreign key becomes an embed or a duplicated key. Choose one and say which, because there is no join to fall back on.
4. Never order by a generated id. Order by a fixed-width `core.TS` value.
5. Separate history from state. A log that only grows and a row that changes in place take different coordinates.

## A read is the whole selection

`Query` answers everything that it selects, bounded only by the `Limit` of the caller, which counts the items returned. An engine that caps a single read walks its own cursor to produce the whole answer. DynamoDB caps a read at 1 MB, measured before the filter. The cap does not go away. It becomes the problem of the driver, which is where it belongs, and no caller carries a cursor that it would never use. `authstore` shows the effect. It uses one partition for the whole daemon, so every read that is not a point read scans the whole partition and asks for all of it.

`Where` is still not an index. On an engine whose page cap applies before the filter, a filter that keeps one row in ten thousand still reads ten thousand rows. It does so inside the driver. A selective predicate belongs on the sort key, where it costs nothing.

## Layout

| path            | what it holds                                                     |
| --------------- | ----------------------------------------------------------------- |
| `objstore.go`   | `Store`, `Item`, `Key`: the object model                          |
| `query.go`      | the read shape: key predicate, filters, order, the caller's bound |
| `batch.go`      | the write shape, and the bounds that every driver enforces        |
| `ordered.go`    | the declared widths that every driver refuses a value against     |
| `protocol.go`   | `Increment`: the counter, and what a protocol looks like          |
| `marker.go`     | `Scope`, `EnsureScope`, `Bump`: the version marker three share    |
| `replace.go`    | `Replace`: the versioned replace and the claim it rolls forward   |
| `consistent.go` | `ReadConsistent`: the multi-read that retries a moved scope       |
| `reach.go`      | `Reachable`, `AddEdge`: the graph walk and its version guard      |
| `dynstore/`     | the DynamoDB driver: one request per shape, and the endpoint env  |
| `conformance/`  | the test bodies that a driver runs                                |

## The DynamoDB driver

`dynstore` renders each shape as one request. A read is a key condition, with `begins_with` for a prefix, plus a `FilterExpression` for `Where` and a `ProjectionExpression` for `Project`. The driver walks `LastEvaluatedKey` until the `Limit` of the caller is met. So the page cap of the engine is the problem of the driver and never of the caller. The driver narrows a prefix with a separator (`Key.Sep`) itself. A filter may not name a key attribute, and a key condition takes one sort-key term. A write batch is one `TransactWriteItems` of every check and write. A cancellation whose reasons carry a `ConditionalCheckFailed` comes back as a refusal and not as an error.

The driver speaks six operations: `Query`, `TransactWriteItems`, `CreateTable`, `DescribeTable`, `DeleteTable` and `ListTables`. It never calls `GetItem`, `Scan` or `BatchWriteItem`. A batch of one write is still a `TransactWriteItems` of one, so a refusal has one shape. `ValidateQuery` refuses a read with no partition key, because that read would be a full scan.

Every attribute name goes through an alias (`#n0`) and every value through a placeholder (`:v0`). Attribute names such as `status`, `value`, `state`, `name`, `size` and `key` are DynamoDB reserved words. Every value has type `S`. The batch bound in `batch.go` is `MaxBatchActions`, 100 actions with one action per item. It is the `TransactWriteItems` bound of DynamoDB. A server that stands in for DynamoDB must also keep its item size and key length limits.

The table name carries the owner in front of the entity (`Prefix`, so `routd.acl`). So one endpoint holds every owner of an instance, and a batch still stays inside one owner. `env.go` is what a daemon reads to get there: `DYNAMODB_URL`, `AWS_REGION`, its own access key pair, and `STORE_CA`. `STORE_CA` is the certificate of the endpoint in base64, because a daemon has its environment and no file of its own.

## Running the conformance suite

```bash
go test ./objstore/...                                     # the local endpoint
ARIZUKO_TEST_DYNAMO_ENDPOINT=https://127.0.0.1:18443 \
  ARIZUKO_TEST_DYNAMO_CA=/path/to/cert.pem \
  go test ./objstore/...                                   # the PostgreSQL deployment
go test ./objstore/... -race -run TestConformance          # the concurrent bodies
```

`dyntest` starts a container of the ExtendDB development build, `dyntest.Image`, which runs over SQLite, unless `ARIZUKO_TEST_DYNAMO_ENDPOINT` names an endpoint that already runs. The container needs docker. The development build charges a control-plane delay per table. A suite that makes all the entities of an owner per fixture would pay that delay hundreds of times. So `dyntest` turns the delays off. **Run the suite against the deployment too.** The two readings of the protocol keep the driver written to the protocol and not to one server. `make test` does not pass `-race`, so the concurrency bodies are proven only under the flag above.

The suite is the deliverable. A driver runs the identical bodies against every server, so an operation that a server cannot express does not go on the interface. A vendor API would impose that portability discipline for free.

**A body earns its place by being able to fail.** Its authors watched it fail against a driver broken on purpose. The mutations were these:

- swap each key operator for its neighbour
- widen containment
- drop the filter, `Limit` and the sort direction
- truncate the read so that it stops answering the whole selection
- drop the `REMOVE` half
- turn the absent-item condition into an upsert
- drop the compare-and-swap of the counter and its companion writes
- disable both width checks

A mutation that breaks the request and not the answer does not count. A body that goes red because the server refused to parse it has proved nothing.

The protocols got the same treatment, and the mutation sized the code. These mutations turn a body red:

- the compare-and-swap of the claim and its held-scope refusal
- the fence on every batch that a replace writes
- the version guard of the plan, and the compare-and-swap that finishes it
- keeping a claim after a partial write
- the intent that a takeover has to match, and the intent that a claim must carry
- the batch bound and the whole-set duplicate check
- `EnsureScope` and the changed value of `Bump`
- the second marker read and the retry of a consistent read, and its held and missing refusals
- the version guard of the graph, its retry, the transitive frontier and the seed of the walk

A body that could not go red for one of these was rewritten until it could. Some bodies need two writers to interleave. Without a forced interleave such a body passes against broken code, so the suite forces it with `SlipAt`.

## Coverage

`go test ./objstore/ -cover` reports 19.3%, measured in this pass. The number is an artefact. `ordered_test.go` is the only test file in the root package, because everything else here is a protocol that a driver has to run. Measured through the driver suite on 2026-09-14:

```
go test ./objstore/dynstore/ -coverpkg=github.com/kronael/arizuko/objstore
→ 81.0% of statements in github.com/kronael/arizuko/objstore
```

`dynstore` lands at 80.4%, where its own suite does. That run needs docker, and this pass did not repeat it, so both figures are the last recorded values. `conformance` and `dyntest` are test libraries and report 0.0% by construction. They have no tests of their own, because they are the tests.

`make test` passes no `-race`, so it proves nothing about the concurrency bodies. Name the flag when you report this package green.
