---
status: draft
---

# migration-triggered announcements

Automatic upgrade notifications, triggered by the same migration
sequence that evolves the schema. Minimal addition — no new daemon, no
new table, no new transport.

## Shape

Each SQL migration may ship a paired markdown note:

```
store/migrations/0024-foo.sql
store/migrations/0024-foo.md   ← optional, user-facing changelog
```

If `.md` is absent, the migration is silent (schema-only; no user-facing
change worth announcing).

The `.md` body is the literal message sent to affected groups. Plain
markdown, one screenful max. Example:

```markdown
arizuko upgraded — v0.26.0

- voice replies now transcribe in under 2s
- `/remember` persists across sessions
- web dashboard at krons.fiu.wtf/dash
```

## Trigger

`dbmig.Run` already loops over pending migrations. After a migration's
tx commits, if a paired `.md` exists in the same embedded FS, it is
recorded in a new lightweight table:

```sql
CREATE TABLE announcements (
  service    TEXT NOT NULL,
  version    INTEGER NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (service, version)
);
```

(This table itself is created by a bootstrap migration before
announcements can land in it.)

## Delivery

`gated` on startup drains `announcements` not yet fanned out:

1. Select rows from `announcements` that have no matching row in a
   per-group delivery ledger (`announcement_sent(service, version,
group_folder)`).
2. For each active group (`groups` table, `state='active'`), insert
   one outbound row into `messages` via the existing bot-message path
   — same path the agent uses for replies. The router picks it up and
   the channel adapter ships it.
3. Record `(service, version, group_folder)` in `announcement_sent`
   inside the same tx.

Groups added after the migration ran still get the announcement the
first time `gated` starts with them active — delivery is idempotent
per `(version, folder)`, not per-run.

## Targeting

Default: every active group on the instance. A migration touches the
shared schema, so every group is potentially affected.

Opt-out per group is a future extension (`groups.announce_mute`).
Not in scope for the minimal version.

## Failure modes

- Adapter offline at send time → the announcement sits in `messages`
  like any other outbound row. The existing retry/queue path handles
  it.
- `.md` missing despite convention → silent. No warning. Migrations
  without user-facing change don't need one.
- Duplicate fan-out after a crash → prevented by `announcement_sent`
  ledger check before insert.

## Out of scope

- No HTML, no rich formatting beyond what the channel adapter already
  strips.
- No per-user notification — group broadcast only. DMs can be added
  later by reusing the same path with a different target selector.
- No release-manager dashboard. `make image && deploy` is enough;
  the migration sequence is the source of truth.

## Files touched

- `dbmig/dbmig.go` — after tx commit, write `announcements` row if
  paired `.md` is present
- `gated/main.go` (or `gateway/gateway.go` startup) — drain unsent
  announcements into `messages` on boot
- `store/migrations/NNNN-announcements.sql` — create
  `announcements` + `announcement_sent` tables
- `store/migrations/NNNN-<feature>.md` — per-release, written
  alongside the schema change that introduces the feature
