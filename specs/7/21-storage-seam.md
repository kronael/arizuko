---
status: shipped
shipped: 2026-09-05
depends:
  [
    ../6/7-environment-contracts,
    ../5/16-mcp-rest-unification,
    ../5/33-paths-roles,
  ]
---

# specs/7/21 — the storage seam: `database/sql` is the seam

Decision: arizuko writes no `Store` interface. The component contract
(`6/7`) says a daemon must "own and migrate your schema"; the seam it does
that through is Go's `database/sql`. A backend question is a choice of
engine and dialect behind that seam, never a data-model rewrite above it.

The transaction census below is the evidence for both halves: what the
schema requires of an engine, and why an engine that keeps it already
exists for every cloud target a spec names.

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
across tables — which is every SQL engine on the list below.

## Why a `Store` interface is the wrong seam

A storage interface abstracts over engines that differ in **data model**.
Every engine that keeps the invariants speaks SQLite's SQL or Postgres's;
what differs is the driver name and the DSN. That difference already has
one place: eight `sql.Open("sqlite", …)` sites (`db_utils.OpenWAL`,
`store/store.go`, `authd/main.go`, `dashd/main.go`, two under
`cmd/arizuko`, two under `tests/`), each carrying its own pragma string.
The one refactor the cloud track owes is funnelling those through
`db_utils` so an engine is chosen once — "one declaration, many readers",
not a second format.

A DynamoDB-shaped seam buys exactly one target, DynamoDB, and its price is
the gap list above turned into per-call-site discipline: FTS indexing
without triggers, cascades as N explicit deletes, the partial unique index
as a singleton document, aggregates as write-time counters, an audit
cursor without a database-assigned order. Root `CLAUDE.md` bans that
class of drift. No spec states a requirement that DynamoDB alone meets.

## The engines, measured

Every capability claim below is from the engine's docs or source; the two
README-only statements are marked.

| Engine                           | `BEGIN…COMMIT` kept                                                                                                                 | Go access                                                                                            | Ops                                                   | Latest                                                              | License                                   |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- | ----------------------------------------------------- | ------------------------------------------------------------------- | ----------------------------------------- |
| Litestream                       | yes — ships WAL frames after commit; never sees SQL                                                                                 | none needed; the file stays local                                                                    | one sidecar + a bucket                                | v0.5.17, 2026-08-31                                                 | Apache-2.0                                |
| libSQL (`sqld` / Turso Cloud)    | yes — `libsql-client-go` `BeginTx` sends `BEGIN`/`COMMIT` over Hrana; rejects `ReadOnly` and non-default isolation                  | `github.com/tursodatabase/libsql-client-go/libsql`, pure Go; `go-libsql` (CGO) for embedded replicas | remote primary: self-hosted `sqld` or a Turso account | core `libsql-0.10.0-pre.4`, 2026-06-02; server v0.24.33, 2025-12-19 | MIT                                       |
| dqlite + go-dqlite               | yes — a real `database/sql/driver`, Raft-replicated commit                                                                          | `github.com/canonical/go-dqlite`; CGO with libdqlite, raft, libuv; `modernc` unusable                | in-process, 3-node quorum for HA                      | dqlite 1.18.7, 2024-07-02; go-dqlite v3.0.4, 2025-02-19             | LGPL-3.0 (dqlite), Apache-2.0 (go-dqlite) |
| LiteFS                           | yes — replicates the file; one writer, writes forwarded to the primary                                                              | unchanged driver under a FUSE mount                                                                  | FUSE + `litefs` per node + Consul lease               | v0.5.14, 2024-04-22; Fly.io states it cannot support it             | Apache-2.0                                |
| rqlite                           | **no** — literal `BEGIN`/`COMMIT` is undefined behaviour (docs); `gorqlite/stdlib` implements `Begin`/`Commit`/`Rollback` as no-ops | HTTP; the `database/sql` shim compiles every transaction into a silent non-transaction               | `rqlited` per node, odd quorum                        | v10.3.1, 2026-09-04                                                 | MIT                                       |
| PostgreSQL (RDS, Neon, Supabase) | yes, on the primary; replicas read-only                                                                                             | `jackc/pgx`; a dialect port of every call site                                                       | a server, or a hosted account                         | 18, 2025-09-25                                                      | PostgreSQL License                        |
| CockroachDB                      | yes, serializable                                                                                                                   | Postgres drivers; the same dialect port plus CRDB's own gaps (no FTS)                                | ≥3 nodes or Cockroach Cloud                           | v26.2.2, 2026-06-05                                                 | CockroachDB Software License, not OSI     |
| Turso (Rust rewrite)             | its own MVCC engine                                                                                                                 | `turso.tech/database/tursogo`, pure Go                                                               | local, or Turso Cloud sync                            | v0.7.2, 2026-07-30                                                  | MIT                                       |
| SQLite session extension         | n/a — captures committed changesets; single writer                                                                                  | compiled into `modernc.org/sqlite` v1.46.1 (`SQLITE_ENABLE_SESSION=1`), not wrapped in Go            | an ingredient; transport is yours                     | inside SQLite                                                       | public domain                             |
| cr-sqlite, marmot                | local yes, cross-node no — CRDT merge / eventual, out-of-order rows                                                                 | loadable extension (CGO) / MySQL wire                                                                | your transport / gossip cluster                       | cr-sqlite v0.16.3, 2025-01-17; marmot untagged (README)             | MIT                                       |

