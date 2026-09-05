---
status: draft
depends:
  [
    ../7/21-storage-seam,
    22-proxyd-cut,
    23-llm-gateway,
    36-minimal-projection,
    ../5/16-mcp-rest-unification,
    ../5/33-paths-roles,
  ]
---

# specs/6/37 — the store model: SQLite for arizuko, Postgres for the tools

Decision (maintainer, 2026-09-05): Postgres is not the model for
arizuko's own data. It runs in the deployment for the two tools that
require it, and no arizuko daemon connects to it. arizuko's store stays
one SQLite file per owner daemon. The relational model is not a
commitment either: `7/21` measured that three write paths bind arizuko
to an interactive multi-table transaction, and this spec names what
each must become to survive without one. `7/21`'s census stands as the
evidence; its recommendation — move arizuko to Postgres — does not.

## Two databases, one boundary

**Dex** documents five backends — etcd, Kubernetes CRDs, SQLite3,
Postgres, MySQL — and says of the one arizuko would share: "SQLite3 is
the recommended storage for users who want to stand up dex quickly. It
is not appropriate for real workloads"
(dexidp.io/docs/configuration/storage/, read 2026-09-05;
[`22`](22-proxyd-cut.md) §"What adopting Dex costs" prices this).

**LiteLLM** fixes its engine in code, not in prose: `schema.prisma`
declares `datasource client { provider = "postgresql" }`, and Prisma
binds its query engine to that provider at generate time. Its production
page says "Currently, PostgreSQL is our primary supported database for
production deployments" and "We explored MySQL but that was hard to
maintain and led to bugs for customers" (docs.litellm.ai/docs/proxy/prod).
Virtual keys, spend logs, budgets and team management all need that
database (docs.litellm.ai/docs/proxy/db_info) —
[`23`](23-llm-gateway.md) chose it for exactly those.

The split:

- The `postgres` service ships inside the package that needs it — the
  Dex package of `22`, the gateway package of `23` — as that package's
  compose fragment, with its DSN in that package's env only. Whether the
  two share one server is a packaging choice those specs make.
- No arizuko daemon carries a Postgres DSN, and `go.mod` carries no
  Postgres driver: `modernc.org/sqlite v1.46.1` is the only database
  dependency. arizuko reads LiteLLM's spend and Dex's identity over their
  HTTP faces, never their tables; they never open `store/`.
- [`5/16`](../5/16-mcp-rest-unification.md)'s rule is unchanged: a store
  belongs to the daemon that migrates it. Dex and LiteLLM migrate theirs.

`7/21` counted arizuko as a third adopter, which made a shared Postgres
look like substrate. With arizuko out, the count is two tools and the
one server they need anyway. "The marginal cost is a connection string"
is the argument for the lock-in this spec declines; it is not an
argument for re-opening it.

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
not in the DDL, as `7/21` found. 47 `Begin`/`BeginTx` sites in 24
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

[`7/21`](../7/21-storage-seam.md) §"Branch B, measured": 47 sites, 26
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
`7/21`'s classification, not re-read here.

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
instance world (`7/21` §"Partition keys") and carries no `gen`; a group
delete never touched those rows either.

Preserved: a re-created folder starts with no authority on every delete
path, including one that never ran the cascade — true by construction
rather than by two transactions agreeing. Cost: `gen` threads through
the writers of those four tables (22 non-test files write control-plane
tables outside `resreg`) and one `groups` read per authorization;
[`5/33`](../5/33-paths-roles.md)'s coordinate stays the path — `gen` is
a storage-level liveness token, never shown to a principal. Rejected:
`7/21`'s per-world version item that every writer bumps so the delete
can `ConditionCheck` it — it keeps "delete every row" as the invariant
and still fails past 100 rows.

### 2. Membership: one writer, a version on write, a bounded walk on read

Four copies of the cycle walk exist; the redesign keeps one
(`store.putMembershipTx`) and deletes three. The writer reads the
parent's ancestor set outside the transaction by key lookups, refuses if
it contains the child, then writes one conditional batch: the edge
`(child, parent)` conditioned on absence; the pairing token's claim where
there is one (the two-item batch `7/21` counts as fitting); and a
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

### One document instead of three redesigns

The control plane of the largest instance is 77 KB of YAML. Stored as
one document per subsystem and swapped by compare-and-swap, all three
paths above become a single conditional write, and the checksum `Apply`
already computes is the document's version. DynamoDB caps an item at
400 KB (docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.NamingRulesDataTypes.html)
— 5× headroom on krons, and a hard ceiling. The single CAS is the single
writer SQLite has per file, at the write rate of grants and routes; the
ledgers stay outside it. This is the shape to choose if one design is
wanted rather than three; the three above are what to do table by table
if the ceiling or the per-row readers rule a document out.

## What a key-value model costs after the redesigns

