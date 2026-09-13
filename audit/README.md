# audit

The operator's record of who did what: one appended item per event, plus the
optional JSONL sink that ships those events off the box.

## Purpose

Two things live here because they are two SINKS for one vocabulary, not two
records:

- **`audit_log`** — the canonical entity. One item per event, appended and
  never touched again. Four daemons own one each (routd, authd, onbod, runed)
  and every one serves it at `GET /v1/audit` through resreg.
- **`Audit`** — an optional JSONL file pair (`audit-system.jl`,
  `audit-web.jl`) with rotation and a webhook, off unless `AUDIT_ENABLED=true`.
  It is a stream for an external collector, not the record. If the two
  disagree, the entity wins.

The vocabulary — categories, actions, outcomes, surfaces — is closed and
declared once in `event.go`. The taxonomy behind it is `audit/PLAN.md`.

## Public doors

Recording, in the order a caller should reach for them:

- `WriteFor(e) (id, objstore.Write)` — when your own mutation is already a
  `GuardedBatch`: the audit item rides in that batch, so the mutation and its
  record commit together. This is the one that exists so a caller never
  assembles the item itself — this package owns the attribute names and the
  redaction, and a second assembler would be a second declaration of both.
- `EmitTo(ctx, store, e)` — the caller names its own store; errors returned.
- `Emit(ctx, e)` — uses the store `Init(store, instance)` wired at boot;
  errors are logged and dropped, because a failed audit write must not fail
  the user's request.
- `EmitSystem` / `EmitWeb` on an `*Audit` — the JSONL sink.

Reading:

- `Query(ctx, store, Filter) ([]Row, error)` — newest first, cursored. One
  reader, used by all four `/v1/audit` handlers, because four per-daemon reads
  of one entity would be four projections of one field list drifting apart.
- `Filter.Folder` is the CONTAINMENT bound, and empty means UNBOUNDED — every
  folder plus the folder-less instance rows. Derive it from the authorization
  the caller just passed, NEVER from a JWT claim: an operator's JWT omits the
  folder claim, but so does a tenant holding two grants, so "empty folder
  claim" is not "is an operator".
- `AllCategories` — the closed enum, exported so a UI renders the whole
  vocabulary instead of a distinct-values query that would have to ask three
  daemons what the enum already answers.

Identity: `NewID(at)` is the instant rendered at `core.TS`'s fixed width plus
an 8-hex suffix, and `IDWidth` is declared ordered. Byte order is therefore
time order, which is what lets one string be both the sort key and an exact
paging cursor — and what makes a short id impossible rather than merely wrong
(BUGS `L97`).

## Where state lives

The partition is the constant `"audit"`, not the folder and not a time bucket,
and `audit_log.go`'s header argues that from measured plans. The short of it:
163,030 of krons' 163,750 rows carry no folder at all, so a folder partition
leaves the operator's own page with no partition to name; and time buckets
give a backward walk no bound it can honestly stop at, so it would report "no
older rows" when it merely gave up.

Folder containment and the actor substring have no operator on the objstore
interface, so `page` applies them itself over `scanChunk`-sized reads. That is
not a regression: both reads already scanned.

## What it does not do

- **No retention, no purge, no delete.** Append-only by design; `MaxLimit`
  (200) is what stops one request draining the whole history.
- **No statement of its own.** `db_utils/sql_layer_test.go` fails the build if
  SQL appears anywhere in this package — the entity is a name a driver
  resolves, and the daemon that opens the store picks which driver.
- **No dependency on `chanlib`.** `envOr`/`envInt` are re-implemented here
  because that edge once closed an `auth → audit → chanlib` import cycle.
- **No cross-daemon merge.** Merging four owners' logs into one page is
  dashd's (`dashd/audit_page.go`).

## The real gaps

- `import.go` (`LegacyRow`, `CheckSnapshot`, `LegacyID`, `Import`) is the
  one-way carry of existing rows into the entity shape. Only tests call it.
- onbod's `audit_log` is not yet federated onto dashd's page (BUGS `F35`).

## Files

- `event.go` — the closed enums, `Event`, and the redaction every value passes
- `audit_log.go` — the whole storage half: the entity, the id, the writes, `Query`
- `audit.go` — the optional JSONL sink and its webhook
- `import.go` — the one-way carry into the entity shape
- `PLAN.md` — the category taxonomy and action vocabulary the enums cite

## Related docs

- `specs/5/I-tool-call-logging.md` — the event model
- `specs/6/40-store-surface.md` — the audited write in a `GuardedBatch`
