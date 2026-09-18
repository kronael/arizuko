---
status: partial
depends:
  [
    22-proxyd-cut,
    23-llm-gateway,
    36-minimal-projection,
    ../5/16-mcp-rest-unification,
    ../5/33-paths-roles,
  ]
---

# specs/6/37 — the store model: an object store behind an interface

Decision (maintainer): **arizuko's storage is an object store behind an
interface.** A daemon addresses objects by key and holds no SQL; SQL lives in
exactly one place, the interface's driver, so a second engine is a second
driver and changes no daemon.

The interface is costed against the engine it serves. Against a POSTGRES
target it buys nothing, because Postgres is already `database/sql`, and the
verdict there is "Postgres and no `Store` interface" — §"The seam is
`database/sql`, and it is one opener" keeps that argument, and it is correct
on that branch. Against an object store the interface is not an indirection
over the engine, it IS the engine's shape. The measurements in §"The census a
scale-out model is judged against" — the transaction census, what a bounded
conditional batch can carry, the three redesigns — are what make the choice
decidable, and they hold whichever way it goes.

**The interface must be totally encapsulating, and that is a proof
obligation, not a claim.** It has to serve SQLite, Postgres and DynamoDB
without any of them showing through. The proof is one conformance suite every
driver passes — the same shape `db_utils`'s migration suite already uses to run
each test body once per engine. A driver that cannot pass it is not a driver,
and an operation that cannot be expressed for all three does not belong on the
interface (`6/40` names five, and three of them are the hard cases).

**It needs no new engine to start.** The vendored driver is SQLite 3.51.2 with
`jsonb()`, measured on the deployment running today, so the first driver is
built over the engine already there. Nothing crosses a point of no return to
begin, and no live data moves.

**State.** Steps 1 to 6 of the order below are done or in progress, and the
seam is built: `db_utils.Open` selects the engine from the DSN it is given,
every production site calls it, and `db_utils/opener_test.go` fails the build
on any other opener. The cap is 4 per Postgres pool — 20 pools exist in a full
deployment and the budget is set for 24, so 96 against the 97 a stock server
leaves, which is the number an operator sharing that server with Dex and
LiteLLM must raise. `db_utils.IsTransient` classifies `40001`, `40P01` and the
SQLite lock codes; it has no caller yet, because retrying a transaction means
re-running the caller's closure at 47 sites, and that is step 7. The migration
runner speaks both dialects, with `pg_advisory_xact_lock` as the Postgres
critical section — transaction-scoped, so it survives a transaction-mode
pooler, and it is the working precedent redesign 3 copies.

