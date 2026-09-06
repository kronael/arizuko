---
status: partial
depends:
  [
    38-storage-seam,
    22-proxyd-cut,
    23-llm-gateway,
    36-minimal-projection,
    ../5/16-mcp-rest-unification,
    ../5/33-paths-roles,
  ]
---

# specs/6/37 — the store model: Postgres, behind one opener

Decision (maintainer, signed off 2026-09-05): **Postgres is the engine
for arizuko's own data, not only for the tools that require it.** Dex
and LiteLLM already put a Postgres in the deployment; arizuko joins it
rather than running a second engine beside it. `6/38`'s census is the
evidence and its recommendation is adopted.

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
database (docs.litellm.ai/docs/proxy/db_info) —
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
not in the DDL, as `6/38` found. 47 `Begin`/`BeginTx` sites in 24
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

[`6/38`](38-storage-seam.md) §"Branch B, measured": 47 sites, 26
multi-table; 19 fit a bounded conditional batch (DynamoDB
`TransactWriteItems`: ≤100 actions, no two on one item, a condition only
on the item it sits on), 7 do not, and 3 of those 7 are
security-bearing. The three, re-verified in the code:

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
written, so both survive a batch model as written. The other 17 are
`6/38`'s classification, not re-read here.

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
instance world (`6/38` §"Partition keys") and carries no `gen`; a group
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
there is one (the two-item batch `6/38` counts as fitting); and a
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

Every production site calls `db_utils.OpenWAL(db_utils.FileDSN(path))` —
`store` for each owner DB, `authd`, `dashd`, the CLI's split cutover and
token signer, and both test harnesses. `db_utils/opener_test.go` reads
the tree and fails on any other `sql.Open("sqlite"`, so a new daemon is
caught the day it is written. `store.OpenMem` is the one exception,
named in that test: its DSN is a per-call in-memory database with no
on-disk pragma set to share.

The connection cap is not in the opener yet. It belongs there — the spec
asks for one — but no site sets one today, so the number is a measurement
this tier still owes.

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
abstraction and not a vendor's shape: the operations the daemons already
repeat, named in arizuko's terms. The surface is DERIVED from the 474
statements now in the tree, never invented ahead of them — a tier that
is a thin passthrough over `database/sql` is the indirection this spec
rejects, and the way to avoid it is to admit only operations that
several daemons write today by hand. The candidates the tree already
shows: folder-subtree containment (24 hand-written `LIKE` predicates,
one helper), the audited transaction, a resource rebuild from a
document, and an idempotent ledger append. Each is a place where the
same SQL appears in more than one package.

**Tier 3 — each daemon's own queries.** They stay SQL against the pool.
A query one daemon issues is not an interface.

**What the key-value question becomes.** It stops being an API to adopt
and becomes a property to measure: can tier 2 be expressed inside
conditional-write and partition-key semantics? Where it can, a later
engine is reachable. Where it cannot — the audit cursor, ranked search,
the partial unique index that is the trust root — the answer names
exactly which operation binds arizuko to a relational engine, which is
worth knowing whether or not anyone ever moves. The measurement is
free; a facade is not.

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

## Order and reversibility

1. The `postgres` service ships and the two tools use it (`22`, `23`).
   Verifiable: Dex and LiteLLM start against it, and `/spend/logs`
   answers. No arizuko daemon has a DSN yet.
2. `L198`: one group delete, both halves — a fix, not this spec.
3. Redesign 1 on the current files. Proof: delete through each path,
   re-create, assert a pre-delete admin row does not authorize.
4. Redesign 2: the three walk copies deleted. Proof: two inserts that
   would together close a cycle run concurrently and one is refused.
5. Redesign 3: `arizuko apply` rehearsed against a `.backup` copy with a
   kill mid-apply — the marker stays, the gates deny, the resume clears it.
   Its SQLite write-lock upgrade becomes `pg_advisory_xact_lock` on the
   move, and the substitution is mandatory or the checksum recheck races.
6. `pgx` enters `go.mod` and `db_utils` opens either engine from the DSN
   it is given, with the connection cap this tier owes and Postgres's
   transient class (`40001`, `40P01`, connection reset) added to the
   retry rule. Proof: the same suite passes against both engines.
7. Each owner database moves on its own, smallest first — `auth.db`,
   `onbod.db`, `runed.db`, then `routd.db`. A daemon migrates its own
   schema on either engine, so no daemon waits for another.
8. `messages` and `audit_log` last, because `messages_fts` is FTS5 and
   its Postgres counterpart is `tsvector`: the index is rebuilt, not
   copied, and it ranks differently.

Steps 1–6 are additive and revert with `git revert` — the SQLite files
are untouched and still authoritative. **The point of no return is the
first owner database whose writes land only on Postgres**, because
reverting it needs a copy back that does not exist. Step 8 is the
sharpest of those: the search index cannot come back byte-identical.

## Ties

`6/38` (the census and the declined seam) · `22` step 4 (the Dex
trade) · `23` (the gateway that needs the database) · `36` (the control
plane's writers, S1/S6/S11) · `5/16` (ownership = the migrating daemon) ·
`5/33` (the path is the coordinate) · BUGS `L44`, `L66`/`L67`, `L195`,
`L198`.
