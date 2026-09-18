---
status: partial
depends:
  [
    37-store-model,
    40-store-surface,
    41-qemu-substrate,
    23-llm-gateway,
    ../5/16-mcp-rest-unification,
    ../5/33-paths-roles,
  ]
---

# specs/6/42 — DynamoDB over PostgreSQL: one server, one driver, no database layer

Decision (maintainer): **the integration is DynamoDB and PostgreSQL. arizuko
speaks the DynamoDB API to a server that stores in PostgreSQL, removes its
database layer, and keeps nothing of SQLite — no owner file, no adapter file,
no search index.** The server is an independent project; arizuko is one client
of it through a driver on the official SDK, and that driver points at Amazon
DynamoDB unchanged.

The opening question is whether that server is adopted or built (§1). The
evidence says adopt: ExtendDB, an Apache-2.0 DynamoDB-protocol server over
PostgreSQL managed by Amazon Web Services, meets every constraint this spec
states, and the conformance suite is the gate it must pass. `dynad`, the Go
server the owner's direction names, is designed here (§4) and ships only if
ExtendDB fails a gate that upstream does not close inside the window of §10.

The interface does not change. `objstore.Store` stays two methods, the four
protocols stay package functions, and `objstore/conformance` (22 bodies) stays
the proof; this spec adds one driver and removes one, and every daemon changes
by zero lines at the storage seam.

## 0. Three measurements this spec rests on

**The Postgres dialect of the current driver works.** With
`ARIZUKO_TEST_PG_DSN` pointed at `postgres:17-alpine`, `go test
./objstore/sqlstore/ -run TestConformance` is green. The two primitives, the
four protocols and the 22 bodies already hold on Postgres; what does not hold
is everything AROUND the driver.

**The gate of §10 step 1 is passed, on two servers (2026-09-13).** The 22
conformance bodies of `objstore/conformance` are green through `dynstore`
against `ghcr.io/extenddb/extenddb-postgres:0.1.11` over `postgres:17-alpine`
(3.6 s) and against `amazon/dynamodb-local` (10.3 s). The ExtendDB run is over
TLS on port 18443 — the server refuses to start without it — signed by an IAM
user's own access key, against `control_plane_delay_seconds = 0`, and trusting
the certificate `extenddb init` generates. The second server is what §9 asks
for and what keeps the driver written to the protocol: a green against one
server alone cannot tell the protocol from that server's reading of it. Neither
dynalite nor moto can run the suite at all (§1), and ExtendDB's `extenddb-dev`
image stores in SQLite, so it proves nothing about the deployment.

**The daemon suites do not open on Postgres, and the cause is one thing.**
`store` and `onbod` were run against the same server (`scratchpad/pg-store.log`,
`pg-onbod.log`): `onbod` fails 111 of 145 tests, every one with
`0001-initial-schema.sql: ERROR: syntax error at or near "VIRTUAL"`; `store`
fails its first six with `read the declaration of messages_fts: unused
argument: 0` and then the binary dies, so 285 of its 294 tests never ran.
Both messages are the FTS5 index: `routd/migrations/0001-initial-schema.sql:43`
embeds `CREATE VIRTUAL TABLE messages_fts USING fts5(...)`, and
`store/store.go:233` calls `EnsureMessageSearch` on every engine, which probes
`sqlite_master` with a `?` placeholder (`store/messages_search.go:164-168`).
Neither has a Postgres spelling and nothing gates either by engine. The panic
is a second defect: `store/extra_test.go:37` writes `s, _ := OpenMem()` and
dereferences the nil store.

