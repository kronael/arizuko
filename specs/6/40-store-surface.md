---
status: shipped
shipped: 2026-09-06
depends: [37-store-model, ../5/33-paths-roles]
---

# specs/6/40 — the store surface: three operations

`6/37` names a middle tier between the opener and each daemon's own SQL, and
derives its candidates from repetition — an operation is admitted when the same
SQL appears in more than one package. That test admits the easy repeats and
excludes every operation that carries an invariant, because each of those
appears exactly once. `6/37` measured which transactions a key-value engine
cannot carry: group delete, the membership walk, and `resreg.Apply`. None of
them is in `6/37`'s list.

This spec derives the surface from invariants instead. **Three operations.**

## 1. Containment — is this coordinate inside that one

Every scoped read asks it, and `groupfolder.SQLSubtree` is the one answer, at
14 call sites:

```go
"(" + col + " = ? OR substr(" + col + ", 1, ?) = ?)"
```

The count is what a mutation proves, not what a grep reports. Three reads
spelled containment with `LIKE` and bound the wildcard from Go, so the grep that
closed `L122` read clean over them (`L252`, fixed). The check that holds is
`store/subtree_filter_test.go`: mutate `SQLSubtree` itself back to `LIKE` and
every adopter goes red. A site that does not call the helper cannot fail that
test, which is why adoption is the guarantee and the grep never was.

A subtree is a **string prefix on the path column**, and nothing else — no
closure table, no adjacency list, no nested set. The separator is `/` between
folders and `#` before a topic, which is why one helper scopes
`routes.target`, where a topic hangs off a folder.

That representation is what makes the folder portable. A prefix on a path is
`begins_with` on a sort key, native to a key-value engine — but only if the
partition key sits ABOVE the folder, at the tenant root, so one query covers
the subtree. Choose the folder itself as the partition key and the same
predicate becomes a full scan. The prefix is also why the predicate must be
`substr`, not `LIKE`: `_` and `%` are wildcards, so a folder named `a_b`
matches `aXb` (BUGS `L122`). Measured on sqlite 3.51.2 and PostgreSQL 17.11,
that half leaks on both engines. **Case folding is an engine difference and not
a general fact** — sqlite's `LIKE` folds ASCII, Postgres's does not — so a
`Coach`/`coach` pair leaks on sqlite alone. `substr` is exact on both, which is
what makes one predicate serve two drivers.

## 2. The guarded transition — read, decide, write under a condition

Two sites today: invite redemption conditions its update on `used_count` and
`active` (`store/invites.go`), and a hold resolution conditions on `status`
(`routd/steer.go`). Both read outside any transaction and make the decision a
condition on the row the decision came from, so neither needs to hold a lock
across the read.

That is a conditional write, which every engine has. It stays on the surface
because it is the shape that keeps the other 19 multi-table transactions
`6/37` classified from needing an interactive one.

## 3. The versioned replace — make a scope's rows equal a document

`resreg.Apply` deletes a resource's rows by scope and inserts the manifest's,
per table, then recomputes a checksum. Group delete runs the same per-table
deletes over a folder subtree and inserts nothing. **They are one operation
with different arguments**: replace the rows matching a scope predicate with a
document, and version the result.

- Group delete is `replace(subtree(folder), ∅)`.
- `Apply` is `replace(exact(subsystem), manifest)`.

The two halves each spec worried about separately are the same half. The
generation that stops a recreated folder inheriting the last tenant's admin,
and the checksum that makes a package install transactional, are both "version
the result".

**The collapse is a proposal, and the code does not do it today.**
`store.DeleteGroup` runs four table-specific predicates and carries no version;
`resreg.Apply` scopes on an exact resource value and compare-and-swaps a
subsystem-wide checksum (`resreg/engine.go:301-316`, `:786-879`). Making them
one operation is work, not an observation, and `L198` is the open half of it.

This is the operation a key-value engine cannot carry as one write. The scope
is unbounded, so the row set must be read before it can be deleted, and a row
added in between survives. That is the price `6/37` costed, and it is now one
price rather than two.

## The audited write commits in a GuardedBatch, not an intent log