Feature survival: Litestream, LiteFS, libSQL and dqlite keep FTS5,
triggers, partial indexes, JSON, `datetime()` and cascades because each
runs an unmodified SQLite (dqlite: `ATTACH` disabled by design, `json1`
carries an open issue). Postgres has a native form of each — `tsvector`,
PL/pgSQL triggers, `CREATE UNIQUE INDEX … WHERE`, `jsonb`, `timestamptz`,
sequences — behind a dialect port. Turso's rewrite calls its FTS
experimental (tantivy, not FTS5) and its own repository says it is not
production ready (README statement).

Sources: rqlite.io/docs/api/api · github.com/rqlite/gorqlite `stdlib/sql.go`
· docs.turso.tech/sdk/go/reference and `libsql/internal/http/hranaV2/hranaV2.go`
· canonical.com/dqlite/docs and github.com/canonical/go-dqlite/releases ·
github.com/superfly/litefs `docs/ARCHITECTURE.md`, fly.io/docs/litefs ·
github.com/benbjohnson/litestream/releases · postgresql.org/docs/current/logical-replication.html
· cockroachlabs.com/docs/stable/postgresql-compatibility · sqlite.org/sessionintro.html.

## What the cloud track adopts

1. **Backup and point-in-time restore: Litestream.** No code change, no
   driver change, no schema feature lost. It is not packaged: no
   `template/services/litestream.yml` exists, and the instance backup is
   still a tar of the data dir.
2. **A daemon with no persistent disk: libSQL.** Swap the driver, keep
   every statement. One remote database per owner keeps the
   `store/<owner>/` boundary (`5/16`). The single code consequence is
   `resreg.ExportSnapshot` (`resreg/archive.go`), which opens a
   `ReadOnly` tx the remote driver rejects. Latency moves from a syscall
   to a round trip per statement; `auth.Authorize`'s live read is on
   every request, and that cost is the same under any remote store.
3. **Managed relational only if a requirement names Postgres.** None does
   today. The port is bounded and countable: 57 `INSERT OR IGNORE`, 41
   `ON CONFLICT`, 12 `json_extract`, 12 `rowid`, 10 `datetime(`, 6
   `AUTOINCREMENT`, 6 FTS `MATCH`, 5 `PRAGMA`, and `?` placeholders across
   598 call sites in 106 files.
4. **HA on own boxes: nothing.** No spec states the requirement. When one
   does, dqlite is the only engine that keeps the Go transaction idiom
   and replicates it, at the cost of a C toolchain and a release line
   that has not moved since 2025-02. rqlite is excluded for good: its
   `database/sql` shim would silently un-transaction all 26 sites.
5. **Turso's rewrite, cr-sqlite, marmot: no.** The first is pre-1.0 by
   its own account; the other two are the multi-master shape the
   invariants forbid.

## Partition keys are per-table — an access-pattern note, not a design

Kept because it states how the tables are read, which any engine's
indexes should follow. 19 of routd's 29 tables have no `folder` column.
`messages` keys on `chat_jid`, `secrets` on `(scope_kind, scope_id)`,
`audit_log` on `folder` with `""` for instance-scope rows, `acl` on a
**world**: `container/runner.go worldOf` returns the top path segment,
and `""` for an elevated turn. A scope whose first segment is a literal
(`acme/**`) belongs to `acme`; a scope starting with a wildcard (`**`,
`*/eng`) belongs to `""`. An authorization read for folder F reads those
two partitions, never a scan. `5/5` R2 already settled that a world is a
folder-shape predicate rather than a table.

## Ties

`6/7` component contract (this is its storage half) · `5/16` per-daemon
DB ownership · `5/33` grants and scope globs · `5/5` R2 (a world is a
predicate) · `7/22` (argues from the invariants; its conclusion stands
with no seam) · `6/16` (the ship-in-parts matrix).
