---
status: shipped
shipped: 2026-09-05
depends: [../5/16-mcp-rest-unification, ../5/33-paths-roles]
---

# specs/6/38 — the storage seam: Postgres, and no seam

Decision: arizuko moves to Postgres and writes no `Store` interface.
`5/16` gives each daemon its own database to own and migrate; the seam it
does that through is Go's `database/sql`, and the
cloud target is managed Postgres, reached by a connection string. A
DynamoDB-shaped layer is justified only if the cloud target is
Postgres-incompatible, and that branch costs three security invariants a
redesign before any seam can be built — measured below.

The transaction census is the evidence for every claim that follows:
what the schema requires of an engine, which engines keep it, and what a
Dynamo-shaped batch can and cannot carry.

## Three adoptions want the same database

Postgres is not a cost this decision pays alone. Measured 2026-09-05:

- **LiteLLM** returns HTTP 500 "Database not connected" on `/spend/logs`
  without it; per-key budgets and virtual keys need it too (`6/23`).
- **Dex** states SQLite is "not appropriate for real workloads" and names
  Postgres, MySQL or etcd (`6/22` step 4).
- The cloud target wants it on its own.

So the marginal cost of the second and third adoption is a connection
string. A dependency three separate tools already require is shared
substrate, not a tax, and that is what makes "assemble existing tools"
affordable rather than expensive. Judge any future adoption's cost after
this move, not before it.

## What the transactions require (measured 2026-09-05)

Method: every `BeginTx`/`Begin` in tracked, non-test Go, the body read to
its `Commit`, helper calls resolved to the tables they touch.

- **47 sites; 26 touch two or more tables.** Two of the 26 are wrappers —
  `store.runAudited` (24 callers across 10 files) and the resreg mutate
  path (`resreg/resreg.go`, every mutating action of every registered
  resource commits its `audit_log` row in the same tx) — so by call path
  the multi-table share is well over half.
- Each multi-table transaction is a security invariant, not tidiness:

| Site                                 | Tables                           | What a partial write costs                               |
| ------------------------------------ | -------------------------------- | -------------------------------------------------------- |
| `dashd/groups_admin.go` delete group | groups, acl, routes              | a re-created folder inherits the last tenant's admin     |
| `store/invites.go` redeem            | invites, invite_redemptions, acl | grant lands, count does not → unlimited redemption       |
| `onbod/main.go` createWorldTx        | user_profiles, groups, acl       | TOCTOU: admin on someone else's world                    |
| `store/pairing.go`                   | route_tokens, acl_membership     | a paired identity with two parents                       |
| `routd/steer.go` `/always`           | pending_actions, acl, messages   | a standing skip-approval rule with no approval behind it |
| `routd/db.go` SetEngagementAudited   | chat_reply_state, audit_log      | a window that moved with no audit row, or the reverse    |

- Schema features the engine must keep, counted on the krons databases:
  5 foreign keys on 49 live tables, all `ON DELETE CASCADE`
  (`store/groups.go` deletes a group with no child cleanup because the
  database does it); 3 triggers (`messages_fts_ai/au/ad`, `routd/migrations/0001`,
  the only thing that keeps FTS5 in step with `messages`); 1 partial
  unique index (`idx_signing_keys_one_active`, `authd/migrations/0002` —
  what stops two concurrent rotations splitting the trust root);
  `AUTOINCREMENT` ids as the audit cursor (`audit/query.go`: id order IS
  time order); `json_extract` in predicates (`store/groups.go`) and
  `datetime()` normalisation (`store/tasks.go DueTasks`).
- `resreg.Apply` (`resreg/engine.go`) takes a write lock, recomputes a
  checksum over every row of a subsystem and swaps the lot in one tx.
- `acl` is 42 rows and `acl_membership` 58 on krons; `auth.Authorize`
  reads `policyRows()` live on every call, so revocation is immediate. No
  cache; anything that must remain true is read.

**What this excludes.** Any key-value or document store, and any
multi-master or CRDT replication (cr-sqlite, marmot): "grant lands, count
does not" is exactly the cross-row outcome a per-field merge produces. A
low foreign-key count is not evidence of a document design; the
invariants live in transactions, which a DDL audit cannot see.

**What this does not exclude.** Any engine that runs `BEGIN … COMMIT`
across tables — Postgres, and every SQLite engine in §"Why not a
replicated SQLite" except rqlite.