`GuardedBatch` is a bounded all-or-nothing write, so **a mutation and its audit
row go in one call and the guarantee `runAudited` gives today survives
unchanged**: no write without its audit, no audit without its write. `6/37`
already measured that this fits — the 24 `runAudited` callers are in its
19-site "fits as a bounded conditional batch" group precisely because none of
them reads inside the transaction.

**Central and per-owner become the same design.** Four SQLite files force
audit to be per-owner: a transaction cannot span them, so each daemon must keep
its own. A document store has no such boundary — `TransactWriteItems` carries
up to 100 actions ACROSS TABLES in one account and region, and the isolation
the four files gave is now a partition key. So one audit table partitioned by
owner is the same thing as four audit tables, and a mutation in routd's table
can commit with an audit row in the shared one, in a single `GuardedBatch`.

That answers the question the audit research had to reject on today's
substrate. A central audit store written synchronously is only a distributed
transaction while the stores are separate; once there is one backend it is one
transaction, and centralisation costs nothing. The write stays atomic AND the
record is already in one place — no shipper, no outbox, no reconciliation.

What remains after the move is durability off the box, which is a different
requirement: retention, tamper evidence and an export the operator can hold.
The archive answers that, and it answers it for one table instead of four.

So the intent log is NOT the replacement for the audited write. It is needed
only where the write set is UNBOUNDED and therefore cannot be one batch: the
versioned replace over a subtree, and `resreg.Apply` over a subsystem. Those
two already have protocols of their own, and the marker they need is the same
mechanism, so it is one thing serving two callers rather than a scheme imposed
on forty.

**One audit row shape, two behaviours: `pending` then `done`.** The state
lives ON THE AUDIT ROW, not as a back-reference from the data row, and that
single choice is what makes it work.

- **Bounded write.** The batch carries the mutation and the audit row together.
  Whether the row is born `done`, or born `pending` and marked `done` in the
  same batch, the intermediate state is never observable — the transaction
  makes it invisible. Nothing changes for the 24 `runAudited` callers.
- **Unbounded write** — the subtree purge and `resreg.Apply`. The writes span
  several batches, so `pending` is real and observable, and it is exactly the
  marker those two protocols need. The audit row IS the in-progress marker;
  there is no second mechanism.

Putting the state on the audit row rather than on the data row removes three
failures a back-reference design has. A delete leaves nothing behind to carry a
back-reference, and here it does not need to. A later writer overwrites a data
row's marker, and here there is no marker on the data row to overwrite. The
`audit.Event` is built lazily inside the mutation
(`store/audit_helpers.go:10`) so a pending row cannot hold its outcome fields —
and here it is not asked to: `pending` records the intent, `done` records the
outcome, which is what those two states are for.

What remains is the fourth: a sweep must tell an abandoned `pending` from one
still in flight. That is a deadline on the audit row itself, so the judgement is
made from the row and needs no lookup into the data. It applies to two
protocols, not to the forty ordinary writes.

## What this surface is not

It is not a storage abstraction, and it presents no vendor's shape. Each
operation is named in arizuko's terms and each has a SQL implementation today.
Tier 3 — a query one daemon issues — stays raw SQL against the pool, and a
passthrough over `database/sql` remains the indirection `6/37` rejects.

## Three is not enough — two more the census missed

A review against the code found two invariants no operation above expresses.

**The reachability guard.** `store.AddMembership` walks the ancestor closure
with `WITH RECURSIVE up(p)` and refuses the edge if the child already appears
in it — a graph reachability test, decided by an unbounded read, under the same
lock as the insert. A path prefix cannot express it: membership is a DAG over
folders, not a tree, so a parent need not be a prefix of its child. This is the
operation `6/37` costed as needing a materialised ancestor closure maintained
on every membership write.

The same walk is on the READ path: `auth.Authorize` resolves a principal's
grants through `store.Ancestors` (`auth/authorize.go:134`, `:308`), and
`routd/sibling_db.go:312` walks it again to ask whether a caller is an
operator. Measured on all three live instances it costs nothing — 58 edges on
krons pointing at exactly two parents, `role:operator` and `role:member`, and
no parent is ever itself a child, so the recursion never recurses. The one
apparent DAG is an identity ALIAS (`telegram:user/… → google:…`) sharing a
table with role assignment, not a deep hierarchy.