Every daemon still opens SQLite. What stands between here and step 7 is
measured rather than estimated: 30 migration files are SQLite-only (`L228`,
two of them FTS5 and therefore step 8's), and 40 `sql.Open` sites across 26
test files bypass the seam, so the suite cannot yet be pointed at another
engine (`L227`).

The engine is a commitment; the provider is not. The seam that keeps the
second true is below, and it is not a `Store` interface.

## One server, three owners

**Dex** documents five backends — etcd, Kubernetes CRDs, SQLite3,
Postgres, MySQL — and says of the one arizuko would share: "SQLite3 is
the recommended storage for users who want to stand up dex quickly. It
is not appropriate for real workloads"
(dexidp.io/docs/configuration/storage/, read 2026-09-05;
[`22`](22-proxyd-cut.md) step 4 measures the trade).

**LiteLLM** fixes its engine in code, not in prose: `schema.prisma`
declares `datasource client { provider = "postgresql" }`, and Prisma
binds its query engine to that provider at generate time. Its production
page says "Currently, PostgreSQL is our primary supported database for
production deployments" and "We explored MySQL but that was hard to
maintain and led to bugs for customers" (docs.litellm.ai/docs/proxy/prod).
Virtual keys, spend logs, budgets and team management all need that
database (docs.litellm.ai/docs/proxy/db_info; without it `/spend/logs`
answers HTTP 500 "Database not connected", measured 2026-09-05) —
[`23`](23-llm-gateway.md) chose it for exactly those.

Both tools need the server whatever arizuko does, so the server is
substrate and the third owner is free. The split:

- One `postgres` service in the deployment. Each owner holds its own
  database on it and knows only its own DSN: the Dex package of `22`,
  the gateway package of `23`, and arizuko's daemons.
- arizuko's daemons carry a Postgres DSN and open it through the one
  `db_utils` opener below. They read LiteLLM's spend and Dex's identity
  over those tools' HTTP faces, never their tables, and neither tool
  opens arizuko's.
- [`5/16`](../5/16-mcp-rest-unification.md)'s rule holds across all
  three: a store belongs to the daemon that migrates it. Dex and LiteLLM
  migrate theirs; each arizuko daemon migrates its own.

## What the store is (measured 2026-09-05, krons)

| file                          | tables                                                                                           | rows                                                                                                                                                                                                                                                                    | bytes   |
| ----------------------------- | ------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `store/routd/routd.db`        | 29, plus one FTS virtual table and 3 triggers                                                    | `audit_log` 159,309 · `messages` 23,777 · `cost_log` 3,452 · `task_run_logs` 1,407 · `sessions` 248 · `scheduled_tasks` 66 · `acl_membership` 58 · `routes` 58 · `web_routes` 54 · `groups` 53 · `route_tokens` 43 · `acl` 42 · `proxyd_routes` 25 · `network_rules` 15 | 58.7 MB |
| `store/runed/runed.db`        | 5: `spawns`, `session_log`, `circuit_breaker`, `audit_log`, `migrations`                         |                                                                                                                                                                                                                                                                         | 4.0 MB  |
| `store/authd/auth.db`         | 6: `signing_keys`, `auth_users`, `oauth_identities`, `refresh_tokens`, `audit_log`, `migrations` |                                                                                                                                                                                                                                                                         | 164 KB  |
| `store/onbod/onbod.db`        | 6: `onboarding`, `onboarding_gates`, `invites`, `invite_redemptions`, `audit_log`, `migrations`  |                                                                                                                                                                                                                                                                         | 120 KB  |
| `store/teled/teled.db`        | `outbound_dedup`, `inbound_pending` (`chanlib/migrations`)                                       |                                                                                                                                                                                                                                                                         | 45 KB   |
| `store/whapd/outbound-dedup/` | one file per key, no database (`whapd/src/dedup.ts:6`)                                           |                                                                                                                                                                                                                                                                         |         |

Where the bytes are (`dbstat`, routd.db): `audit_log` 29.0 MB of table
pages plus 11.6 MB in its three indexes; `messages` 8.4 MB plus 2.4 MB
of FTS and 3.8 MB of indexes; `turn_context` 0.6 MB; `turn_results`
0.5 MB; `cost_log` 0.4 MB. Every other table is at most 20 KB of pages:
the whole control plane — `groups`, `acl`, `acl_membership`, `routes`,
`scheduled_tasks`, `network_rules`, `web_routes`, `proxyd_routes`,
`route_tokens`, `secrets`, `installed_packages` — is under 0.4 % of the
file, and `arizuko export krons` renders it, both subsystems, as 77,023
bytes of YAML (2,351 lines). Write rates: `audit_log` ≈ 1,560 rows/day
(159,309 rows over 102 days), `messages` ≈ 140/day, `cost_log` ≈ 30/day.

What the engine carries: 7 tables declare a `REFERENCES` clause across
the four files; 3 triggers keep `messages_fts` in step; 1 partial unique
index holds the trust root (`authd/migrations/0002`). Foreign-key
enforcement rides the DSN (`db_utils/db_utils.go:52-58`), and the live
routd.db carries 10 orphan `task_run_logs` rows
(`pragma_foreign_key_check`): the invariants are in the transactions,
not in the DDL. 47 `Begin`/`BeginTx` sites in 24
non-test files; `store.runAudited` has 24 callers. Backup is `arizuko
archive export [--quiesced]` (`cmd/arizuko/archive.go:87`), a logical
export of config, secrets, messages and group trees, not a file copy.

## What pins arizuko to one host — not the engine

Six daemons and the CLI open `routd.db` through a bind mount: dashd
(`compose/compose.go:1140`, which also mounts onbod's and runed's
files), onbod (`:1113`), proxyd (`:1189`), webd (`:1226`), gitd and slakd
(`template/services/gitd.yml:12`, `slakd.yml:13`). routd opens no
sibling file (`routd/sibling_db.go:13`): authd and runed are federated
over HTTP, and the non-mounted daemons write through the owner's `/v1`
(`CLAUDE.md` §"Split write-discipline"). Beside the store: `groups/`
(routd, runed, onbod, dashd), `ipc/` (routd, runed) and `docker.sock`
(runed, `compose/compose.go:1062`).

So a second host for the execution plane is runed with its own docker,
`groups/` and `ipc/`, talking to routd over the HTTP it already uses —
no store change. What the store blocks is a second host for any of the
six file-mounted readers, and `5/16` federates each of those over `/v1`
(dashd's audit page already reads the owners that way,
`dashd/audit_page.go:9`, `:176`). The engine is the
last pin to move, not the first; a key-value store moves nothing until
the six readers and the filesystem state have moved.

## The census a scale-out model is judged against

Measured 2026-09-05: every `BeginTx`/`Begin` in tracked, non-test Go, the
body read to its `Commit`, helper calls resolved to the tables they touch.
The census is the evidence for every claim that follows: what the schema
requires of an engine, which engines keep it, and what a Dynamo-shaped batch
can and cannot carry. It is the pre-port count the redesigns below are
costed against; [`6/40`](40-store-surface.md) §"`Merge`" re-measures the
transaction sites as 20 on 2026-09-12, after the port.

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

**What this excludes.** Any engine without a cross-item atomic write, and
any multi-master or CRDT replication (cr-sqlite, marmot): "grant lands,
count does not" is exactly the cross-row outcome a per-field merge
produces. A low foreign-key count is not evidence of a document design; the
invariants live in transactions, which a DDL audit cannot see.

**What this does not exclude.** Any engine that runs `BEGIN … COMMIT`
across tables, and — once the three redesigns below have landed — a bounded
conditional batch.

### The question is "managed Postgres, or not?"

The requirement, as stated by the maintainer (2026-09-05): an
organization-sized deployment, so Postgres suffices for scale; the
deployment must also be able to run on one cloud platform, through an
abstracted database whose provider is "Postgres or so". Scale therefore
justifies nothing here. What has to be justified is **portability
between self-hosted and one cloud**, and that has exactly two branches.

**Branch A — the cloud target is managed Postgres** (RDS, Aurora, Cloud
SQL, AlloyDB, Azure Database for PostgreSQL, Neon, Supabase). The
application sees the same wire protocol it sees self-hosted; the provider
swap is a connection string, and the one opener is the whole seam
(§"The seam is `database/sql`, and it is one opener"). The leaks that reach
application code are small and enumerable — §"What managed Postgres leaks"
below.

**Branch B — the cloud target is Postgres-incompatible** (DynamoDB,
Firestore, Spanner's native API). This is a decision to give up SQL, and
its price is measured below in transactions: three security-bearing
transactions do not fit DynamoDB's transaction model as written and must
be redesigned first. Branch B is costed rather than dismissed, so that
choosing it is a decision, not a discovery; the three redesigns are what a
second driver owes.

### Branch B, measured: what DynamoDB's transactions can and cannot carry

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
cross-table atomic batch. That bound is why `GuardedBatch` caps at 100
actions with no two on one item, and why the versioned replace exists for
the sites that do not fit. The 26 multi-table transactions classify
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
`resreg.Apply` already carries (`L44`). That is Branch B's price. It is a
redesign of three invariants, and it is what a second driver owes. The
three, re-verified in the code:

| write path     | where                                                                                                                                                                                                                                                | the transaction                                                                                                                                                                            | what it guarantees                                                                                     | the escalation it prevents                                                                                                                                                                                                                                                                      |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| group delete   | `dashd/groups_admin.go:447-491`; `store/groups.go:112-135`                                                                                                                                                                                           | dashd: `groups` exact and `/`-subtree, `acl` `/`-subtree, `routes` `#`- and `/`-subtree, the audit row. store: `groups` exact, `routes` `#`-subtree, `chat_reply_state`, `scheduled_tasks` | no row that confers on or routes into the deleted subtree survives                                     | a re-created folder inherits the last tenant's admin rows and routes; stale tasks fire into it (BUGS `L66`/`L67`). The two paths each purge a different half today — BUGS `L198`                                                                                                                |
| membership     | `store/membership.go:145` `putMembershipTx`, inside `RedeemPairing` (`store/pairing.go:101-142`) with the single-use token claim; the same walk copied at `store/membership.go:36`, `resreg/resources/membership.go:88`, `routd/acl_resource.go:361` | a `WITH RECURSIVE` walk from the parent up; refuse if it reaches the child; insert the edge                                                                                                | the graph is acyclic and one pairing token yields at most one edge                                     | a cycle makes every grant on it reachable from its lowest member — `policyRows` (`auth/authorize.go:266`) reads rows for the whole ancestor set that `expandPrincipals` (`:288`) builds from `store.Ancestors` (`store/membership.go:197`); a double redemption pairs one device to two parents |
| `resreg.Apply` | `resreg/engine.go:789`; the lock idiom `:815`, the checksum `:818`, per-resource `DeleteScope`/`DeleteAll` then `InsertAll`, a second checksum, the audit row, commit                                                                                | recheck the subsystem checksum under a write lock; delete and re-insert every mentioned resource; recompute                                                                                | the subsystem moves from checksum X to Y with nothing observable between, and only if it is still at X | a manifest exported before a revocation re-applies the revoked row (the resurrection `resreg/resources/membership.go:78` guards); a half-applied manifest leaves old grants beside new groups                                                                                                   |

Two of the 19 spot-checked: invite redemption is a conditional increment
(`UPDATE invites SET used_count = used_count + 1 WHERE … AND used_count <
max_uses`, `store/invites.go:279-281`); `/always` claims the held action
with `UPDATE pending_actions SET status = ? WHERE id = ? AND status = ?`
and a `RowsAffected == 1` check (`routd/pending_actions.go:429`, under
the tx at `routd/steer.go:582-597`). Both are conditions on the item
written, so both survive a batch model as written. The other 17 stand on
the classification above.

### What managed Postgres leaks into application code

Measured against the providers' own documentation; every item is small,
and every one is a change the self-hosted deployment needs as well.

- **Connection limits and poolers.** `max_connections` on a managed
  instance follows the instance class, and seventeen Go daemons with
  unbounded pools is the first thing a managed instance rejects — hence
  the cap the opener sets on every Postgres pool. A transaction-mode
  pooler (PgBouncer, RDS Proxy, Azure's built-in PgBouncer) additionally
  forbids `SET`/`RESET`, `LISTEN`, session advisory locks and
  `PREPARE`/`DEALLOCATE` (pgbouncer.org/features.html), so `pgx` runs
  with the simple protocol or statement caching off. arizuko uses none
  of the forbidden four; the advisory lock it does take is
  transaction-scoped.
- **No superuser, an extension allowlist.** RDS, Cloud SQL and Azure
  each install only extensions on their list (`rds.allowed_extensions`;
  Cloud SQL's extension list; Azure's `azure.extensions` parameter). The
  move needs no extension: `tsvector`, `jsonb`, partial indexes,
  PL/pgSQL triggers and identity columns are core.
- **Failover drops connections mid-transaction.** A managed failover
  re-points the endpoint; in-flight transactions fail with a connection
  error and `database/sql` reconnects. The retry rule (`CLAUDE.md`:
  transient errors only) covers Postgres's transient class beside the
  SQLite lock codes: `40001` serialization failure, `40P01` deadlock,
  connection reset — `db_utils.IsTransient`. Deadlock has no SQLite
  counterpart: one writer means two multi-table transactions never wait
  on each other, while on Postgres group delete against a concurrent
  grant can, and the retry wrapper is the fix.
- **`LISTEN/NOTIFY` and logical replication** are supported by every
  managed provider on a direct connection, and `LISTEN` is not available
  through a transaction-mode pooler. arizuko uses neither; routd polls.
- **TLS on by default** on Azure and Cloud SQL public IP; `sslmode` is a
  DSN field.

None of these is an abstraction; each is a DSN field, a pool cap, or a
retry class. That is the whole provider swap.

### Partition keys are per-table — an access-pattern note, not a design

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

## The three redesigns

Each is engine-neutral: a conditional write is `UPDATE … WHERE ver = ?`
plus `RowsAffected` on SQLite and a condition expression on a key-value
store; a version item is a row. Each lands on the current files, each is
proven by a property test, and together they remove the transaction
argument against a key-value model. What remains against it is the read
side, below.

### 1. Group delete: a folder is `(path, generation)`

Today the guarantee is "no stale row exists", which needs an atomic
predicate delete over four tables and is already false on one of the two
paths (`L198`). The redesign moves the guarantee to "a stale row cannot
match": `groups` gains a `gen` minted at create; every row that confers
on or routes into a folder — `acl.scope`'s literal root, `routes.target`,
`scheduled_tasks.owner`, `chat_reply_state.engaged_folder` — records the
`gen` of the folder it was written against; every reader that confers
(`auth.Authorize` through `policyRows`, the route resolver, timed's task
read, the engagement read) treats a row whose `gen` is not the folder's
current `gen` as absent. Delete becomes one conditional write on the
`groups` row; the four purges become a sweep that may fail or lag with
no consequence. A scope whose first segment is a wildcard belongs to the
instance world (§"Partition keys" above) and carries no `gen`; a group
delete never touched those rows either.

Preserved: a re-created folder starts with no authority on every delete
path, including one that never ran the cascade — true by construction
rather than by two transactions agreeing. Cost: `gen` threads through
the writers of those four tables (22 non-test files write control-plane
tables outside `resreg`) and one `groups` read per authorization;
[`5/33`](../5/33-paths-roles.md)'s coordinate stays the path — `gen` is
a storage-level liveness token, never shown to a principal.

### 2. Membership: one writer, a version on write, a bounded walk on read

Four copies of the cycle walk exist; the redesign keeps one
(`store.putMembershipTx`) and deletes three. The writer reads the
parent's ancestor set outside the transaction by key lookups, refuses if
it contains the child, then writes one conditional batch: the edge
`(child, parent)` conditioned on absence; the pairing token's claim where
there is one (the two-item batch §"Branch B, measured" counts as
fitting); and a
`membership` version item conditioned on the value it read — so two
inserts that would together close a cycle cannot both pass. On the read
side `store.Ancestors` becomes parent-pointer lookups with a visited set
and a depth cap, so a write defect denies rather than loops; the graph
is 58 edges on krons.

Preserved: acyclic, one edge per token, and revocation immediate — the
authorization read stays a strongly consistent read of the base table by
key, never an index: a DynamoDB global secondary index is "updated
asynchronously, using an eventually consistent model"
(docs.aws.amazon.com/amazondynamodb/latest/developerguide/GSI.html),
which is a revoked edge still conferring. Cost: the version item
serializes every membership write instance-wide, at a write rate of
pairings and joins.

### 3. `Apply`: a subsystem version, an in-progress marker, compensation

The checksum precondition becomes a subsystem version item that every
control-plane writer bumps — one place once the writers funnel through
`resreg`, which `36` S1, S6 and S11 already ask for; until then the 22
files above each bump it. The atomic swap becomes: set
`apply:<subsystem>` to in-progress, conditioned on the version; write
the manifest in batches of ≤100 actions; clear the marker. While the
marker is set the gates — `auth.Authorize`, route resolution — fail
closed, which is the property a half-applied manifest must not break: no
reader acts on a partial grant set. On failure the pre-image, which
`arizuko apply` already holds for rollback (`cmd/arizuko/apply.go:221`),
is re-applied under the same marker; if the process dies, the marker
stays and an operator resumes or rolls back by hand.

Preserved: no observable half-state confers, and a stale manifest is
refused. Lost: `L44`'s pre-image rollback becomes compensation with a
visible, fail-closed window equal to the apply's duration; the consistent
whole-subsystem export (`resreg/archive.go:68`, `:137`, a read-only tx)
becomes "read under the marker, or accept skew".

## The seam is `database/sql`, and it is one opener

A storage interface abstracts over engines that differ in **data
model**. Managed Postgres and self-hosted Postgres do not differ in data
model; they differ in DSN. `database/sql` is therefore the seam, and it
ships in the standard library.

What arizuko needs is not an abstraction but ONE place that opens a
connection, and that place is `db_utils`. It owns the pragma set:
`busy_timeout(5000)` and `foreign_keys(on)` ride the DSN, because
modernc.org/sqlite defaults foreign-key enforcement OFF per connection
and `database/sql` pools many — a one-shot `PRAGMA` covers one of them
and `ON DELETE CASCADE` silently no-ops on the rest. WAL is a hard
error, not a warning: a store in rollback-journal mode looks identical
to a healthy one.

Every production site calls `db_utils.Open(db_utils.FileDSN(path))` —
`store` for each owner DB, `authd`, `dashd`, the CLI's split cutover and
token signer, and both test harnesses. `db_utils/opener_test.go` reads
the tree and fails on any other `sql.Open("sqlite"`, so a new daemon is
caught the day it is written. Two such opens exist: the seam itself, and
the guard's own string literal.

A test file is held to the same rule, and the guard's only exemption is
`db_utils`'s own files. `ARIZUKO_TEST_PG_DSN` decides the engine a test
database is created on — unset, a private SQLite one held in memory;
set, a fresh database on that server, dropped when the test ends —
and `db_utils.NewTestDB` is the one place that reads it. `dbtest.Open(t)`
is how a `_test.go` reaches it; `store.OpenMem`, `routd.OpenMem` and
`runed.OpenMem` call `NewTestDB` from their own package, because a
production package must not link `testing`. That is what makes step 6's
proof askable: pointing the suite at the other engine is one variable
rather than an edit in every test file.

The cap is 4 per Postgres pool, set in the opener. SQLite is left
uncapped on purpose: a cap is a share of a finite SERVER budget and a
local file has no server, so capping it would be an unmeasured
throughput change to every daemon. The floor is that `Migrate` pins one
connection for a whole file while the daemon serves; the ceiling is that
a full deployment holds 20 pools, budgeted for 24, which asks a shared
server for 96 against the 97 a stock Postgres leaves.

That is "one declaration, many readers" — the same rule the route table
follows — and it is engine-neutral: the funnel is correct on SQLite
today and is the whole provider swap tomorrow.

A DynamoDB-shaped facade whose only implementation is Postgres is the
seam this spec refuses. Every call site would pay the key-value price —
no predicate deletes, no joins, no snapshot reads, FTS and cascades and
the partial unique index reimplemented as call-site discipline — while
the engine underneath could do all of it. Its one benefit, that a later
switch touches no call site, is paid up front whether the switch happens
or not.

## Three tiers, and the middle one is arizuko's own

The refusal above is narrow: no layer in arizuko presents a foreign
vendor's API. That is not an argument against an internal interface, and
the store has three tiers, not one.

**Tier 1 — the opener.** DSN in, pool out. It owns the pragma set, the
connection cap and the transient-error class. Every daemon calls it and
none opens its own. This is the unified setup, and it ships first.

**Tier 2 — what arizuko's daemons actually need.** Not a storage
abstraction and not a vendor's shape: operations named in arizuko's
terms. [`6/40`](40-store-surface.md) derives them and is the authority
on what they are. It admits an operation because it carries an
INVARIANT, not because its SQL repeats — the operations that decide
portability each appear exactly once, so a repetition test excludes
every one of them. Five survive, and three cannot be carried by a
key-value engine. A tier that is a thin passthrough over `database/sql`
remains the indirection this spec rejects.

**Tier 3 — each daemon's own queries.** A query one daemon issues is not an
interface, so it never joins tier 2. It is still not the daemon's to hold:
every statement lives in that daemon's storage layer, and the driver behind the
interface is what a second engine reimplements. Tier 3 names who OWNS a query,
not where the SQL sits.

**What the key-value question becomes.** It stops being an API to adopt
and becomes a property to measure: can tier 2 be expressed inside
conditional-write and partition-key semantics? Where it can, a later
engine is reachable. Where it cannot — the audit cursor, ranked search,
the partial unique index that is the trust root — the answer names
exactly which operation binds arizuko to a relational engine, which is
worth knowing whether or not anyone ever moves. The measurement is
free; a facade is not.

**Search is the one that is answered by leaving, not by modelling.**
`store/messages.go:630` and `routd/reads.go:110` are the same FTS5 query
written twice — `bm25` ranking, `snippet`, a rowid join to a virtual table, and
three triggers that keep `messages_fts` in step with `messages`. No document
store has ranked full-text search, and no denormalisation produces one. Every
other hard shape the review found is a model change: 36 aggregates become
maintained rollups, 18 `IN` lists become N queries, 13 `OR`s become two queries
or a stored attribute. Search is not on that list. **It is a second service
behind its own interface, not a store operation** — which is why the two
primitives are right to lack it rather than wrong.

## What the three redesigns are worth now

They are the price of surviving without an interactive multi-table
transaction. Postgres has one, so none of them is required by the
engine. Each is kept or dropped on its own property:

- **Redesign 1 (group delete as `(path, generation)`)** stays. It closes
  `L198` by construction rather than by remembering to purge in two
  places, and that is true on any engine.
- **Redesign 2 (membership version + bounded walk)** stays as the fix
  for the three duplicated walk copies. The concurrency half is what
  Postgres gives for free.
- **Redesign 3 (`Apply` in-progress marker)** needs a different mechanism
  on each engine. `resreg.Apply` upgrades to a write lock with `UPDATE
audit_log SET id = id WHERE 1 = 0`, a RESERVED lock in SQLite and a
  no-op in Postgres. It becomes `pg_advisory_xact_lock`, or the checksum
  recheck races. That substitution is mandatory on the move; the marker
  is not.

## Order — one daemon reaches no-SQL, then the rest go straight there

Not one schema migration, and not nine SQL layers either. **Exactly one daemon
is filed into a SQL layer**, because the interface is derived from the questions
that filing exposes. Every other daemon moves onto the interface directly. A
plan that files all nine first writes nine layers in SQL and then rewrites nine
layers; the filing is worth doing once, for its method names, and never again.

1. **The storage-layer rule and its guard.** SQL only inside a daemon's storage
   layer, enforced by a test that fails the build — the way
   `db_utils/opener_test.go` fails on a stray `sql.Open`.
   `db_utils/sql_layer_test.go` is that test, and `sqlLayers` is the covered
   set. **The guard reads string literals, so it proves placement and not
   ownership**: a daemon that hands its raw `*sql.DB` to `store` or `audit`
   passes it while 178 statements still run against its database (`L251`).
2. **`L195` settled** before anything is carried forward. `store/migrations/`
   is the frozen legacy pre-split schema: `migrate-split` migrates FROM it and
   nothing else runs it, so no daemon's port carries it.
   `store/migrations_frozen_test.go` fails the build on a file added to it.
3. **`authd` states its questions.** 15 statements, 3 files, 4 tables, its own
   database, one two-table transaction. Filed by entity, every method renamed
   for the question it answers. It is the only daemon that gets a SQL layer,
   and it gets one because step 4 is derived from it.

   **It was the wrong daemon to derive from, and the reason is one constant.**
   `authd/authstore/authstore.go:77` is `const Partition = "authd"` — one
   partition for the whole daemon, holding 3 signing keys, 2 accounts and 78
   refresh tokens fleet-wide. Every read is a point read or a whole-partition
   scan sorted in Go, so no read orders, ranges, pages or aggregates. A daemon
   that never exercises a constraint cannot find the constraint's holes, and a
   review of the first interface against 401 statements found six the derivation
   could not have surfaced. The message cluster in `routd` — `messages`,
   `sessions`, `chat_reply_state`, `chats` — is where the partition, the range,
   the cursor, the volume and the hot path all live, and it is the honest
   derivation set. Nothing here reverses: `authd` still holds no SQL, and the
   interface it produced is the one the review widened.

4. **The interface and its conformance suite**, derived from `authd`'s
   questions and `6/40`'s three operations over `Query(shape)` and
   `GuardedBatch`. The suite runs every test body against every driver, and an
   operation that cannot be expressed for both does not go on the interface.
5. **`authd` moves onto the interface.** Its method names do not change; the
   bodies stop being SQL. This is the first daemon that genuinely holds none,
   and it is the proof the interface carries a real daemon. `L251` closes here:
   `audit_log` becomes one entity behind the interface, implemented once per
   driver instead of twice in every daemon.
   **The port is complete, measured 2026-09-12.** Every table in arizuko is an
   objstore entity, including `messages` — 53,255 rows across three instances, 234
   cursors verified at the same position, and zero chats whose unfed set changed.
   71 packages green. Ranked search still runs FTS5 and now reads the entity's
   coordinate rather than a rowid; it leaves for its own daemon next, which is the
   last thing holding `routd` to SQLite.

What remains of SQL in the tree: 13 schema probes, the FTS search, and the
`migrations` bookkeeping row.

**Where this stood, measured 2026-09-10. One table was left.** Every table in
arizuko is an objstore entity except `messages` and its FTS twin. The census
over non-test Go finds 138 statements against `messages`, and after that only
`sqlite_master` probes, the `migrations` bookkeeping and test fixtures. Sixteen
registered resources, the five cursor tables, and the six remaining hot-tier
tables all moved; `session_log` turned out to be dead in `store` — `runed` has
owned that entity since its own move and `routd` federates over HTTP — and was
deleted rather than ported.

### How `messages` leaves, and how search stays in step

**The coordinate is `chat_jid` as the partition, `core.TS(timestamp) + sep + id`
as the sort key.** Twenty-five reads filter on the chat and range on the time,
and the largest chat partition measured is 10,688 rows. The four sort-key
inequalities on the interface were admitted for four of these reads, so this
entity is the reason they exist.

**The id has to be IN the sort key.** On krons 1,195 pairs carry an identical
timestamp string, 245 of them inside one chat, and 10,488 pairs share a second
inside one chat —
and a time-only key would put two messages on one item. That changes the cursor:
`chats.agent_cursor` is 30 bytes compared `> timestamp` today, and under a
composite key it must hold the whole sort key or a message at exactly the cursor
instant is re-fed. The migration derives each chat's new cursor as the greatest
sort key at or before the stored instant — the same position, a new rendering —
and that derivation is checkable per chat against a snapshot.

**The poll loop gets cheaper, not harder.** `NewMessages` is the one read with no
partition key, and it does not need one: it reads from the lowest cursor across
all chats and throws most rows away. `chats` IS the partition list — 136 items on
krons — and already holds each chat's own cursor, so the loop reads each chat
from its own watermark and `MinAgentCursor` goes.

**Sixteen live reads carry no `chat_jid`, not three.** The first count was wrong;
gopls settles that six of `store/messages.go`'s copies are test-only, leaving
sixteen live operations against twenty-one reads and ten writes. They are
answered by ONE idea rather than sixteen: a document store fans out at write
time into read-shaped indexes, and each index is an entity like any other.

**Three indexes, and the message itself stays in one place.**

- **The folder feed** answers `ObservedSince` and `DeadLetters(folder)`.
  Partition the folder, sort `core.TS + sep + id`, and carry a POINTER — the
  message's chat and the attributes the feed filters on (`sender`,
  `is_observed`) — never a copy. A reader queries the folder partition and then
  fetches the items it will actually render. One source of truth, and the index
  costs what its filters cost. It carries a count too, maintained by
  `objstore.Increment` on the same write, which is what answers
  `MessageActivityByFolder` over 95 folders without reading any of them.
  Its writers are BOTH sites that stamp `routed_to` — `PutMessage` as well as
  the observed path — which the first version of this section missed.
- **The activity feed** answers `handleActivity`, the newest thousand across
  every chat. One constant partition, sort `core.TS + sep + id`. Affordable only
  with a retention sweep, and the sweep is part of the entity rather than a
  later idea: an index with no drainage is the mistake `turn_context` already
  records.
- **Delivery state leaves the message, and `sent` is the ABSENCE of a delivery
  item.** The first version of this said partition the status and let the queue
  drain. Measured, it does not: 98.4% of krons rows are `sent`, `pending` holds
  zero, and nothing in the tree ever deletes a sent or failed row — so a `sent`
  partition is the whole table and never sheds one, which is the very mistake the
  activity feed's sweep exists to avoid. The live schema already knew:
  `idx_messages_status` is partial, `WHERE status != 'sent'`.

  So a delivery item exists only while the row is NOT sent. That is objstore's
  "absent is the only null" applied to a status, and it is what the partial index
  already encodes. Partition the status, sort `core.TS + sep + id`, carry a
  pointer — `chat_jid` and `delivery_attempts` — the same shape as the folder
  feed. `pending` then genuinely drains and `failed` is the dead-letter set, 388
  items on krons.

  **`platform_id` stays on the message**, because it is not mutable state: one
  statement writes it (`routd/db.go:360`), it is stamped once when the adapter
  confirms delivery, and no non-bot row on any instance carries one. Moving it
  would also cost a live read — `MessageByID` resolves a reply's parent by
  platform id inside one chat partition today, and a status partition turns that
  into a 23,625-row scan. `core.Message` loses `Status` and `DeliveryAttempts`
  only, and `RecordDeliveryFailure` is still `objstore.Increment`.

`ProactiveChats` is a `DISTINCT chat_jid` that becomes an attribute on `chats`.

**An operator naming a bare message id** — `DeadLetterByID`, `ReplayDeadLetter` —
passes the coordinate it just read, because every such id comes from a list the
same surface rendered. No id-to-coordinate index is built for a caller that
already holds the answer; if one ever genuinely starts from an id alone, that is
when the index earns itself.

**The two 19-byte rows** (`L250`, `2026-04-02 12:38:47` on krons) are read as
UTC by the migration. Every item needs a `core.TS` sort key and the move cannot
carry a naive datetime, so the choice is to normalise or to drop two messages.
Normalising is what `0044` should have done, and the widening is recorded there
rather than invented here.

**Search is a daemon with its own database and one verb** —
`Search(text, scope, sender, since, limit)`, which is today's `FoundMessage`
exactly, so the two duplicated FTS5 queries collapse to one owner. Its index
stays FTS5 on a SQLite deployment; the point is that no other daemon runs its
SQL.

**The index tails, it is never told.** A trigger commits with the writer's
transaction; a service has no such transaction, and "the writer notifies the
index" fails silently when the write commits and the notification does not — the
message is stored, the index lacks it, and search answers "no hits" rather than
an error, for ever. An empty result is indistinguishable from a correct one, so
nothing reports it. So the index holds one cursor per chat, the same shape as
`agent_cursor`, and advances only after its own write commits. A lost
notification then costs latency and never a gap; a crash between the index write
and the cursor advance re-indexes one row, which is idempotent under the message
coordinate. "Is the index in step" becomes `max(lag)`, a number an operator
reads. A notification may arrive as a wake-up, never as the delivery.

`messages` waits on one decision that is already recorded rather than open:
ranked search leaves the store for its own service (§"Search is the one that is
answered by leaving"), because no document store answers bm25 and no
denormalisation produces one. `messages` moves with that service, not before it.

**Where this stood, measured 2026-09-09.** The config plane is done. All
sixteen registered resources are objstore entities — onbod's three and routd's
thirteen — and `authd`, `emaid`, `chanlib`, `audit`, `runed` hold no statement
at all. Eighteen daemons never held one.

What still runs SQL is the hot tier, and it is the set this spec always said
would need a verdict rather than a port: `messages` (139 statements, plus its
FTS virtual table and three triggers), `user_profiles` 38, `sessions` 26,
`chats` 19, `turn_context` 18, `chat_reply_state` 12, `task_run_logs` 11,
`invite_redemptions` 6, `group_watchers` 6, `turn_results` 4, `chat_proactive`
4, `system_messages` 3, `session_log` 3. None is a management resource and none
should become one.

Three of those decide the port's shape rather than its size. `chats` holds
`agent_cursor` and `sessions` holds `observed_cursor`, so an import that drops
them re-feeds every consumed message — the failure `0044`'s migration order
exists to avoid. And `messages` carries ranked search, which §"Search is the one
that is answered by leaving" already sends to its own service rather than
modelling.

6. **`resreg` moves before any registered table does.** `resreg/engine.go:209`
   is the ONE row reader for every registered resource and it builds
   `SELECT <columns> FROM <table>` from `RowType` reflection. An entity has
   three columns, so the moment a registered table becomes one, every
   `apply`, `export`, `plan`, `get`, `packages` and `archive` verb breaks at
   runtime. `audit_log` escaped by declaring `DB: ""` and leaving the config
   lane entirely, which is right for evidence and wrong for `acl` and `routes`.
   Seven of `store`'s ten single-table files are registered; that is why only
   two of them moved (`L259`).
7. **Every other unit moves straight to the interface**, never through a SQL
   layer. Measured 2026-09-07 with `go/parser` over non-test files: **eighteen
   daemons hold no SQL at all** — `webd`, `slakd`, `timed`, `gitd`, every
   adapter — because they reach storage through `store` or over HTTP, so "every
   daemon" is nine units, not twenty-one. `proxyd` 3 statements, `emaid` 5,
   `cmd/arizuko` 8 and `chanlib` 9 come first; `resreg` 18 and `onbod` 39
   next; `routd` 140 and `store` 178 last, `store` last of all because every
   other unit reaches through it. `dashd` and `runed` already hold one named
   layer each, so they re-do like any other unit: the bodies change, the names
   stay. Each unit may add an operation the interface lacks, which is the
   expected cost of deriving the surface from one daemon rather than nine.
8. **Export from the old backend, import into the new one**, through the lane
   below rather than a script per daemon. One-way, and rehearsed against a
   snapshot of a live database before it is believed — "0 rows matched" is a red
   flag, never an all-clear.
9. **Two drivers: Postgres and DynamoDB.** The interface is `6/40`'s three
   operations over two primitives — arizuko's own vocabulary, not a vendor's
   API surface — so each driver implements those directly, with no wire
   protocol translated in between.

   **There is no SQLite-specific driver, and SQLite still runs.** The SQL driver
   generates its own statements, so serving SQLite and Postgres is a dialect
   switch inside one driver — placeholders, `substr` versus `substring`,
   `json_extract` versus `->>`, `INSERT OR IGNORE` versus `ON CONFLICT` — not a
   second implementation. `db_utils.Open` already selects the engine from the
   DSN. That is what dissolves `L228`: the dialect distance stops being 156
   hand-written DDL files to port and becomes one generator with two emitters.

   SQLite is still the SOURCE rather than a destination. A daemon that has moved
   runs on whichever DSN it is given, and the pre-port `.db` file stays on disk
   unread, so the code that exists today can still open it.

   **No surrogate database.** Two were examined and both answer a question this
   design no longer has. ScyllaDB Alternator is production-supported since 2020
   but source-available since AGPL 6.2.x; AWS's ExtendDB is Apache 2.0 and Rust,
   speaks the DynamoDB wire protocol over PostgreSQL, and is v0.1 "for
   development, testing, and experimentation" by AWS's own words. Either would
   be needed only to serve the FULL DynamoDB API — 58 operations, four
   expression grammars, a typed attribute model and pagination semantics.
   Two primitives need none of it, and a partial implementation written here
   beats a complete one written for a surface arizuko never calls.

   What a vendor API would have imposed for free is portability discipline.
   **The conformance suite replaces it**: every driver runs the same test
   bodies, and an operation that cannot be expressed for both does not go on
   the interface.

   With the DynamoDB driver come the three redesigns §"Branch B, measured"
   costed: the
   versioned replace's per-scope version, membership's materialised ancestor
   closure, and `resreg.Apply`'s lock object with bounded batches, which gives
   up the pre-image rollback it carries today (`L44`).

## How a storage layer is organized

A storage layer is organized the way the HTTP surface is: one file per thing,
named for the thing. Three rules, and the third is the one that stops the first
two from being wrong.

**1. One file per entity.** `spawns.go` holds every read and write of `spawns`.
A reader looking for how a table is used opens one file and finds all of it,
and a driver rewriting that entity rewrites one file. Measured 2026-09-07:
**38 of the 67 files that hold statements already touch exactly one table**, so
this is mostly filing, not redesign. The work concentrates in nine files, and
`routd/db.go` is the job — 76 statements over 10 tables.

**2. Methods are named for the question, not the verb.** `SelectTurns` names
the SQL; `PendingTurnsForFolder` names what the caller wants to know, which is
the thing a second driver has to answer some other way. Of 312 exported methods
holding SQL, 157 are named for the question and 137 for the verb. The verb
names hide predicates the caller depends on: `store/messages.go:61`
`NewMessages` returns at most 200 messages and excludes bot senders, and
`routd/db.go:318` `NewMessages` — the same name, a different type — does
neither. `UpdateObservedCursor` (`store/sessions.go:93`) is a forward-only
guarded transition, which is `6/40` §2 and not an update.

**3. An operation that carries an invariant stays whole, in the file of the
entity it protects.** This is the limit of rule 1, and ignoring it is how the
filing becomes a bug. `CreateGroupWithAdmin` writes `groups`,
`acl_membership`, `acl` and `audit_log` in one transaction because a group row
that exists without its admin grant is an escalation (`L119`); it belongs in
`groups.go` whole, never split across four entity files. Sixteen operations
span two or more domain tables, and `store.DeleteGroup` spans eighteen —
`6/40` §3 already names that one as the operation a key-value engine cannot
carry, so `folderScopePurge` keeps its own home rather than scattering.

Two file kinds are entity-free by design and stay that way: the generic engine
(`resreg/engine.go` builds its statement from `r.Table`) and the total
classification (`store/folder_scope.go` iterates the scope list). Splitting
either by entity would destroy the property that makes it correct.

**Where the layer lives: a daemon gets a package, a library already is one.**
`authd/authstore`, `emaid/emaistore`, `chanlib/chanstore`, `runed/runstore` and
`dashd/dashstore` are separate packages because each daemon has handler code
that must stay free of statements, and a package boundary is the thing the
compiler and `db_utils/sql_layer_test.go` can both enforce. `audit` and `store`
get no such package: they are storage libraries with no handlers to separate
from, so `audit/audit_log.go` IS the layer and an `auditstore/` directory would
add nothing but a path. The test is whether the package holds anything that is
not storage.

The prefixes — `authstore`, `emaistore`, `runstore` — exist because Go cannot
have two packages named `store` imported into one file, and today's shared
`store` holds that name. When `store` stops being shared and becomes routd's
own layer, each owner's package can take its owner's name and the prefixes stop
earning their keep. That rename waits until `routd` and `store` have moved;
doing it mid-port is churn with no test behind it.

## How a daemon's tables become documents — five rules

Derived from modelling `runed`, the first daemon to move. Its five tables are
one domain entity (`spawns`, 232 rows, 56 references), one mandated
`audit_log`, one history log (`session_log`, 15,008 rows), one flag table
(`circuit_breaker`) and bookkeeping — and the sixth table SQLite reports is
`sqlite_sequence`, which exists only because of `AUTOINCREMENT` and does not
survive the move.

**1. Partition by the coordinate you filter on, not the identifier you look up
by — but count the WRITES, not only the reads.** `spawns.run_id` is the SQL
primary key, and the reads are `WHERE folder=?` and `WHERE folder=? AND state
IN ('queued','running')`, which argues for `folder` as the partition. Building
it proved that wrong for this table. Six operations address a spawn by run id
with NO folder in hand — `GET` and `DELETE /v1/runs/{run_id}` receive one path
value, and `Manager.Kill` is the thing that RESOLVES a run to its folder — and
three of them carry invariants. A guarded write names one item, so under a
folder partition the condition would run after a scan had found the row, which
is exactly the read-then-write the condition exists to replace. `spawns` takes
one constant partition and the run id as its sort key; folder reads and
newest-first ordering become the reader's, and what makes that affordable is
drainage — 232 items over 53 folders behind a seven-day sweep.

The tenancy coordinate is still usually right, and `session_log` takes it: no
drainage, 15,008 items with 11,993 in one folder, read per turn with a
descending limit, so the folder partition makes it a range instead of a scan.

**2. Count the queries that carry no partition key before designing.** runed
has three: `state IN ('queued','running')`, `kind='agent' AND state IN (…)`,
and `created_at < ?` for the sweep. Each is a full scan unless it gets a
secondary index or the caller starts supplying a folder. A query without the
partition key is either an index you must declare or a design error you must
fix — never something to discover at runtime.

**3. A foreign key becomes an embed or a duplicated key, and you must choose.**
`spawns.session_log_id REFERENCES session_log(id)` cannot be a join. If the
child is only ever read with its parent, embed it; if it is read on its own,
copy the parent's key into it and index that. Deciding by default is how a
document model rots.

**4. Never order by a generated id.** `session_log` is read
`ORDER BY id DESC LIMIT ?`, and that `id` is `AUTOINCREMENT` — a sequence no
document store has. Order by a time key the writer produces, fixed width so
string order is time order. That is `core.TS` and the reason `L97` cost every
approval in the system.

**5. Separate history from state.** `audit_log` and `session_log` are history;
`spawns` and `circuit_breaker` are mutable state. History needs none of the
transactional guarantees state does and partitions by time or folder without
contention. Modelling them alike makes the cheap thing pay the expensive thing's
costs.

History is not always one write. `audit_log` is: one row, appended, never
touched again. `session_log` is opened when a run starts and closed when it
exits — `runed/db.go:116` sets `ended_at`, `result`, `error` and
`message_count` on the row `StartSession` created. That is two writes to one
key with no read-modify-write of content, which every engine carries, and it is
still history: nothing else ever writes that row, and no invariant spans it.

## The migration is an export and an import, through infrastructure that exists

Not a hand-written script per daemon. `resreg.ExportSnapshot` reads a
subsystem through its registered resources and emits a checksummed manifest;
`arizuko apply` writes one back. Export from the old backend, import into the
new one, export again and compare — that is the migration and its own test, and
it is a property rather than an example, so it catches the drift a bespoke
script introduces.

**The lane runs today, and what it carries is measured.** On 2026-09-07 all
three live instances were snapshotted with `sqlite3 .backup`, migrated to routd
0046, and exported with `arizuko archive export <instance> --quiesced`. Every
export succeeded. `routd.yaml` carries thirteen resources — `acl`,
`acl_membership`, `github_sources`, `groups`, `installed_packages`,
`network_rules`, `pending_actions`, `proxyd_routes`, `route_tokens`, `routes`,
`scheduled_tasks`, `secrets`, `web_routes` — beside `routd.messages.jl`,
`routd.secrets.yaml` and `onbod.yaml`.

**The gap in that path is the prerequisite, and it is work already owed.** Ten
routd tables hold rows and travel in no dump. Four are runtime state the port
must carry: `chats` (which holds `agent_cursor`), `sessions` (which holds
`observed_cursor`), `chat_reply_state` and `system_messages`. An import that
drops the first two re-feeds every consumed message — the same failure
`0044`'s migration order exists to avoid. Three more are cold-tier management
entities root `CLAUDE.md` already requires to be resreg resources —
`chat_proactive`, `group_watchers`, `pane_sessions`, `user_profiles` — and "a
management resource without a resreg registration is a review-blocker". That is
the `5/16` debt, and closing it is step 3a rather than a separate project.

The rest are history, and history is a separate decision from state:
`audit_log` (163,750 rows on krons) has its own hash-chained archive design,
`cost_log_archive` is deliberately left behind by `6/39`, and `turn_results`
and `task_run_logs` each need an explicit verdict on whether they move, are
rebuilt, or stay in the database the port leaves behind.

**The SQLite files stay.** A port copies out; it does not consume. Every
instance keeps its `routd.db`, `onbod.db`, `auth.db` and `runed.db` byte for
byte, so the old code path reads them unchanged for as long as an operator
wants a second opinion. The dump is a snapshot beside the database, never
instead of it.

Steps 1 to 4 change no engine and no data, and revert with `git revert`. Steps
5 and 6 are per-daemon and revert per daemon, which is the whole point of doing
it this way: the point of no return arrives once per daemon instead of once for
the system.

**The open question this does not answer.** Seven owners and 156 versioned DDL
files are a relational discipline, and an object store has no DDL past creating
a table and its keys. Under a second driver that apparatus does not port, and
the reviewability versioned files give has to come from somewhere else.
`L228` and `L195` both feed it.

## Ties

`22` step 4 (the Dex trade) · `23` (the gateway that needs the database) ·
`36` (the control plane's writers, S1/S6/S11) · `5/5` R2 (a world is a
predicate) · `5/16` (ownership = the migrating daemon) · `5/33` (the path is
the coordinate) · `7/22` (argues from the invariants) · BUGS `L44`,
`L66`/`L67`, `L195`, `L198`.
