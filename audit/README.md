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
The figure of all three substrates, the record, the log and the spans, is
at the top of `obs/README.md`.

## The row and its closed enums

An item carries these attributes (`audit_log.go`); one the event does not
carry is absent, not empty, so a forensic read can ask for a missing
folder:

| attribute                                                  | holds                                                                                                  |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `category`                                                 | `authn`, `authz`, `access`, `mutation`, `system`, `network`, `channel`, `agent`, `secret`, `scheduler` |
| `action`, `resource`, `scope`                              | what was done, to what, at which scope                                                                 |
| `actor`, `actor_sub`                                       | who asked                                                                                              |
| `surface`                                                  | `mcp`, `rest`, `cli`, `gateway`, `cron`, `agent_pretool`, `agent_posttool`                             |
| `params_summary`                                           | the redacted, capped arguments                                                                         |
| `outcome`, `error_msg`, `duration_ms`                      | `ok`, `error` or `denied`, and how                                                                     |
| `turn_id`, `folder`, `instance`, `request_id`, `source_ip` | where                                                                                                  |

The two `agent_*` surfaces are declared and no writer uses them: the
agent's in-guest tool hooks print one `[ant] [tool]` JSON line to stderr
per phase (`ant/src/tool-log.ts`: `phase`, `turn_id`, `tool`,
`args_summary`, `duration_ms`, `outcome`), runed keeps it in the turn log
and logs it at debug, and nothing writes it to the store. A hook that wrote
a row would make every agent tool call a distributed transaction.

The slog line is not this shape either. resreg logs every call it serves
with `caller`, `resource`, `action`, `surface`, `outcome` and `duration_ms`
(`resreg/resreg.go` `logCall`), whether or not a row follows. When the
line and the row disagree, the row wins: the line is lossy.

Redaction runs first, then truncation: each string value is capped at 200
runes, then the whole map at 512 bytes. A key matching `password`, `token`,
`secret`, `credential`, `authorization`, `cookie`, `dsn`, `api_key` or a
`_key` suffix is replaced with `<redacted:Nchars>`. `session` is not
redacted: a session id is a turn identifier, and hiding it would break the
forensic join.

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
- **No statement of its own.** The entity is a name the driver resolves, and
  the daemon that opens the store hands this package the handle.
- **No dependency on `chanlib`.** `envOr`/`envInt` are re-implemented here
  because that edge once closed an `auth → audit → chanlib` import cycle.
- **No cross-daemon merge.** Merging four owners' logs into one page is
  dashd's (`dashd/audit_page.go`). webd, proxyd and dashd own no
  `audit_log`; each `Init`s the package on routd's store, so their rows land
  in routd's.

## The real gaps

- `import.go` (`LegacyRow`, `CheckSnapshot`, `LegacyID`, `Import`) is the
  one-way carry of existing rows into the entity shape. Only tests call it.
- The `agent_pretool` and `agent_posttool` surfaces have no writer.

## Files

- `event.go` — the closed enums, `Event`, and the redaction every value passes
- `audit_log.go` — the whole storage half: the entity, the id, the writes, `Query`
- `audit.go` — the optional JSONL sink and its webhook
- `import.go` — the one-way carry into the entity shape
- `PLAN.md` — the category taxonomy and action vocabulary the enums cite

## Related docs

- `obs/README.md` — the three substrates and the slog and OTLP half
- `resreg/README.md` — why four daemons share the resource name `audit`
- `dashd/README.md` — the federated `/dash/audit/` page