**Left as it is, deliberately.** Measured on all three live instances it costs
nothing: 58 edges on krons pointing at exactly two parents, `role:operator` and
`role:member`, and no parent is ever itself a child, so the recursion never
recurses. The one apparent DAG is an identity ALIAS
(`telegram:user/… → google:…`) sharing a table with role assignment.

Resolving at token-mint time and revoking from the recorded issued set would
take the walk off the authorize path. It is an OPTIMISATION — the walk still
runs at mint, the revocation scan still runs, and this operation stays in the
census either way — so it is recorded in
[`15/h`](../15/h-authorization-cache.md) with its measurements and its reopen
trigger, not scheduled here.

**The claim-and-return.** `store.DueTasks` is
`UPDATE scheduled_tasks SET status='firing' WHERE status='active' AND next_run
<= ? RETURNING …` — a conditional update over an unbounded set that returns
what it claimed, which is a work-queue lease. A key-value engine has neither
the predicate update nor the return, so it becomes query-then-conditional-write
per item, and the claim stops being atomic across the set.

Two corrections to operation 2 as well: invite redemption and `/always`
condition ONE row and atomically write OTHERS, so the operation is a guarded
BATCH — a condition check plus a bounded set of writes — not a single-row
conditional write. That shape is native (`TransactWriteItems` allows a
`ConditionCheck` on an item it does not modify), but the bound is 100 actions.

## The seam is TWO primitives, and the rest are protocols

A design review against the code cut the five to two. Everything below stands
as the analysis that produced them; this is the interface.

```
Query(shape)                 // shape carries an exact or prefix predicate
GuardedBatch(checks, writes) // conditions on items it may not modify, plus writes
```

Each driver implements those two and nothing else. The other three are
PROTOCOLS — package-level functions written once, over the two primitives,
shared by every driver:

- **Containment is not an operation, it is a predicate.**
  `groupfolder.SQLSubtree` returns `WHERE` text that callers embed inside other
  reads and mutations (`groupfolder/folder.go:183`, `routd/reads.go:109`,
  `store/folder_scope.go:33`). So `Query` takes an exact-or-prefix predicate
  argument; containment never needed a method of its own.
- **The versioned replace** is `Query`, a start-marker CAS, N guarded writes,
  then a finish CAS. It cannot be one batch because `resreg.Apply` processes an
  unbounded row set (`resreg/engine.go:819`, `:834`). It also has exactly ONE
  domain consumer — `Apply` itself; `store.DeleteGroup` is an unversioned
  subtree purge (`store/groups.go:113`). One caller is a function, not an
  interface member.
- **The reachability guard** is a graph `Query` plus a version-guarded batch.
- **The claim-and-return** is `Query` plus a per-row guarded batch, which is
  the form `routd/pending_actions.go:421` already uses.

**One check has no SQL twin, and the driver refuses it rather than faking it.**
A `Check` on an item that must NOT exist is native to DynamoDB — a
`ConditionCheck` with `attribute_not_exists`. SQL cannot match it. At Postgres
READ COMMITTED, `SELECT … FOR UPDATE` locks the rows a query FOUND, and an
absent item has none, so a concurrent insert is invisible and the check passes
for both racers. `objstore/sqlstore` therefore returns an error naming the
substitute: a `Put` carrying its own absent-condition, where the engine's own
uniqueness enforces it. An interface that quietly accepted the check would let
a caller depend on a guarantee one driver does not have, which is the exact
drift the conformance suite exists to catch — so the asymmetry is a loud
refusal instead of a silent race.

**The constraint this exposes, and it is not negotiable.** Exact set-wide
atomicity — "these N rows change together or none do", for unbounded N —
cannot have an identical DynamoDB driver. The interface therefore defines
per-item claims and marker-gated replace, and every caller is written to those
semantics on BOTH drivers. A Postgres driver that quietly offers stronger
guarantees would let callers depend on something the other driver cannot honour,
which is the drift the conformance suite exists to catch.

**Five shapes the two primitives do not yet cover**, each measured: plural
condition checks (`store/pairing.go:114`), unbounded bulk mutation
(`store/secrets.go:372`), computed post-images (`store/routes.go:58`),
generated ids, and result-dependent audit writes — `runAudited` builds its
`audit.Event` after the mutation so it can carry affected-rows
(`store/audit_helpers.go:10`). They are normalised by client-supplied ids,
precomputed audit data, paging, and CAS batches. Schema migration stays
driver-local.