So the ACL failures, the onboarding flow and the world-creation race are not
Postgres-semantics failures; they are tests that never reached their logic.
The classes a remote engine genuinely changes — the single-writer lock SQLite
lends a cycle walk (`L256`), a read through the pool inside a transaction
(`sqlstore.QueryInTx`'s own comment) — are UNMEASURED until the FTS5 index is
gone. §8 removes it; §9 names the re-run as the first acceptance step.

## 1. Adopt or build — the evidence

The hard constraints, from the driver's needs (§2): the DynamoDB wire
protocol; data in PostgreSQL; `TransactWriteItems` with `ConditionCheck`,
`Put`, `Update`, `Delete`, condition expressions and per-action
`CancellationReasons`; `Query` with a sort-key condition, `begins_with`, a
filter, a projection and `LastEvaluatedKey` paging; `CreateTable`; a licence
that permits production use; a project someone maintains. Every candidate
below was read at its source on 2026-09-12; the quotes are the projects' own.

| candidate                                   | licence                                                   | language   | storage                               | transactions                                | positioning, in its own words                                      | verdict                                                          |
| ------------------------------------------- | --------------------------------------------------------- | ---------- | ------------------------------------- | ------------------------------------------- | ------------------------------------------------------------------ | ---------------------------------------------------------------- |
| **ExtendDB**                                | Apache-2.0                                                | Rust       | PostgreSQL 14+ (also MongoDB, SQLite) | yes, 100 actions, `CancellationReasons`     | "a v0.1 release for development, testing, and experimentation"     | **meets every constraint; adopted, and the gate is passed (§0)** |
| ScyllaDB Alternator                         | ScyllaDB Software License Agreement 1.1, source-available | C++        | ScyllaDB                              | **no**                                      | "compatibility with DynamoDB is fairly complete"                   | no transactions; not PostgreSQL; licence limits                  |
| Amazon DynamoDB Local                       | proprietary "DynamoDB Local License Agreement"            | Java       | SQLite                                | yes                                         | "intended for development and testing purposes only"               | SQLite; a test fixture; no derivative works                      |
| LocalStack                                  | Apache-2.0 (archived repository)                          | Python     | DynamoDB Local inside                 | inherited                                   | "This repository was archived by the owner on Mar 23, 2026"        | wraps DynamoDB Local; archived                                   |
| dynalite                                    | Apache-2.0                                                | JavaScript | LevelDB                               | **no** ("Implement Transactions" is a TODO) | "built on LevelDB for fast in-memory or persistent usage"          | no transactions; not PostgreSQL                                  |
| moto                                        | Apache-2.0                                                | Python     | in-process memory                     | yes                                         | "a library that allows your tests to easily mock out AWS Services" | a mock; no persistence                                           |
| Dynoxide                                    | MIT or Apache-2.0                                         | Rust       | SQLite                                | yes                                         | "not as a production DynamoDB replacement"                         | SQLite; a fixture                                                |
| kumo                                        | MIT                                                       | Go         | memory, optional snapshots            | yes                                         | "A lightweight AWS service emulator written in Go"                 | not PostgreSQL; the only Go server found                         |
| YugabyteDB, CockroachDB, TiDB, FoundationDB | —                                                         | —          | —                                     | —                                           | none advertises a DynamoDB API                                     | —                                                                |

- **ExtendDB** (https://github.com/ExtendDB/extenddb): "ExtendDB is an
  independent open source project managed by Amazon Web Services. It is not
  Amazon DynamoDB and does not contain any DynamoDB source code"; "Supported
  Operations: CreateTable, DeleteTable, DescribeTable, ListTables, UpdateTable
  … Query, Scan (key conditions, filters, projections, pagination, index
  selection) … TransactGetItems (100 items), TransactWriteItems (100 ops)";
  "PostgreSQL 14+"; "Self-hosted deployments — run DynamoDB workloads on your
  own infrastructure". `Cargo.toml` in the tree read 2026-09-12: `version =
"0.1.11"`, `license = "Apache-2.0"`, `rust-version = "1.88"`. The
  announcement (2026-05-20,
  https://aws.amazon.com/blogs/database/introducing-extenddb-an-open-source-dynamodb-compatible-adapter-with-pluggable-storage-backends/):
  "an implementation of the DynamoDB wire protocol, written in Rust and
  developed by AWS engineers"; "This is a v0.1 release for development,
  testing, and experimentation." AWS engineers to InfoQ (2026-06-07,
  https://www.infoq.com/news/2026/06/extenddb-dynamodb-adapter/):
  "Initially, in the current version it doesn't differ that much except you can
  use Postgres. But as we progress, it'll have more parity with DynamoDB and
  potentially be suitable to run for production workloads." A user in the same
  article: "Latency shoots up under high write load. I'm seeing 300ms p90
  latency on update_item commands." §3 measures it against this spec's
  contract, source line by source line.
- **Alternator**
  (https://docs.scylladb.com/manual/stable/alternator/compatibility.html):
  "DynamoDB's multi-item transaction feature (TransactWriteItems,
  TransactGetItems) is not supported." That alone excludes it: `GuardedBatch`
  is a transaction. The licence moved with "ScyllaDB Enterprise 2025.1 …
  from closed source to source available" and "ScyllaDB OSS AGPL 6.2 will stand
  as the final OSS AGPL release"
  (https://www.scylladb.com/2024/12/18/why-were-moving-to-a-source-available-license/);
  the current agreement (`LICENSE-ScyllaDB-Source-Available.md` in the
  repository) is "Restricted to "Never Customers" Only" with a usage limit of
  "10TB and/or an upper limit of 50 VCPUs".
- **DynamoDB Local**
  (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html):
  "intended for development and testing purposes only"; "TransactionConflictExceptions
  aren't thrown"; "Read operations are eventually consistent"; "table names
  are case insensitive"; its file is `shared-local-instance.db`. The jar's
  `LICENSE.txt` is the "Amazon DynamoDB Local License Agreement" under the AWS
  Customer Agreement: "AWS Licensor or its licensors own all right, title, and
  interest in and to the Software". It stays what it is good for: the oracle
  of §9.
- **LocalStack** (https://github.com/localstack/localstack): "This repository
  was archived by the owner on Mar 23, 2026. It is now read-only"; its
  DynamoDB is DynamoDB Local (https://docs.localstack.cloud/aws/services/dynamodb/).
- **dynalite** (https://github.com/mhart/dynalite, now under `architect`):
  "Implement Transactions" is on its TODO list; its author recommends
  DynamoDB Local "if you don't mind the overhead of starting the JVM".
- **moto** (https://docs.getmoto.org/en/latest/docs/services/dynamodb.html):
  `transact_write_items`, `query`, `create_table` are marked implemented; the
  FAQ: "Moto automatically deletes any created data after a Moto-decorator
  ends". **Dynoxide** (https://github.com/nubo-db/dynoxide): "A DynamoDB
  emulator in Rust, backed by SQLite"; "not as a production DynamoDB
  replacement". **kumo** (https://github.com/sivchari/kumo): "A lightweight AWS
  service emulator written in Go"; in-memory with optional persistence.

**Verdict: adopt ExtendDB, gated.** It is the only candidate that satisfies
every constraint, it is maintained by the vendor whose protocol it speaks, and
§3 shows its PostgreSQL engine already does the four things a driver leans on
and that a build would have had to get right. What is adopted is a wire
protocol and a container image, so the coupling is the same as to `litellm`
(`template/services/litellm.yml`): a third-party image whose env is what the
fragment names. The risks are maturity — "v0.1 … for experimentation", by its
own words — and a language the house does not write. The gate answers the
first: the 22 bodies and the `-race` runs pass against it or it is not
adopted. The wire protocol answers the second: a fix arizuko cannot make
upstream is a fix arizuko makes in `dynad` (§4), against the same suite, with
no daemon changing. Building first would spend twelve to sixteen engineer-days
(§11) to reach a server the gate can already be pointed at today.

## 2. The contract the server must meet — derived from the code

`objstore` reaches the engine through three calls and one test hook: `Query`
and `GuardedBatch` (`objstore/objstore.go`), the driver-local `CreateEntity`
(13 non-test callers, e.g. `store/entities.go:278`, `audit/audit_log.go:78`,
`runed/runstore/runstore.go:71`), and the conformance `Factory`, which needs a
fresh empty store per call and therefore a way to drop what it made. Six
operations of the 58 the service lists
(https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Operations_Amazon_DynamoDB.html):

| operation            | objstore caller                           | what the driver uses                                                                                                             |
| -------------------- | ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `Query`              | `Store.Query`, every read                 | partition equality + one sort-key condition, `FilterExpression`, `ProjectionExpression`, `ScanIndexForward`, `Limit`, pagination |
| `TransactWriteItems` | `Store.GuardedBatch`, every write         | `ConditionCheck`, `Put`, `Update`, `Delete`; `ClientRequestToken`; `CancellationReasons`                                         |
| `CreateTable`        | `CreateEntity`                            | composite key, both `S`; `PAY_PER_REQUEST`; then `DescribeTable` until `ACTIVE`                                                  |
| `DescribeTable`      | `CreateEntity`'s "is it there" probe      | the table's status                                                                                                               |
| `DeleteTable`        | the test `Factory`'s cleanup; an operator |                                                                                                                                  |
| `ListTables`         | an operator; the copy's verification (§7) | pages of 100                                                                                                                     |

No `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `BatchWriteItem`, `Scan`,
`TransactGetItems`: no objstore path issues them, and a one-write batch is a
`TransactWriteItems` of one action so that a refusal has ONE shape
(`CancellationReasons`) rather than two. `Scan` is the full scan
`objstore.ValidateQuery` refuses before any engine sees it.

**The expression subset the driver emits.** Every attribute name is an alias
(`#a0…`) and every value a placeholder (`:v0…`), because the service refuses a
bare reserved word and `STATUS`, `VALUE`, `STATE`, `NAME`, `SIZE`, `TYPE`,
`KEY`, `SCOPE`, `ROLE`, `ACTION`, `OWNER`, `TOKEN`, `SESSION`, `PATH`, `GROUP`,
`FAMILY`, `SOURCE`, `RESULT` and `MODE` are all reserved and all arizuko
attribute names
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ReservedWords.html).

| expression                                | grammar emitted                                                                                                  |
| ----------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `KeyConditionExpression`                  | `#pk = :p` optionally `AND` one of `#sk = :v`, `<`, `<=`, `>`, `>=`, `begins_with(#sk, :v)`                      |
| `ConditionExpression`, `FilterExpression` | conjunction (`AND`) of `#a = :v`, `<>`, `<`, `<=`, `>`, `>=`, `attribute_exists(#a)`, `attribute_not_exists(#a)` |
| `UpdateExpression`                        | `SET #a = :v, #b = if_not_exists(#b, :w) REMOVE #c, #d`                                                          |
| `ProjectionExpression`                    | `#a, #b, …`                                                                                                      |
| `AttributeValue`                          | `S` only — the one modelling rule of `objstore`                                                                  |

The semantics are the reference's, not arizuko's, and four of them are what a
driver leans on: `Limit` is "The maximum number of items to evaluate (not
necessarily the number of matching items)"; "A FilterExpression cannot contain
partition key or sort key attributes"; results are returned "in order of UTF-8
bytes" and `ScanIndexForward` false reads "in reverse order by sort key value"
(https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html);
and "A single Query operation can retrieve a maximum of 1 MB of data. This
limit applies before any FilterExpression or ProjectionExpression is applied"
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.KeyConditionExpressions.html).
That last one is why the conformance body `AReadReturnsTheWholeSelection`
writes 300 items of 4 KB with the five matching items at the END: a driver
that stops on an empty page passes against a server that pages after the
filter and fails against the service. `TransactWriteItems` "groups up to 100
action requests … no two actions can target the same item … The aggregate size
of the items in the transaction cannot exceed 4 MB", and a condition that does
not hold cancels the request with `TransactionCanceledException` whose
`CancellationReasons` carry `ConditionalCheckFailed` where it failed and
`None` elsewhere
(https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactWriteItems.html).
`REMOVE` of a name the item lacks: "If the attributes don't exist, nothing
happens"; `if_not_exists`: "If the item does not contain an attribute at the
specified path, if_not_exists evaluates to value. Otherwise, it evaluates to
path"
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.UpdateExpressions.html).
Strings order "using the bytes of the underlying UTF-8 string encoding. For
example, "a" (0x61) is greater than "A" (0x41)"
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.NamingRulesDataTypes.html).