## The question is "managed Postgres, or not?"

The requirement, as stated by the maintainer (2026-09-05): an
organization-sized deployment, so Postgres suffices for scale; the
deployment must also be able to run on one cloud platform, through an
abstracted database whose provider is "Postgres or so". Scale therefore
justifies nothing below. What has to be justified is **portability
between self-hosted and one cloud**, and that has exactly two branches.

**Branch A — the cloud target is managed Postgres** (RDS, Aurora, Cloud
SQL, AlloyDB, Azure Database for PostgreSQL, Neon, Supabase). The
application sees the same wire protocol it sees self-hosted; the provider
swap is a connection string. No seam. The leaks that reach application
code are small and enumerable — §"What managed Postgres leaks" below.

**Branch B — the cloud target is Postgres-incompatible** (DynamoDB,
Firestore, Spanner's native API). This is a decision to give up SQL, and
its price is measured below in transactions: three security-bearing
transactions do not fit DynamoDB's transaction model as written and must
be redesigned first. A seam earns its place only on this branch, and
only after that redesign.

**Recommendation: Branch A, and no seam.** Move the whole of arizuko to
Postgres; make the cloud target managed Postgres; write no `Store`
interface. It is the cheapest answer and it keeps every invariant above
as a database guarantee. Branch B stays available — its cost is written
down here so that choosing it later is a decision, not a discovery.

## Why a `Store` interface is the wrong seam on Branch A

A storage interface abstracts over engines that differ in **data model**.
Managed Postgres and self-hosted Postgres do not differ in data model;
they differ in DSN. That difference already has one place: eight
`sql.Open("sqlite", …)` sites (`db_utils.OpenWAL`, `store/store.go`,
`authd/main.go`, `dashd/main.go`, two under `cmd/arizuko`, two under
`tests/`), each carrying its own pragma string. The move to Postgres
funnels those through one `db_utils` opener that takes a DSN — "one
declaration, many readers", not a second format. `database/sql` is the
seam, and it is in the standard library.

A DynamoDB-shaped façade whose only implementation is Postgres is pure
indirection with a self-imposed feature ban: every call site pays the
DynamoDB price — no predicate deletes, no joins, no snapshot reads, no
unbounded atomic writes, FTS and cascades and the partial unique index
reimplemented as call-site discipline — while the engine underneath could
do all of it. The façade's one benefit, that a later switch to real
DynamoDB touches no call site, is paid up front on every call site
whether or not the switch happens. If Branch B is chosen, build the
façade then, against the redesigned transactions; if it is not, the
façade is the rewrite the maintainer does not want.

## Branch B, measured: what DynamoDB's transactions can and cannot carry

`TransactWriteItems` (docs.aws.amazon.com, developerguide
`transaction-apis.html` and APIReference `API_TransactWriteItems.html`):
up to 100 actions — `Put`, `Update`, `Delete`, `ConditionCheck` — across
tables in one account and region, all-or-nothing, aggregate 4 MB, **no two
actions on the same item**, a condition may reference only the item it is
on, and `ConditionCheck` only an item the transaction does not modify.
Reads are a separate `TransactGetItems` (100 items). There is no
interactive transaction: nothing reads, decides and writes under one
lock. `Query` and `Scan` are read-committed against a transaction, not
serializable. `ClientRequestToken` gives 10-minute idempotency.

So DynamoDB is not key-value: it carries a bounded, conditional,
cross-table atomic batch. The 26 multi-table transactions classify
against that as follows.

**Fit as a bounded conditional batch — 19 sites.** The writes are known
before the transaction starts, or the one decision is a condition on the
item being written: `authd` link identity (2 upserts); create group + seed
role, in `dashd`, `onbod` and `store` (`INSERT OR IGNORE … RowsAffected==0`
becomes `Put` with `attribute_not_exists`, which fails closed the same
way); `emaid` thread + message id; engagement upsert + audit; message +
proactive upsert; the three route writes, two secret writes, the secret
use log and revoke-acl, each with its audit row; and the 24 `runAudited`
callers, none of which reads inside the transaction. Two sites need the
read-then-condition pattern: invite redemption reads the invite outside
the transaction and conditions the `Update` on `used_count` and `active`
(`store/invites.go`); `/always` reads the held action outside and
conditions the `Update` on `status` (`routd/steer.go`, `resolveHoldTx`).
Both stay correct because the condition is on the row the decision came
from.

**Do not fit — 7 sites, 3 of them security-bearing.**

| Site                                                                 | Why the batch model cannot carry it                                                                                                                                                                                                                                 | What it guards                                       |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| group delete (`dashd/groups_admin.go`, `store/groups.go`)            | four predicate deletes over a subtree (`groupfolder.SQLSubtree` on `groups`, `acl`, `routes`); DynamoDB needs the keys, so the set is read outside the transaction and a row added in between survives; a subtree over 100 rows cannot be deleted atomically at all | a re-created folder inherits the last tenant's admin |
| pairing + membership (`store/pairing.go`, `putMembershipTx`)         | a recursive ancestor walk (`WITH RECURSIVE up`) decides the insert; the walk is an unbounded read under the same lock                                                                                                                                               | a cycle, or a paired identity with two parents       |
| `resreg.Apply` (`resreg/engine.go`)                                  | a checksum over every row of a subsystem, then delete-all/insert-all of unbounded size, then a second checksum, in one critical section                                                                                                                             | a package install that is transactional (BUGS `L44`) |
| `ExportSnapshot` (`resreg/archive.go`)                               | a consistent read of a whole subsystem; `TransactGetItems` caps at 100 items and `Scan` is read-committed                                                                                                                                                           | an export whose checksum means something             |
| message import (`resreg/archive.go`)                                 | unbounded `INSERT OR IGNORE`                                                                                                                                                                                                                                        | nothing security-bearing; atomicity of an import     |
| two `*_legacy` backfills (`store/invites.go`, `store/onboarding.go`) | one-off table-to-table copies                                                                                                                                                                                                                                       | nothing after the move                               |

The three that matter each have a known DynamoDB redesign, and each is a
change to how every writer behaves, not a storage detail: group delete
needs a per-world version item that every `acl`/`routes`/`groups` writer
bumps, so the delete can `ConditionCheck` it — and still fails past 100
rows; membership needs a materialised ancestor closure per node,
maintained on every membership write; `Apply` needs a lock item and a
sequence of ≤100-item batches, which gives up the pre-image rollback
`resreg.Apply` already carries (`L44`). That is Branch B's price. It is a redesign of
three invariants, and it is what a seam would have to be built against.

## What managed Postgres leaks into application code

Measured against the providers' own documentation; every item is small,
and every one is a change the self-hosted deployment needs as well.

- **Connection limits and poolers.** `max_connections` on a managed
  instance follows the instance class; arizuko opens one `database/sql`
  pool per daemon and sets `SetMaxOpenConns` at one site in the tree.
  Seventeen Go daemons with unbounded pools is the first thing a managed
  instance rejects. Every opener sets a cap; a transaction-mode pooler
  (PgBouncer, RDS Proxy, Azure's built-in PgBouncer) additionally
  forbids `SET`/`RESET`, `LISTEN`, session advisory locks and
  `PREPARE`/`DEALLOCATE` (pgbouncer.org/features.html), so `pgx` runs
  with the simple protocol or statement caching off. arizuko uses none
  of the forbidden four today.
- **No superuser, an extension allowlist.** RDS, Cloud SQL and Azure
  each install only extensions on their list (`rds.allowed_extensions`;
  Cloud SQL's extension list; Azure's `azure.extensions` parameter). The
  move needs no extension: `tsvector`, `jsonb`, partial indexes,
  PL/pgSQL triggers and identity columns are core.
- **Failover drops connections mid-transaction.** A managed failover
  re-points the endpoint; in-flight transactions fail with a connection
  error and `database/sql` reconnects. The retry rule (`CLAUDE.md`:
  transient errors only — today "DB busy/locked") gains Postgres's
  transient class: `40001` serialization failure, `40P01` deadlock,
  connection reset. Deadlock is new — SQLite has one writer, so two
  multi-table transactions could never wait on each other; group delete
  against a concurrent grant now can, and the retry wrapper is the fix.
- **`LISTEN/NOTIFY` and logical replication** are supported by every
  managed provider on a direct connection, and `LISTEN` is not available
  through a transaction-mode pooler. arizuko uses neither; routd polls.
- **TLS on by default** on Azure and Cloud SQL public IP; `sslmode` is a
  DSN field.

None of these is an abstraction; each is a DSN field, a pool cap, or a
retry class. That is the whole provider swap.

## The move to Postgres, costed (both branches)

**Ownership.** Today each daemon owns a file, and compose binds one
`store/<owner>/` directory per container, so ownership is a mount
boundary (`5/16` step 7). On Postgres, ownership is one database and one
role per owner daemon on one server (`routd`, `runed`, `authd`, `onbod`,
one per adapter that keeps a private store), with `GRANT` limited to the
owner's database. The boundary stays server-enforced, not a convention.
The FS-mounted write path — dashd, onbod and the CLI opening routd's file
directly (the "split write-discipline") — becomes "connect with routd's
role", which is the same discipline under a different enforcer; the
non-mounted daemons keep writing through the owner's HTTP API.

**Dialect.** 598 `database/sql` call sites in 106 files. Countable
SQLite-isms: 57 `INSERT OR IGNORE` (→ `ON CONFLICT DO NOTHING`, which
ignores only unique conflicts where SQLite ignored every constraint), 41
`ON CONFLICT` (compatible), 12 `json_extract` (→ `jsonb` operators), 12
`rowid`, 10 `datetime(` (→ `timestamptz`), 6 `AUTOINCREMENT`, 6 FTS
`MATCH` (→ `tsvector`/`tsquery` with a GIN index and a generated column,
no triggers), 5 `PRAGMA`, 3 `sqlite_master` probes, `?` placeholders
throughout (→ `$n`, or a rewriting opener), 3 triggers to PL/pgSQL. `LIKE`
becomes case-sensitive, which is the behaviour `L122` wants.

**Three assumptions that are SQLite's single writer, not SQL.** These
are the real port, and none is a dialect substitution:

1. `resreg.Apply` upgrades to a write lock with `UPDATE audit_log SET id
= id WHERE 1 = 0` — a RESERVED lock in SQLite, a no-op in Postgres
   (zero rows, no lock). It becomes `pg_advisory_xact_lock` or `SELECT …
FOR UPDATE` on a lock row, or the checksum recheck races.
2. `audit/query.go` pages on `id` because "id order IS time order". A
   Postgres sequence assigns ids at insert and commits in any order, so
   a cursor on `id` can skip a row that commits late with a smaller id.
   The cursor pages on `(created_at, id)` with `created_at` set by the
   database, or accepts the skew.
3. Foreign keys are enforced everywhere. SQLite enforces them only where
   the DSN says `foreign_keys(on)`; Postgres always does. Any writer
   whose correctness rests on that pragma being absent fails loudly, which
   is the correct outcome and a migration-day surprise.

**Migrations.** 151 SQL files in seven embedded sets, 27 of them
carrying SQLite DDL. They do not port: each owner gets a squashed
Postgres baseline `0001`, and an existing instance converts once with
`pgloader` (SQLite source, PostgreSQL target; PostgreSQL License) — an
off-the-shelf tool, not a migration of migrations. `db_utils.Migrate`
and its `migrations` table are dialect-neutral and stay.

**Tests.** 598 call sites are tested against `modernc.org/sqlite`
in-process. Postgres tests need a server: `testcontainers-go` or
`fergusstrange/embedded-postgres` (MIT), one server per `go test` run
with a database per test. The `make test` under-5-seconds rule pays a
few seconds of startup. A dual-dialect period is forbidden — two SQL
strings for one statement is the drift this repo bans — so the move is
a cutover, one release, all owners.

**What an operator gains.** Concurrent writers (SQLite serialises every
write per file); managed backup, point-in-time restore and failover on
the cloud branch, `pgBackRest` or `pg_dump` self-hosted; daemons that
can run on more than one host as far as the database is concerned —
though `groups/`, `ipc/` sockets and `docker.sock` still bind runed and
routd to one host, so the move alone does not make arizuko multi-host;
row-level security as a second folder-containment gate; ranked full-text
search; `pg_stat_statements`.

**What an operator pays.** One more service in compose (`postgres`, a
volume, ~100 MB resident plus per-connection memory, hence the pool
caps); a backup path where a tar of the data dir is not enough — the tar
must include a `pg_dump`, or stop the server first; major-version upgrades
(`pg_upgrade`); one DSN per daemon in the generated `env/<daemon>.env`;
and a single point of failure where today each daemon's file fails
alone. Litestream, the SQLite replication tool, has no role once the engine
is Postgres.

## Why not a replicated SQLite instead

The alternative to Postgres for "more than one box" is an engine that
keeps SQLite's file. Measured against each engine's docs or source
(rqlite.io/docs/api/api · github.com/rqlite/gorqlite `stdlib/sql.go` ·
canonical.com/dqlite/docs, github.com/canonical/go-dqlite/releases ·
github.com/superfly/litefs `docs/ARCHITECTURE.md`, fly.io/docs/litefs ·
github.com/benbjohnson/litestream/releases · docs.turso.tech/sdk/go and
`libsql/internal/http/hranaV2/hranaV2.go`):

| Engine                        | Keeps `BEGIN…COMMIT`                                                                                                           | Cost                                                                                                          | Latest                                                     | License               |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- | --------------------- |
| Litestream                    | yes — ships WAL frames after commit                                                                                            | backup and restore only; one sidecar and a bucket                                                             | v0.5.17, 2026-08-31                                        | Apache-2.0            |
| libSQL (`sqld` / Turso Cloud) | yes — `libsql-client-go` sends `BEGIN`/`COMMIT` over Hrana; rejects `ReadOnly` (one site, `resreg.ExportSnapshot`)             | a driver swap and a remote primary; a SQLite fork, so every feature above survives                            | core 0.10.0-pre.4, 2026-06-02; server v0.24.33, 2025-12-19 | MIT                   |
| dqlite + go-dqlite            | yes — a real `database/sql/driver`, Raft-replicated                                                                            | CGO with libdqlite, raft, libuv; `modernc` unusable; `ATTACH` disabled; `json1` has an open issue             | dqlite 1.18.7, 2024-07; go-dqlite v3.0.4, 2025-02          | LGPL-3.0 / Apache-2.0 |
| LiteFS                        | yes — replicates the file; one writer, writes forwarded                                                                        | FUSE, a `litefs` per node, a Consul lease; Fly.io states it cannot support it                                 | v0.5.14, 2024-04-22                                        | Apache-2.0            |
| rqlite                        | **no** — literal `BEGIN`/`COMMIT` is undefined by its docs; `gorqlite/stdlib` implements `Begin`/`Commit`/`Rollback` as no-ops | every one of the 26 transactions silently stops being atomic                                                  | v10.3.1, 2026-09-04                                        | MIT                   |
| cr-sqlite, marmot             | local only — CRDT merge / eventual, out-of-order rows                                                                          | the multi-master shape the invariants forbid                                                                  | cr-sqlite v0.16.3, 2025-01; marmot untagged (README)       | MIT                   |
| SQLite session extension      | n/a — captures committed changesets                                                                                            | compiled into `modernc.org/sqlite` v1.46.1 (`SQLITE_ENABLE_SESSION=1`), not wrapped in Go; transport is yours | inside SQLite                                              | public domain         |
| Turso (Rust rewrite)          | its own MVCC engine                                                                                                            | pre-1.0; its repository says not production ready (README); FTS is experimental, not FTS5                     | v0.7.2, 2026-07-30                                         | MIT                   |

Only libSQL keeps every call site and adds a remote database, and it
does so as a SQLite fork with a slower server release line than its
library. It is the right answer to "SQLite, but not a local file"; it is
not the answer to the stated requirement, which names Postgres.

## Partition keys are per-table — an access-pattern note, not a design

This states how the tables are read, which any engine's indexes should
follow. 19 of routd's 29 tables have no `folder` column.
`messages` keys on `chat_jid`, `secrets` on `(scope_kind, scope_id)`,
`audit_log` on `folder` with `""` for instance-scope rows, `acl` on a
**world**: `container/runner.go worldOf` returns the top path segment,
and `""` for an elevated turn. A scope whose first segment is a literal
(`acme/**`) belongs to `acme`; a scope starting with a wildcard (`**`,
`*/eng`) belongs to `""`. An authorization read for folder F reads those
two partitions, never a scan. `5/5` R2 already settled that a world is a
folder-shape predicate rather than a table.

## Ties

`5/16` per-daemon DB ownership (this is its storage half; ownership
becomes a role per owner) · `5/33` grants and
scope globs · `5/5` R2 (a world is a predicate) · `7/22` (argues from the
invariants; its conclusion stands with no seam) · `6/16` (the
ship-in-parts matrix) · BUGS `L44` (the transactional install Branch B
would give up), `L122` (`LIKE` case-sensitivity, which Postgres fixes).