- **The audit trail's query, not its atomicity.** Every audit row commits
  with the mutation it records (`runAudited` ×24, the `resreg` mutate
  path, `audit.EmitInTx` in dashd's delete); a two-item batch keeps that.
  What goes is `audit.Query`'s shape (`audit/query.go:99-139`): subtree,
  category, actor substring, and an exact cursor on `id` because "id
  order IS time order". A partition per world sorted by `(time, ulid)`
  keeps the page and the cursor; subtree and actor become filters on the
  partition, and actor substring is a partition scan over 159,309 rows.
  The gates never read `audit_log` — its one reader is `audit.Query`,
  fed by the four `audit_resource.go` files into dashd; the gates read
  `acl` and `cost_log`.
- **Full-text search has no replacement in the model.** `messages_fts`
  and its 3 triggers serve `FindMessages` (`routd/reads.go:114`,
  `store/messages.go:755`) for the agent tool (`ipc/ipc.go:2166`), MCP
  (`routd/mcp.go:480`) and REST (`routd/reads_http.go:133`). A key-value
  store has no `MATCH`; recall over history becomes an external index or
  a scan.
- **Aggregates become counters.** The cost gate reads `SUM(cost_cents)`
  per folder-day and per user-day (`routd/db.go:1141`); a counter item
  updated in the same batch as the `cost_log` put preserves it exactly.
  dashd's usage page (`dashd/usage_page.go:114-181`) joins `cost_log` to
  `messages` with `GROUP BY` — counters or scans.
- **Query shapes that move into code** (non-test Go, lines/files): `JOIN`
  13/10, `GROUP BY` 8/6, `UNION` 10/6, `DISTINCT` 13/9, `WITH RECURSIVE`
  5/3, `LIKE` 28/16 of which `groupfolder.SQLSubtree` 16/11, `ORDER BY`
  100/43, `json_extract` 12/2. A subtree predicate is a key prefix only
  where the folder is the key; 19 of routd's 29 tables have no folder
  column (`7/21`).
- **Schema versioning.** 153 migration files in 7 embedded sets — `routd`
  43, `store` 88 (the second declaration `L195` records), `authd` 7,
  `onbod` 6, `runed` 6, `chanlib` 2, `emaid` 1 — and `db_utils.Migrate`'s
  `(service, version)` ledger. A key-value item carries its own shape
  version and readers upgrade on read; the duplicate-version check that
  caught `L4` (`db_utils/db_utils.go:79`) has no equivalent.
- **The seam returns.** `modernc.org/sqlite` runs in-process, which is why
  `make test` needs no server. A key-value model needs a local
  implementation for tests and a cloud one for deployment — the interface
  `7/21` declined for Postgres, and the right one for a store whose data
  model is the interface (get, put, query by key, conditional batch). A
  local implementation on SQLite — one `kv(pk, sk, doc, ver)` table per
  owner — keeps tests in-process and makes "SQLite now" and "key-value
  later" one code path.
- **Failure class.** SQLite fails a stale read-then-write loudly
  (`SQLITE_BUSY_SNAPSHOT`, the "busy/locked" class the retry rule names);
  a failed condition is the same class but not transient — the caller
  re-reads and re-decides, never retries blind.

## Decision: SQLite now

Nothing measured needs a second engine. One writer per file is by design
(`runed/manager.go:205`: "the mutex IS the `BEGIN IMMEDIATE` claim"), and
the busiest ledger writes about one row a minute. The concurrent writers
and managed failover Postgres would buy answer a problem no instance
has, at the price of the relational commitment the maintainer declines.
The three redesigns are the work that makes the model a choice; each is
worth its property on SQLite alone — the first closes `L198` by
construction — and none requires the others.

## Order and reversibility

1. Postgres ships inside the Dex and gateway packages (`22`, `23`).
   Verifiable: `go.mod` names no Postgres driver; no generated
   `env/<daemon>.env` carries a Postgres DSN.
2. `L198`: one group delete, both halves — a fix, not this spec.
3. Redesign 1 on the current files. Proof: delete through each path,
   re-create, assert a pre-delete admin row does not authorize.
4. Redesign 2: the three walk copies deleted. Proof: two inserts that
   would together close a cycle run concurrently and one is refused.
5. Redesign 3: `arizuko apply` rehearsed against a `.backup` copy with a
   kill mid-apply — the marker stays, the gates deny, the resume clears it.
6. A key-value pilot on a ledger with one writer and no join: the adapter
   dedup store (`chanlib/migrations`: `outbound_dedup`, `inbound_pending`,
   keyed by idempotency key; whapd already runs the same contract as one
   file per key). Then `cost_log` with its counters; then `messages`.
7. `audit_log` last.

Steps 1–5 are code on the same SQLite files and revert with `git
revert`. Step 6 reverts by rebuilding the dedup ledger. **The point of no
return is a ledger with history written to the key-value store only** —
`messages` or `audit_log` — because the `id` cursor and the FTS index
cannot be rebuilt from a key-value partition without a copy back, and no
copy-back exists.

## Honest gaps

- The 19-fit classification is `7/21`'s; four sites were re-read here
  (the three non-fits, invites, `/always`) and the site count (47, 24
  files) re-counted.
- No key-value target is chosen. DynamoDB's published limits are used
  because they are the strictest (100 actions, 400 KB, eventually
  consistent indexes); a store with multi-document transactions moves
  the 19/7 line and was not measured.
- The 77 KB manifest is one instance. The document ceiling is 400 KB; no
  instance near it was measured.
- Write contention was not measured: the version items in redesigns 2
  and 3 assume membership and control-plane writes are rare, which the
  row counts suggest and no log proves.
- The `gen` check adds a `groups` read to every authorization;
  `policyRows` has no cache and the cost was not measured.
- The search loss has no measured usage: how often `find_messages` runs
  is not logged.
- Whether Dex and LiteLLM share one Postgres server or each ship their
  own is left to `22` and `23`.

## Ties

`7/21` (the census and the declined seam) · `22` §"What adopting Dex
costs" · `23` (the gateway that needs the database) · `36` (the control
plane's writers, S1/S6/S11) · `5/16` (ownership = the migrating daemon) ·
`5/33` (the path is the coordinate) · BUGS `L44`, `L66`/`L67`, `L195`,
`L198`.