**Limits** the server must keep identically, so that "works here" implies
"works on the service": 400 KB per item, 2048-byte partition key, 1024-byte
sort key, 4 KB and 300 operators per expression, 100 actions and 4 MB per
transaction, table names of 3–255 characters in `[a-zA-Z0-9_.-]`
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Constraints.html).

**Consistency.** The driver sends `ConsistentRead: true` on every `Query`: the
service's default is eventual — "the responses might not reflect the results
of a recently completed write operation"
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.ReadConsistency.html)
— and `Increment` tells a lost race from a caller's refusal by re-reading the
counter (`objstore/protocol.go`); a stale re-read would misclassify. A server
over PostgreSQL answers committed state either way. Isolation for `Query` is
read-committed on the service — "between any transactional operation and any
read operation that involves multiple standard reads (BatchGetItem, Query, or
Scan)"
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/transaction-apis.html)
— and the protocols were written for exactly that (§6).

## 3. ExtendDB against the contract — read from its source

Read at `version = "0.1.11"` on 2026-09-12; paths are in its repository.

**It does the four things a driver leans on.**

- **Byte order.** `crates/storage-postgres/src/data/query_scan.rs:34-35`
  renders `COLLATE "C"` for a string sort key, and `:236-280` puts the same
  collation on `ORDER BY` — the comment reads "use COLLATE "C" for string sort
  keys to match DynamoDB". Its DDL (`data/ddl.rs:61-66`) declares `pk TEXT NOT
NULL, sk_s TEXT, item_data JSONB NOT NULL` with no column collation, so the
  comparison carries it instead; correct, and a planner question (§12).
- **The 1 MB page counts evaluated items, before the filter.**
  `crates/engine/src/read_helpers.rs:96-102` adds every raw item's size to
  `response_bytes` and checks `> 1_048_576` BEFORE the filter runs on it, and
  sets `LastEvaluatedKey` to "the last _processed_ item's key". That is the
  service's rule, and the one a pushed-down `WHERE` would break.
- **Conditions are exact, absence included.**
  `crates/storage-postgres/src/data/transactions.rs` fetches each item `FOR