## What authd asks, and what the two primitives owe it

`6/37` derives the interface from one real daemon rather than from nine, and
`authd` is that daemon: 14 statements, 4 tables, its own database, and the
system's hardest small invariant — a refresh token that may be redeemed exactly
once. `authd/authstore/` states its questions; this is what each one costs.

**Eleven of fifteen methods are `Query` or a one-item `GuardedBatch`.**
`RefreshTokenBySecret` is a point read on the hash. `SigningKeysWithMaterial`
and `SigningKeyMetadata` scan one small table with a projection.
`OAuthIdentityForSub` is a point read plus that user's identity list.
`StartRefreshFamily` is one put. `LinkOAuthIdentity` is two puts in one batch,
because there must be no identity row without its user row.

**`ClaimRefreshToken` is the guarded transition, exactly.** `UPDATE
refresh_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND
revoked_at IS NULL`, and the winner is the caller that changed one row. Both
conjuncts are load-bearing (`F36`): the first picks one winner out of N
concurrent redeems, the second refuses a token whose family died between the
caller's lookup and the write. On DynamoDB that is one `ConditionExpression`
with two `attribute_not_exists` terms, and the loser gets
`ConditionalCheckFailed`. It ports with no redesign.

**`ClaimAndRotateRefresh` is `GuardedBatch` at its intended size** — claim the
presented token and insert its successor together, or neither. Two rows, one
account, one region, well inside `TransactWriteItems`' hundred. It ports.

**Two do not port as written, and each names a modelling gap rather than an
engine limit.**

1. **`SessionsByFamily` runs two correlated subqueries** — `COUNT(*)` and
   `MIN(issued_at)` over every row sharing a `family_id` — to report how many
   rotations a login has had and when it started. No key-value engine answers
   that. Both numbers are known at write time by the rotation that already
   writes the successor, so they belong on the row as fields. The
   denormalisation costs nothing: the write happens either way.
2. **`RevokeFamily` is a conditional UPDATE over a whole family** and returns
   how many rows it killed. The family has no item of its own, so revocation
   has to reach every row of an unbounded set — the shape §3 already refuses.
   The fix is not a protocol, it is a missing entity: **a refresh family is a
   thing, and revocation is one field on it.** Then killing a family is one
   guarded write, reuse detection is one read, and the returned count stops
   being the answer to "did this work" — which it never should have been,
   because re-revoking a dead family legitimately affects zero rows.

**What that buys.** One daemon, chosen for being small, produced one
denormalisation, one genuinely missing entity, and zero changes to the two
primitives. The interface holds. The `refresh_families` entity is the first
thing the port ADDS rather than moves, and it is an improvement arizuko would
want under SQL too: `revoked_at` on 1 row instead of on every row a login ever
rotated through.

## What gets on the surface, and the test that keeps it small

The surface was widened once and had to be trimmed back the same day, so the
rule is written down rather than re-derived.

**An operation goes on the interface only when a PRODUCTION call site needs it
and the caller cannot do it over what is already there.** Not a test, not a
conformance body, not a helper this package wrote for itself. Two independent
reviews of the widened surface found the same thing: `BetweenKey`, the exposed
page (`After`, `More`, `Examined`), the attributed refusal and the conditional
whole-item replace were each consumed by exactly one thing — `objstore`'s own
protocol helpers and its own tests. A type that exists to serve the package
that defines it is accretion, and accretion is how a two-primitive seam becomes
a database API nobody chose.

What survived that test, and why:

- **Four sort-key inequalities** — `Gt`, `Ge`, `Lt`, `Le`. Four measured
  callers: the poll loop's cursor (`routd/db.go:315`), history
  (`routd/reads.go:28`), bot activity (`routd/db.go:1181`) and the retry feed
  (`routd/db.go:406`). Without them every history read is a whole-partition
  transfer; the largest chat partition measured is 10,688 rows.
- **`Write.Remove`** — attribute removal. An empty string is PRESENT, not
  absent, so writing one collapses the `Absent`/`Present` pair the interface
  already carries. `store/onboarding.go:262` sets a status, removes five
  admission fields and preserves pairing in one write; query-plus-replace loses
  a concurrent attribute and delete-plus-put cannot share a batch, because one
  item may carry only one action.