UPDATE` inside one PostgreSQL transaction (`tx_helpers.rs:57-84`), decides a
  create by an "atomic insert-if-absent" and retries a lost create race up to
  `MAX_CREATE_RACE_ATTEMPTS = 5` before answering a `TransactionConflict`
  cancellation (`transactions.rs:382-427`). A `ConditionCheck` on an item that
  must not exist is therefore honoured — the check the SQL driver refuses
  today because `READ COMMITTED` alone cannot (`objstore/sqlstore/batch.go`
  `checkStmt`).
- **Idempotency.** `tx_helpers.rs:394-412` inserts `(account_id, token,
fingerprint)` into `idempotency_tokens` inside the transaction, with a
  ten-minute window; the engine answers `TransactionInProgressException` and
  `IdempotentParameterMismatchException` (`crates/engine/src/transact_write_items.rs`).

**It also carries what a build would have had to add later.** IAM users,
policies and access keys, administered by `extenddb manage` and a
`/management` REST API; resources are "`arn:aws:dynamodb:<region>:<account-id>:table/<table-name>`.
Wildcard matching (`*`, `?`) in policy Resource fields follows AWS IAM
conventions" (`docs/manuals/10-security-model.md:136`), which is what §7 uses
for per-daemon table scoping. `DynamoDBStreams_20120810` targets
(`crates/server/src/request_helpers.rs:14`), which is §8's reopen path. A
production image, `extenddb/extenddb-postgres`, with a bootstrap container
that runs `extenddb init` (`docs/local-dev-docker-compose.md`); the dev image
is SQLite-backed and "not the production image" (`docs/dev-image.md`), so it
is not used anywhere here.

**What is different, and what each costs.** "TLS is always enabled"
(`crates/config/src/lib.rs:95`), with a self-signed certificate at init: every
daemon carries `AWS_CA_BUNDLE`, which the Go SDK reads from the environment
("Sets the path to a custom Credentials Authority (CA) Bundle PEM file",
https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/config). A table is `CREATING`
for `control_plane_delay_seconds` (default 5; `docs/getting-started.md:277`
sets it to 0), so the driver's `CreateEntity` polls `DescribeTable` — the
loop the service needs anyway — and the instance sets 0. Deadlock: two
transactions that lock the same two items in opposite orders are aborted by
PostgreSQL, and the tree has no handler for `40P01` (grep over
`crates/storage-postgres/src`), so the abort surfaces as an internal error;
the SDK retries 5xx and the token makes the retry safe, and §12 measures it.
Latency under high write load is a user report, not arizuko's load: krons
writes about 1,560 audit rows and 140 messages a day (`6/37`).

**The gate.** `objstore/conformance` through `dynstore` against
`extenddb/extenddb-postgres` on `postgres:17-alpine`, all 22 bodies, `-race`
on the three concurrent ones; then the same suite against DynamoDB Local. A
body that fails against ExtendDB and passes against DynamoDB Local is an
ExtendDB defect; it is reported upstream, and if it is not closed inside the
window of §10 step 1, `dynad` ships in its place.

## 4. `dynad` — the fallback, designed

`dynad` is what the house builds if the gate fails: a DynamoDB-protocol server
over PostgreSQL in Go, its own repository `github.com/kronael/dynad`, its own
`go.mod`, no import of anything under `github.com/kronael/arizuko`, useful to
anyone with a PostgreSQL. It ends in `d` because it is a daemon. arizuko would
consume it as an image AND as a Go module, so the conformance suite could
mount `server.Handler` on an `httptest.Server` inside `go test` — the one
advantage a Go server has over ExtendDB, and the reason `make test` under §7
runs a container rather than a function.

**Layout.** `cmd/dynad/` (config, listener, health) · `wire/` (JSON 1.0
framing, `AttributeValue` codec, the error envelope) · `expr/` (one tokenizer,
one parser, ONE evaluator for key-condition, condition/filter, update and
projection expressions) · `sigv4/` (verification against a named key) · `pg/`
(catalog, DDL, `Query`, `TransactWriteItems`, the idempotency table) ·
`server/` (`Handler(pool, keys) http.Handler`, the dispatch on `X-Amz-Target`)
· `conformance/` (request/response cases run against dynad AND DynamoDB
Local). A TOML config first, a TOML keys file second, `:8080` in a container.

**Contract.** `POST /` with `Content-Type: application/x-amz-json-1.0` and
`X-Amz-Target: DynamoDB_20120810.<Operation>`, Signature Version 4, the
service's response shapes and its error envelope
`{"__type":"com.amazonaws.dynamodb.v20120810#<Name>","message":"…"}`
(https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.LowLevelAPI.html,
https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html).
The six operations and the grammar of §2, limits identical to the service's,
and every other operation, function, operator or type answered with
`ValidationException` naming it. Bare identifiers are refused everywhere —
stricter than the service, in the direction that keeps "works on dynad"
implying "works on DynamoDB".

**Schema.** `dynad_tables(name, physical, key_schema, attribute_definitions,
created_at, status)`; `dynad_requests(access_key, token, request_sha256,
state, response, at)`; and per table `"t_<id>"(pk TEXT COLLATE "C", sk TEXT
COLLATE "C", item JSONB, PRIMARY KEY (pk, sk))` — the collation on the column
so the primary-key index serves every comparison, the physical name a catalog
surrogate so DynamoDB's 255-character case-sensitive names never meet
PostgreSQL's 63-byte identifier, the item stored as the typed wire form.

**`Query`** is one statement: `WHERE pk = $1 [AND sk <op> $2] [AND sk > $3]
ORDER BY sk [DESC] [LIMIT $n]`, with `begins_with` as the byte range `sk >= $p
AND sk < $p'` (`$p'` is `$p` with its last byte incremented, open-ended past
`0xFF`), never `LIKE`. Rows stream from pgx (https://github.com/jackc/pgx,
MIT); dynad stops at `Limit` or 1 MB of evaluated items, THEN filters and
projects in `expr/`, and sets `LastEvaluatedKey` from the last evaluated row.

**`TransactWriteItems`** is one transaction at `READ COMMITTED`: for every
coordinate, in ascending order of key, `pg_advisory_xact_lock($1)` —
"Obtains an exclusive transaction-level advisory lock, waiting if necessary"
(https://www.postgresql.org/docs/17/functions-admin.html) — with the key the
first eight bytes of `SHA-256(physical ‖ 0x1F ‖ pk ‖ 0x1F ‖ sk)`. Sorted
acquisition is what makes deadlock impossible ("acquire locks on multiple
objects in a consistent order",
https://www.postgresql.org/docs/17/explicit-locking.html), so dynad never
emits `TransactionConflict`. Under the locks: read each item, evaluate every
condition in `expr/` (an absent row is the empty item, so
`attribute_not_exists` is exact), and on any failure `ROLLBACK` with
`CancellationReasons`; otherwise `Put` is `INSERT … ON CONFLICT DO UPDATE`,
`Update` computes the document in Go against the pre-update image and writes
it the same way, `Delete` is `DELETE`, then `COMMIT`. The `ClientRequestToken`
row is inserted `in_progress` before the transaction; a repeat within ten
minutes returns the recorded response, a different request hash answers
`IdempotentParameterMismatchException`, an in-flight duplicate answers
`TransactionInProgressException`.

**Identity.** SigV4 recomputed over the canonical request with the named key's
secret and compared in constant time
(https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html);
each key names the table prefixes it may read and write.

**Its own acceptance** is a request/response table run against itself and
against DynamoDB Local, whose documented differences bound what the oracle
cannot prove ("doesn't throw TransactionConflictExceptions", "Read operations
are eventually consistent", "table names are case insensitive").

Cost if it ships: **12–16 engineer-days** (§11), on top of everything else.

## 5. The driver: `objstore/dynstore`

`dynstore` is a library, so it does not end in `d`; it lives beside the
interface it implements and replaces `objstore/sqlstore`. `New(client
*dynamodb.Client, owner string) *Store`, where `owner` is the table prefix
(§7): entity `messages` under owner `routd` is table `routd.messages`. A daemon
that holds three sibling files today holds three `Store`s with three prefixes
on one client; a `GuardedBatch` stays inside one owner, which is the boundary
it has today. The client is `aws-sdk-go-v2/service/dynamodb` with
`BaseEndpoint` set — "In practice, BaseEndpoint will most likely be used to
point your client at a development or preview instance of a service"
(https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html).

**How each objstore shape becomes a request.** The key attributes are `pk` and
`sk` in every table.

| objstore                                            | DynamoDB                                                                                                                                                                         |
| --------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Query.Entity`, `.Partition`                        | `TableName: <owner>.<entity>`, `KeyConditionExpression: #pk = :p`                                                                                                                |
| `Key.Op` `ExactKey`/`LtKey`/`LeKey`/`GtKey`/`GeKey` | `AND #sk = :k` / `<` / `<=` / `>` / `>=`                                                                                                                                         |
| `Key.Op` `PrefixKey`, `Sep == ""`                   | `AND begins_with(#sk, :k)`                                                                                                                                                       |
| `Key.Op` `PrefixKey`, `Sep != ""`                   | `AND begins_with(#sk, :k)`; items whose key is neither `Value` nor prefixed by `Value+Sep` are dropped in the driver BEFORE counting toward `Limit`                              |
| `Where []Cmp`                                       | `FilterExpression`, `AND`-joined; `Absent` → `attribute_not_exists(#a)`, `Present` → `attribute_exists(#a)`                                                                      |
| `Project`                                           | `ProjectionExpression`                                                                                                                                                           |
| `Descending`                                        | `ScanIndexForward: false`                                                                                                                                                        |
| `Limit`                                             | `Limit: remaining` per request; the walk continues on `LastEvaluatedKey` until `remaining == 0` or the key is absent                                                             |
| every read                                          | `ConsistentRead: true`                                                                                                                                                           |
| `Check{Entity, Key, Cond}`                          | `ConditionCheck` with `ConditionExpression` = `Cond`                                                                                                                             |
| `Write{Op: Put}`                                    | `Put{Item: key + Attrs}`; `ItemAbsent` → `attribute_not_exists(#pk)`                                                                                                             |
| `Write{Op: Update}`                                 | `Update{UpdateExpression: SET … REMOVE …}`, `ConditionExpression: attribute_exists(#pk) [AND where…]` — must-exist is `Update`'s meaning                                         |
| `Write{Op: Merge}`                                  | `Update` with no condition; `Init` renders as `#i = if_not_exists(#i, :iv)`; no attributes at all renders as `SET #pk = if_not_exists(#pk, :p)`, which creates and never changes |
| `Write{Op: Delete}`                                 | `Delete`; `ItemPresent` → `attribute_exists(#pk)`; `Where` appended                                                                                                              |
| `Cond.Item` `ItemPresent`/`ItemAbsent`/`ItemAny`    | `attribute_exists(#pk)` / `attribute_not_exists(#pk)` / nothing, `AND`-joined with `Cond.Where`                                                                                  |
| `GuardedBatch(checks, writes)`                      | one `TransactWriteItems` of `len(checks)+len(writes)` actions, `ClientRequestToken` fresh per call and reused across retries                                                     |
| `MaxBatchActions = 100`                             | the request's own bound; `ValidateBatch` still refuses above it                                                                                                                  |
| `CreateEntity(entity)`                              | `CreateTable` (`pk` HASH `S`, `sk` RANGE `S`, `PAY_PER_REQUEST`); `ResourceInUseException` means present; `DescribeTable` until `ACTIVE`                                         |

The subtree drop happens in the driver because the service rule leaves no
other place — "A FilterExpression does not allow key attributes" (API_Query,
above), and a key condition takes one sort-key term, so `#sk = :v OR
begins_with(#sk, :v/)` cannot be written. The sort key is always returned, so
the drop reads and projects nothing extra; the over-read is bounded by siblings
sharing the prefix (`a_b` reads `a_bb`), which `PrefixDoesNotWiden` proves are
dropped.

**Answers.** A `TransactionCanceledException` whose reasons contain any
`ConditionalCheckFailed` is `(false, nil)`. `TransactionConflict`,
`TransactionInProgressException`, throttling and 5xx are transient: the driver
retries the same token a bounded number of times, then returns the error. Every
other 4xx is an error as it is. The SDK's retryer handles what the reference
marks "OK to retry? Yes"; the driver adds only the cancellation-reason cases
the SDK does not retry — "If that request fails, AWS SDKs do not retry the
request" (transaction-apis, above).

**What `dynstore` does not have.** No `QueryInTx`, no `GuardedBatchInTx`, no
`*sql.Tx`: a remote server has no interactive transaction to hand a caller.
§7 names the six sites that use those today and what each becomes.

**The test `Factory`** mints a fresh owner prefix per call, `CreateTable`s
the entities it is asked for, and `DeleteTable`s them in `t.Cleanup`. The
suite runs unchanged; the SQL driver's twelve-line wiring
(`objstore/sqlstore/sqlstore_test.go:20-31`) is the size of this one.

## 6. The four protocols, and what each asks of the server

Every protocol is a package function over the two primitives
(`objstore/README.md`), so none is reimplemented; the question is what each
needs from a remote engine.

- **`Replace`** (`objstore/replace.go`). The claim is a `Put` with `ItemAbsent`
  or an `Update` conditioned on `version`; every batch under the claim carries
  a `Check` that `holder = token`; `finish` and `release` are `Update`s
  conditioned on `holder`. Each is one `ConditionExpression`; the fence is a
  `ConditionCheck` on an item the batch does not modify, which is what that
  action is for — "Applies a condition to an item that is not being modified
  by the transaction"
  (https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ConditionCheck.html).
  ExtendDB's `FOR UPDATE` on the marker serialises two claimants; nothing needs
  care. The cost is unchanged: 417 rows on krons are nine batches, and a
  replace that dies half-way stays held until the same work finishes it
  (`L267`).
- **`ReadConsistent`** (`objstore/consistent.go`). Two point reads of the
  marker around N queries; both must see committed state, which
  `ConsistentRead: true` gives on the service and PostgreSQL gives always. A
  page boundary inside one of the N queries is read-committed on both, which is
  exactly the interleave the second marker read detects.
- **`AddEdge` / `Reachable`** (`objstore/reach.go`). A walk of `begins_with`
  reads, then one batch: the marker's `version` compare-and-swap plus the edge
  `Put` with `ItemAbsent`. Two walkers closing a cycle from opposite ends read
  the same version; the second batch refuses on `#version = :was`. `L256`
  records this as resting on SQLite's single writer today; here it rests on
  the condition, which is where `6/40` put it.
- **`Increment`** (`objstore/protocol.go`). `Update` conditioned on the read
  value; a refusal re-reads. Needs read-your-writes for the re-read, hence
  `ConsistentRead`. The counter stays a string; nothing here uses `ADD` or
  `SET … + :n`, which the reference restricts to `Number`.

One thing changes in arizuko's favour. `store.RestoreInvite`
(`store/invites.go`) exists because an invite's counter lives in `onbod.db` and
its ACL grant in `routd.db`, "a separate DB so the two can't share a tx". Under
one server they can. This spec keeps the compensation — a batch stays inside
one owner prefix (§5) — and records the collapse as the follow-up `6/40`
§"The audited write" argues for.

## 7. The arizuko side: what is deleted, what replaces it

**Deleted, by name.** The list is the census of 2026-09-12; a name here does
not exist after the deletion release (§10 step 6).