What did not, and what replaces it:

- **`Between`** has no caller. No statement anywhere in `store`, `routd`,
  `onbod`, `dashd` or `resreg` uses a two-sided range. It returns when one does.
- **The exposed page.** `Query` returns the COMPLETE result and a driver pages
  internally to produce it. DynamoDB's 1 MB cap and its filter-before-cap rule
  do not go away — they become the driver's problem, which is where they
  belong. `Limit` is the caller's bound. A streaming caller would change this;
  none exists.
- **The attributed refusal.** `GuardedBatch` answers `committed` and nothing
  else. `authd/authstore/refresh_tokens.go` deliberately collapses "another
  claimant won" and "the family is dead" into one answer, because from the
  caller's side both are reuse. The one consumer of a richer answer was
  `Increment`, and it re-reads on the path that already lost a race.
- **The conditional whole-item replace.** Production's conditional puts are
  create-if-absent. The whole-replace case existed only in a conformance body.

**Atomic increment is a protocol, not an operation, and the reason is the type
system.** DynamoDB's in-place increment needs a Number — `SET Price = Price +
:p` — and `ADD` supports only numbers and sets. Every value here is a string.
So the operation does not exist on the engine this interface is measured
against, and putting it on the interface would mean either a typed attribute
model or a method only the SQL driver honours. `objstore.Increment` reads,
writes under the value it read as its condition, and retries the lost race.

## The consistent multi-read, and why it is a protocol

`resreg.ExportSnapshot` wraps N reads in one transaction so a subsystem document
is one MVCC snapshot rather than thirteen unrelated ones. `objstore` has `Query`
and nothing else, so the obvious move is to add a snapshot or a read
transaction. **It cannot be added.** DynamoDB has `TransactGetItems`, which
fetches up to 100 items BY KEY; it has no snapshot across N table reads, and an
operation one driver cannot express does not go on the interface.

The answer is the read twin of §3's versioned replace, and it needs no new
shape. Read the scope's version marker, run the N queries, read the marker
again: if it moved, a writer committed during the read and the export retries.
That is one extra point read per export, it detects exactly the interleaving a
transaction would have prevented, and it is expressible on both drivers.

What it does not give is a BLOCKING snapshot — a busy scope could retry more
than once. `resreg` already carries the ingredient: `ExportSnapshot` computes a
checksum per subsystem, so a moved marker and a changed checksum are the same
signal, and the export is already a read the operator repeats when it fails.

## What a registered resource still needs before routd can move

`resreg` dispatches per resource, which let onbod's three move alone. It does
NOT let routd's thirteen move one at a time: `resreg/entity.go` refuses a
subsystem that mixes shapes, because `Apply` is atomic per subsystem and a
half-entity apply would roll the columns back while leaving the items written.
So routd is one cutover of thirteen, and these four contract questions are what
it waits on. Each is answered here so the cutover has no open decision in it.

**1. A coordinate can be composite.** `EntitySortKey` names one field, and two
resources have no single-field identity: `network_rules` is keyed
`(folder, target)` and `target` alone is not unique in live data — 15 rows over
10 targets on krons — and `installed_packages` is `(folder, name)`. The sort key
takes a LIST of fields joined by a separator no value may contain, which is what
`chanlib/chanstore` and `store/pane_sessions` already do (`team/user/thread`).
The separator is declared, and a value containing it is refused at write time
rather than silently splitting a key.

**2. A stamped field is the writer's, not the engine's.** Six resources declare
`StampedFields` so the engine fills `created_at` on insert (`F49`). An entity
has no engine between the caller and the item, so the storage layer stamps it
with `core.TS` — the same layout `OrderedSort` enforces — and the declaration
stays, because it is what tells an operator's manifest the field is not theirs
to set.

**3. A non-string field needs a declared codec, and this is not optional.**
Every attribute value is a string, so `proxyd_routes`' `preserve_headers
[]string` and `strip_prefix bool` do not survive without one — and its two
`Hooks` are exactly that codec today, not null handling. Dropping them strips
preserved headers from every proxied route, read once per request over 26 rows.
So `RowType` declares an encoding per non-string field, applied on both paths,
and a field with neither a string type nor a codec is refused at registration.

**4. The scoped delete moves inside the replace.** Five of the six simplest
resources sit in `folderScopePurge` or claim a foreign-key cascade, and an
entity has neither a table to delete from nor a cascade (`L253`). A folder purge
becomes `Query` on the folder prefix plus guarded deletes, and for `Apply` those
deletes ride inside `Replace`'s claimed scope rather than beside it. Without
this the next tenant of a folder path inherits the last one's egress rules, cron
tasks, held actions and web routes.

**5. A `BeforeInsert` default is the writer's, like a stamp.** `acl`, `groups`
and `scheduled_tasks` use a hook to fill a default the operator may omit —
`effect='allow'`, `product='assistant'`, `status='active'`. That is the same
answer as the stamped field: an entity has no engine between caller and item, so
the storage layer applies the default, and the declaration stays because it is
what tells a manifest the field is optional.

**6. A `RowFilter` is a declared predicate, not a hook.** `6/40` refused it
because a reader-side filter cannot narrow a `DeleteScope`, and a half-answer
would silently widen an apply's delete. The scoped delete is now a declared
predicate chosen at registration, and `RowFilter` takes the same form: one
declaration the read and the delete both honour, so they cannot disagree.
`route_tokens`' `kind = 'route'` is a `Where`. `acl_membership`'s
`(added_by IS NULL OR added_by != 'pairing')` is not — `Where` is a conjunction
— so it is the one that becomes an attribute the writer sets, which is what it
was describing all along.

**The four coordinates that looked missing, measured 2026-09-08.** Each is a
composite under rule 1, and the separator refusal built for it is what makes
them safe going forward rather than merely safe today.

- `acl` — the six-field identity joined. No value on any of the three live
  instances contains the separator.
- `acl_membership` — `child` and `parent` joined. Same, zero hits.
- `route_tokens` — `token_hash`, which IS the key and is unique on all three
  instances. It is a credential digest, so the `RowType` carries it under
  `yaml:"-"` and `json:"-"`: in the row for the engine to key on, out of every
  document and every read surface. `PKFields` names it too — the coordinate and
  the manifest identity are separate declarations, but a PK narrower than the
  coordinate files two items under one identity, which is what `L260` cost.
- `routes` — `(seq, target, match)`, unique on all three. `seq` is an integer,
  so it takes the codec and a padded rendering, because string order must be
  numeric order.

Measuring today's rows is not the guarantee; the refusal is. A write whose value
carries the separator fails rather than splitting a key, and that check is
mutation-proven.

**And one precondition that is not a contract question.** A `RowType` that
declares fewer columns than the table has is survivable under the column path
and is data loss under an entity, because a `Put` replaces the whole attribute
set. `pending_actions` declares 12 of 18 (`L265`). Every resource's declaration
is checked against the live schema before the cutover, not during it.

## `Merge`, the write op both engines have and the surface had no name for

Re-measured 2026-09-12: the 47 transaction sites `6/37` counted are **20**, and a
verdict on each says most are a `GuardedBatch` wearing a `*sql.Tx`. Three are
already vestigial — they wrap a single conditional write. `resreg.Apply` is off
the list entirely; it adopted the versioned replace and opens no transaction.

**One missing write op is why seven of the twenty still hold a transaction.**
`Update` means must-exist, deliberately, because the refusal IS the
compare-and-swap. That left create-or-merge with no spelling, so five call sites
spell it as two writes on one item — `putSecret`, `putGroup`, `upsertReplyState`,
`RecordProactiveFireInTx` and `putMessage`'s chat-open — and `ValidateBatch`
forbids two actions on one item in a batch, which forces the transaction.

So add **`Merge`**: merge the attributes into the item, create it when absent,
never refuse. It is `UpdateItem` on DynamoDB natively, and on SQL it is one
statement from fragments already in the dialect — `ON CONFLICT DO UPDATE SET
attrs = json_patch(attrs, excluded.attrs)` on SQLite, `attrs ||
excluded.attrs::jsonb` on Postgres. `Update` keeps must-exist, so every existing
CAS is untouched.

**`Merge` needs one companion field, `Init`.** A single attribute map cannot say
"set this every time, but only stamp that on creation", and four sites need
exactly that: `putGroup`'s `added_at`, `SetLastReply`'s `engaged_folder`, and
the membership and ACL writes that must not restamp who granted what. Without
it, converting `putGroup` silently changes what `added_at` MEANS, and the
`AddEdge` adoption is impossible — `RedeemPairing` with a guarded edge write
refuses whenever a jid is re-paired to the parent it already has, and leaves the
token unspent. `Init` is DynamoDB's `if_not_exists` and one more bind parameter
in the same SQL statement. The interface is still two methods.

**What it makes impossible:** a half-upserted item. And transitively it is the
precondition for moving four callers back onto `AddEdge`, because a batch may
name each item once — so `CreateGroupWithAdmin`, `createWorldTx`, `PutGroupRow`
and `RedeemPairing` can only become single `AddEdge` calls once their upsert is
one write. That move restores the cycle guard on PostgreSQL, which
`store/membership.go` records as currently resting on SQLite's single writer
(`L256`).

**A trap worth recording, because a review nearly shipped it.** Collapsing
create-or-merge into one unconditional `Put` looks equivalent and destroys data:
a `Put` is a whole-item replace, and a secrets item carries six attributes of
which surrogate OAuth owns two. The same trap sits in `putGroup`'s settings
document and `upsertReplyState`. `Merge` exists precisely so nobody reaches for
`Put` here.

**Measured after the adoption: 20 transaction sites became 7**, and the tree
lost 91 lines of non-test Go. What is left is `runAudited` (which carries
`DeleteGroup`'s unbounded purge), `resreg.invoke`, the system-message drain,
`cmdResolveHold`, `consumeInvite`, and two in dashd's task writer.

**Six transactions cannot go, and the list is short enough to state.** The
migration critical section (DDL precedes entities); `resreg.invoke`, whose
`Handler` performs writes rather than describing them — a contract redesign, not
a refactor; `DeleteGroup`'s unbounded subtree purge; `SyncProxydRoutes`'
result-dependent count; the unbounded half of the system-message drain; and two
legacy backfills that carry a `DROP TABLE` and are deletable rather than
fixable.

## Portability, stated once

**First, what this table is not.** arizuko is NOT key-value ready, and `6/37`
§"The seam is `database/sql`, and it is one opener" decided not to be.
The seam is `db_utils.Open`, which returns a `*sql.DB` — a SQL handle. No
key-value engine can sit behind that type, so the question is never "does the
seam carry DynamoDB", it is "what would a rewrite cost". This table is that
estimate.

Two things make the estimate larger than the table alone suggests. The
migration apparatus — seven owners, 156 versioned DDL files, keyed
`(service, version)` — has no counterpart in a key-value engine, which has no
DDL past creating a table and its keys. It would be deleted, not ported, and
with it goes the mechanism that makes a schema change reviewable. And the four
owner databases are four consistency boundaries chosen because a transaction
cannot span them; a key-value engine has one boundary per item, so every
cross-table invariant in the census below becomes application code.

| Operation          | Key-value engine                                                                     |
| ------------------ | ------------------------------------------------------------------------------------ |
| containment        | native: `begins_with` on a sort key under a tenant partition                         |
| guarded batch      | native: a `ConditionCheck` plus writes, bounded at 100 actions                       |
| versioned replace  | NOT native: unbounded scope, read-then-delete, and the read is not part of the write |
| reachability guard | NOT native: an unbounded graph read decides the write                                |
| claim-and-return   | NOT native: no predicate update, no return over a set                                |
| audited write      | native only with tombstoned deletes, a two-shape row, and a time-bounded sweep       |

Three of the five block a key-value move, not one. The versioned replace is
also the operation needing `pg_advisory_xact_lock` on Postgres, and that
coincidence stands: `replace` is not a storage call, it is a protocol over a
lock and a sequence of bounded writes, and it should be named as one.

The first draft of this spec claimed three operations and that the audited
write folded in cleanly. Both were wrong, and the code said so — the sections
above record what the review found rather than the shape that was tidier.

## Ties

`6/37` (this replaces its tier-2 candidate list; the transaction census
these three are derived from) · `5/33` (the path is the coordinate) ·
BUGS `L122` (why the prefix is `substr`), `L44` (the pre-image rollback the
versioned replace carries), `L198` (the group delete with two halves).