| what                                                                                                                                                                                                                     | measured                                                                                                                                                                    | replaced by                                                                                                                              |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `objstore/sqlstore/` (one driver, two emitters)                                                                                                                                                                          | 5 files, 1,097 lines                                                                                                                                                        | `objstore/dynstore/`                                                                                                                     |
| `db_utils/` (`Open`, `Migrate`, `IsTransient`, `IsPostgres`, `TableExists`, `RequireDB`, `CreateDBFile`), `db_utils/dbtest`, `opener_test.go`, `sql_layer_test.go`                                                       | 27 non-test `db_utils.Open` sites; 22 long-lived pools fleet-wide; the layer guard covers 6 daemons and not `routd`, `onbod`, `store`, `resreg` (`sql_layer_test.go:61-68`) | one `dynamodb.Client` per daemon from `DYNAMODB_URL` and `AWS_*` env; the import guard of §9                                             |
| `routd/migrations/` and `store/migrations/`                                                                                                                                                                              | 47 + 88 `.sql` files; the two `db_utils.Migrate` callers (`routd/db.go:115`, `store/store.go:209`); the `migrations` table; `store/migrations_frozen_test.go`               | nothing — an entity has no DDL past `CreateTable`, which `CreateEntity` issues at boot                                                   |
| the owner files `routd.db`, `onbod.db`, `auth.db`, `runed.db` (`store/owners.go:22`), eight adapter files, `store/messages.db`, `seedOwnerDBs` in `cmd/arizuko/main.go`                                                  | 12 live files per instance                                                                                                                                                  | tables `<owner>.<entity>` in one database; `arizuko create` calls `CreateTable`                                                          |
| the sibling bind mounts (`compose/compose.go` `dataSubdirs` for `routd`, `onbod`, `runed`; `template/services/gitd.yml:12`, `slakd.yml:13`) and `DATABASE`, `DB_PATH`, `ROUTD_DB_PATH`, `ONBOD_DB_PATH`, `RUNED_DB_PATH` | six daemons open `routd.db`; dashd opens three files; runed mounts the whole data dir (`compose/compose.go:1008`)                                                           | `DYNAMODB_URL`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_CA_BUNDLE` per daemon; `groups/` and `web/` mounts stay |
| `cmd/arizuko/migrate-split`, `-hot`, `-routd`, `-onbod`, `-audit`, `-authstore`, `-chanlib`, `-emaid`, `-runed`, `-store`                                                                                                | 10 files, 17 `db_utils.Open` sites                                                                                                                                          | nothing in the binary — the copy is the operator's own script (below)                                                                    |
| `store/messages_search.go`, `messages_fts` and its three triggers                                                                                                                                                        | the one FTS5 index; the only `sqlite_master` read in production                                                                                                             | §8                                                                                                                                       |
| `modernc.org/sqlite` and `jackc/pgx` in `go.mod`                                                                                                                                                                         | `go.mod:32`, `go.mod:16`                                                                                                                                                    | `github.com/aws/aws-sdk-go-v2/service/dynamodb`                                                                                          |
| `dashd/dashstore/tasks.go:117` (`randomblob` id mint), `cmd/arizuko/token.go:172` (a column read of `signing_keys`)                                                                                                      | the last two hand-written statements outside the layer                                                                                                                      | `crypto/rand` in Go; `authstore`'s own read                                                                                              |

**The six interactive-transaction sites, each with its answer.** A remote
server offers no `*sql.Tx`, so these change SHAPE, not engine; they are the
part of the port that is design rather than substitution.

1. `resreg/resreg.go:690` — `invoke` opens `BeginTx` for every mutating REST
   and MCP call, hands it to the resource `Handler` as `x.Tx`, writes the audit
   row into it and commits. The contract changes: a `Handler` DESCRIBES its
   writes (`[]objstore.Check`, `[]objstore.Write`) and the engine runs one
   `GuardedBatch` with the audit row appended — the shape `6/40` §"The audited
   write" states and `audit.WriteFor` already offers. This is the largest
   single item in §11.
2. `store/audit_helpers.go:16` — `runAudited`, which carries `DeleteGroup`'s
   unbounded subtree purge. The purge becomes `Query` on the folder prefix
   plus guarded deletes in batches of 99 with the audit row in the last batch
   (`6/40` §4); a purge that dies half-way is re-run, which an idempotent
   delete permits.
3. `dashd/dashstore/tasks.go:94` and `:157` — two task writers holding a
   transaction around a read-then-write; each is one `GuardedBatch` whose
   condition is what the read decided.
4. `routd/prompt_db.go:27` — the same shape, one batch.
5. `store/scopes.go:341`, `:357` (`QueryInTx`) and `:245`, `:362`
   (`GuardedBatchInTx`), with `audit.TxSink` (`audit/audit_log.go:286`,
   `:373`) — the adapters that let a caller holding a transaction reach the
   store. They lose their last caller with items 1–4 and go.
6. `resreg/engine.go:333` — the column-backed row reader, live for every
   resource registered without an entity coordinate. Each declares its
   coordinate (`6/40` §"What a registered resource still needs") or, like
   `audit_log` and `signing_keys`, mounts its own `Handler`; the column path
   goes with the SQL that fed it.

**Who may reach which table.** Today a daemon reaches a file only if compose
mounted it, and that mount set is part of the isolation `SECURITY.md` §Model
rests on. A network server has no mount, so the equivalent is a credential per
daemon scoped to table ARNs: one ExtendDB account per instance, one IAM user
per daemon, one policy whose `Resource` is
`arn:aws:dynamodb:<region>:<account>:table/<owner>.*` for exactly the owners
that daemon mounts today — `routd` on `routd.*`; `dashd` on `routd.*`,
`onbod.*`, `runed.*`; `gitd` and `slakd` on their own prefix and `routd.*`; an
adapter that opens no owner file on its own prefix only. `arizuko create`
mints the account, users, policies and keys through the `/management` API and
writes each key into that daemon's `env/` file — one declaration, many readers
— and the certificate ExtendDB generates at init is exported into the instance
directory as every daemon's `AWS_CA_BUNDLE`. Narrowing those policies to "only
the owner writes" is the `5/16` federation debt and is not taken here.

**The compose fragment.** `template/services/extenddb.yml` declares
`extenddb` (`extenddb/extenddb-postgres`, `control_plane_delay_seconds 0`),
its bootstrap, and a `postgres:17-alpine` whose data directory sits under the
instance directory — unlike `litellm_db`'s named volume, because this IS
arizuko's state and backup owns it. Whether `litellm_db` folds into that
server is `6/37` §"One server, three owners", not this spec. Every daemon's
fragment gains the five env lines above and loses its `DB_PATH` mount.

**The copy is hand-driven, and the binary carries no migration.** Decision
(maintainer, 2026-09-13): an instance moves under an operator who watches it,
so the copy is a script that operator runs once and throws away, and
`cmd/arizuko`'s ten `migrate-*` subcommands go with the rest of the table
above rather than gaining an eleventh. A one-off belongs in the one-off file:
a migration subcommand is permanent surface, tested and documented forever,
for a run each instance makes once. The lever back is the previous image tag
plus the SQLite files, which stay on disk unread — and the retired binary,
which still reads them.

What the copy does, whoever writes it: read every entity table of every `.db`
file — all are `(part, sort, attrs)` on an instance that runs the object model
— and write each row as a `Put` in `TransactWriteItems` batches of 100 into
`<owner>.<entity>`. It is a copy, not a remodel: no key changes, no attribute
changes. `*_v1` (the pre-entity originals), `migrations`, `messages_fts*` and
`cost_log` are not copied; `cost_log` has no reader left since `6/39` gave the
gateway the ledger. Verification is mechanical: per table the row count and
the `Query` count agree, and every `chats` cursor reads back at the same
position (234 cursors on the three instances, `6/37` step 5).

**`make test` needs the server.** In-memory SQLite is what let `go test
./...` run with no service, and there is no in-memory ExtendDB the house may
use: its dev image is SQLite-backed, and a second backend is the drift the
conformance suite refuses. `make store` starts `extenddb/extenddb-postgres`
and `postgres:17-alpine` from a compose file under `tests/`, with the delay at
0 and one test key; the store tests take `DYNAMODB_URL` from the environment
and fail with one message naming `make store` when it is unset. CI runs the
same two images as services.

**Backups.** `arizuko archive export` is a logical export through resreg and
does not change. The file-level backup (`sqlite3 .backup` in `6/37`'s
rehearsals) becomes `pg_dump` of the instance's database, in `INSTALL.md`.

## 8. Ranked search without SQLite

`find_messages` (MCP, `ipc/ipc.go:2145`) and `GET /v1/messages/find`
(`routd/server.go:294`) are the two callers; no dashboard page reads it. The
verb is `FindMessages(query, scope, sender, since, limit)` with a snippet and
a BM25 rank (`routd/msgsearch`), tokenised on Unicode letters and digits with
diacritics dropped, and the ACL filter stays the caller's job.

**The index is an in-memory BM25 index inside routd, rebuilt from the store at
boot and fed by the writes routd already makes.** No SQL, no file, no
second engine: the messages entity is the store of record and the index is
derived from it. Sizing from `6/37`: 23,777 messages on krons, 8.4 MB of table
pages and 2.4 MB of FTS5 index, so the index is tens of megabytes.

- **Boot.** routd walks `chats` (the partition list, 136 items on krons) and
  `Query`s each partition with `Project` on `content`, `sender`, `routed_to`
  and the two flags — about 25 pages of 1 MB. Until the walk finishes,
  `find_messages` answers "index warming" as a refusal the caller can show,
  never an empty hit list.
- **Live.** routd is the sole appender, so the feed is the four writes on
  `routd.DB` that touch an indexed attribute: the append, the media rewrite
  (`EnrichMessage`), the observe stamp (`MarkMessagesObserved`) and the
  breaker's prune (`DeleteErroredMessages`). Each applies in-process after its
  own batch commits, so the record is written first and the index second. NOT
  the per-chat poll: `processGroupMessages` advances `chats.agent_cursor` past
  the rows it consumes, so a message ingested and dispatched between two ticks
  — which is every adapter ingest and every bot reply — never appears in a
  `pollOnce` result, and an index fed from there would be missing most of the
  corpus.
- **What it does not see.** An append, update or delete made by another
  process (`arizuko send`, an archive import, `DeleteGroup` from dashd or the
  CLI) is invisible until the next boot. A
  stale hit does not leak: the caller re-resolves every hit's chat to a folder
  before returning it (`ipc/ipc.go:2178-2186`), and a purged chat has no
  route. This is the discipline `6/37` §"The index tails, it is never told"
  asked of a separate daemon, kept inside the owner's process.
- **Tokeniser and ranking are hand-written, about three hundred lines.**
  Unicode letters and digits, lower-cased, diacritics removed by NFD
  decomposition and dropping the combining marks — `unicode61
remove_diacritics` in Go; BM25 with FTS5's constants; the snippet is the
  first window of 32 tokens around a hit with `«»` marks, which is what the
  callers render today. The query grammar is terms, `"phrases"` and `-` in front
  of either.
  `bleve` (Apache-2.0, `github.com/blevesearch/bleve/v2`, "tf-idf / bm25
  scoring models", "Search result match highlighting") is the fallback if the
  grammar ever has to match FTS5's, at the cost of a large dependency for one
  verb.

**Why not the alternatives.** PostgreSQL full-text search (`ts_rank_cd`,
`ts_headline`, `websearch_to_tsquery`;
https://www.postgresql.org/docs/17/textsearch-controls.html) would put SQL
back into one daemon against a database beside the server's — a second reader
of the message and a second schema to migrate, for a ranking the reference
itself calls "only examples". A separate daemon tailing the store over `Query`
sees inserts only, so it is no more exact than the in-process index and costs
a daemon. An external engine is a fourth service for one verb. **Reopen
trigger:** the index's rebuild time or RSS exceeds what §12 measures, or a
second consumer needs a change feed — ExtendDB serves `DynamoDBStreams`
(§3), so an exact tailing index outside routd is then a Streams consumer, not
a new mechanism.

This supersedes one sentence of `6/37` §"How `messages` leaves" — "Its index
stays FTS5 on a SQLite deployment" — because there is no SQLite deployment.

## 9. Acceptance

- `go test ./objstore/...` green with `dynstore` against ExtendDB on
  `postgres:17-alpine`, AND against `amazon/dynamodb-local`, AND with `-race`
  on the three concurrent bodies. The second run is what proves the driver is
  written to DynamoDB rather than to one server.
- The `store` and `onbod` suites re-run on Postgres AFTER §8 lands and BEFORE
  the driver swap, so that the classes §0 could not measure (`L256`, the pool
  read inside a transaction) are measured on the SQL driver first, where a
  failure is cheap to read.
- `go list -deps ./... | grep -E 'database/sql|modernc.org/sqlite|jackc/pgx'`
  is empty after the deletion release. This one line replaces both
  `opener_test.go` and `sql_layer_test.go`, and it sees a handle passed across
  a package boundary, which `L251` records the string guard cannot.
- `make test` on a fresh clone with only `make store` running: green, and its
  wall time is in the changelog (§12).
- krons: the copy verifies (counts equal per table; every `chats` cursor at
  the same position), `anteval --smoke` green on the new store, fourteen green
  days, and `compose/compose_test.go` asserts that no service mounts `store/routd`,
  `store/onbod` or `store/runed`.
- `find_messages` on krons: for twenty recorded queries, the top-ten hit set
  from the in-memory index equals FTS5's for exact-term queries, and the
  overlap for the rest is in the changelog.

## 10. Ordering, and the rule

krons runs the object model on SQLite entity tables today; marinade and sloth
do not. **An instance moves to the server only FROM the object model, never
from the column schema**, because the copy of §7 is mechanical only when
source and target share the item shape. So marinade and sloth first run the
entity migration krons has run (`arizuko migrate-hot` and its siblings, once
`L276` and `L277` are closed — both block an unattended run and are outside
this spec), then the copy.

`6/41` states the rule this spec inherits: "Two levers are never in one deploy
window." The copy's lever is the previous image tag plus untouched SQLite
files; `6/41`'s is `RUNED_SUBSTRATE=docker`. Neither ships in the other's
window, in either order.

1. **The gate on ExtendDB** (§3): compose it, run the suite, run the probes of
   §12 items 2, 5 and 6. A failure goes upstream; the window for an answer is
   this step's own length. **2–3 days.** If the gate fails and stays failed:
   `dynad` (§4), **12–16 days**, then this step again.
2. **`dynstore` and the conformance gate** (§5, §9 first line). **4–6 days.**
3. **Remove the interactive transactions and the column path** (§7 items 1–6),
   the two stray statements, and land §8. Both drivers pass; `make test` runs
   on either. **8–11 days.**
4. **Compose, keys, `arizuko create`, and the operator's copy script**,
   rehearsed on a krons snapshot; then krons: copy, flip, smoke. **5–6 days.**
5. **marinade and sloth**: entity migration, fourteen green days on krons,
   copy, flip. **2–3 days** of engineer time; calendar time longer.
6. **The deletion release**: the table of §7, the import guard,
   `ARCHITECTURE.md`, `INSTALL.md`, `SECURITY.md`, `README.md`,
   `objstore/README.md` and the comments in `objstore/` that describe a SQL
   driver, the web docs, the migration file and the `MIGRATION_VERSION` bump.
   Instances not yet moved stay on the pre-deletion tag. **3–5 days.**

## 11. Cost

| item                                                                                                    | engineer-days        |
| ------------------------------------------------------------------------------------------------------- | -------------------- |
| the gate on ExtendDB: compose, suite, the three probes                                                  | 3                    |
| `dynstore`: renderers, page walk, error mapping, `CreateEntity`, test factory                           | 3                    |
| conformance green on ExtendDB and on DynamoDB Local, `-race`                                            | 2                    |
| `resreg.invoke` contract redesign (handlers describe writes) and the column path                        | 5                    |
| `runAudited`/`DeleteGroup` purge, dashstore tasks, `prompt_db`, the InTx adapters, two stray statements | 4                    |
| §8: index, tokeniser, BM25, snippet, boot walk, poll hook, FTS5 removal, the 20-query comparison        | 4                    |
| compose, IAM minting, CA bundle, the copy script, rehearsal, krons cutover                              | 6                    |
| marinade and sloth (engineer time)                                                                      | 3                    |
| deletion release and docs                                                                               | 4                    |
| **total, adopting**                                                                                     | **34 (range 28–42)** |
| `dynad`, only if the gate fails: wire and SigV4 3, `expr/` 3, `pg/` 4, tests and oracle 3, packaging 1  | **+14 (12–16)**      |

The two items that can double are `resreg.invoke` (every registered
resource's handler changes contract) and §8 (if the hand-written tokeniser
cannot match FTS5's hit sets and bleve comes in). Not counted: closing
`L276`/`L277`, which the marinade and sloth moves need whatever engine follows.

## 12. Open questions — each with the measurement that settles it

1. **`make test` wall time** with ExtendDB and Postgres in containers, against
   today's in-memory SQLite. `time make test` on the deploy box, both ways;
   the number goes in the changelog. If it exceeds the house budget, the
   answer is test parallelism and table reuse inside one prefix, never a
   second storage implementation.
2. **Transaction latency.** p50 and p99 of a one-action and a 100-action
   `TransactWriteItems` on the krons host, and the wall time of
   `resreg.Apply`'s 417-row replace (nine batches). ExtendDB's per-item `FOR
UPDATE` is two statements per action; the InfoQ report of 300 ms p90 is
   under a write load arizuko does not have, and this is the number that says
   so or not.
3. **The classes §0 could not reach.** After §8 lands and before the driver
   swap: `store` and `onbod` on Postgres with the SQL driver, `-race`. Any
   failure that is not FTS5 is a Postgres-semantics defect this design has to
   name before krons moves.
4. **The index.** ANSWERED (2026-09-13, `routd/msgsearch/parity_test.go`):
   over a seeded corpus of 24,296 messages in 136 chats the walk takes 283 ms
   and the index holds 18.8 MB of heap, against the reopen trigger's one
   second and hundred megabytes; one search on the most common term of that
   corpus is 11.7 ms. The top ten is FTS5's top ten, exactly, for twelve
   queries spanning the frequency range — ten single terms and two pairs.
   Over the endpoint, a walk of 7 chats holding 40 messages plus one search is
   16.5 ms (`tests/dynamo/search_test.go`), which is the per-partition round
   trip krons' 136 chats multiply.
5. **Deadlock under ExtendDB.** Two batches locking items `(a, b)` and `(b,
a)` concurrently, a thousand times: the answer must be one commit and one
   refusal-then-commit on retry, never an error reaching the caller and never
   a lost write. If the abort surfaces as `InternalServerError`, the SDK's 5xx
   retry with the same token is the mechanism, and the test proves it.
6. **The planner and `COLLATE "C"`.** ExtendDB's key columns carry no
   collation and its comparisons do (§3); `EXPLAIN` on a `begins_with` query
   over the largest krons partition (10,688 messages) says whether the
   primary-key index serves it or the partition is scanned. Either answer is
   correct; only one is fast, and the fix is a column collation upstream.
7. **The real service.** Pointing the suite at Amazon DynamoDB is one
   `BaseEndpoint`; the shapes arizuko chose — one constant partition for
   `audit_log` (163,750 rows) and for `authd` — would meet "a maximum capacity
   of 3,000 read units per second and 1,000 write units per second" per
   partition
   (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/bp-partition-key-design.html).
   A capacity fact about a target nobody has chosen, recorded so that choosing
   it later starts from a measurement rather than a surprise.
8. **Whether `ItemAbsent` on a `Check` joins the surface.** `dynstore` renders
   it and ExtendDB honours it (§3); `ValidateBatch` admits it; no production
   caller asks for it. It gets a conformance body when one does.

## 13. What shipped, and what is left

**v0.68.0 is step 6, the deletion release, shipped 2026-09-14 with step 5
outstanding.** That is the order this section states is allowed: marinade and
sloth are not moved, and they stay on the pre-deletion tag.

Deleted as §7 names them: `objstore/sqlstore`, `db_utils` and `db_utils/dbtest`,
`store/migrations` and `routd/migrations`, the owner files and `seedOwnerDBs`,
the sibling bind mounts and `DATABASE`/`DB_PATH`/`ROUTD_DB_PATH`/`ONBOD_DB_PATH`,
`modernc.org/sqlite` in `go.mod`, and the six interactive-transaction sites —
including §7 item 6, the column-backed row reader, which took `Querier`,
`filterSQL`, `whereFilter`, `orderBy` and `scanTargets` with it.

**The import guard of §9, measured:** `go list -deps ./...` reaches
`database/sql` from nothing and `modernc.org/sqlite` from nothing. It still
shows `database/sql/driver` and `database/sql/internal`, reached by
`google/uuid` for its `driver.Valuer` — a third party's interface, not a pool
this repo opens.

**One thing §7 did not anticipate.** `arizuko create` seeded the default group
by opening routd, and a new instance has no endpoint to open until the store
server is up — which `INSTALL.md` cannot do before `create` makes the directory.
The split is by owner: `create` makes the instance's FILES and declares the
`--product` choice as `ARIZUKO_DEFAULT_PRODUCT`; routd's own boot writes the
`main` row and its default tasks beside the floors it already seeds
(`store.SeedDefaultGroup`, BUGS `L300`).

**Left: step 5.** marinade and sloth keep their owners in files and cannot run
this release — their daemons exit at boot naming the missing `DYNAMODB_URL`.
Moving them needs the entity migration first, and the `migrate-*` subcommands
are deleted, so it is the operator's own copy (§7).

## Ties

`6/37` (the object model this spec keeps, the one sentence it supersedes,
the census, and the Postgres seam this spec removes) · `6/40` (the surface,
the protocols, the audited write) · `6/41` (the two-lever rule) ·
`6/23` (the PostgreSQL service already in the deployment,
`template/services/litellm.yml`) · `5/16` (ownership, and the federation debt
the policies do not close) · `5/33` (the path is the coordinate) · BUGS `L97`,
`L122`, `L251`, `L256`, `L267`, `L276`, `L277`.
