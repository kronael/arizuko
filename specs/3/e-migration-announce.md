---
status: unshipped
---

# Migration-triggered Announcements

Automatic upgrade notifications, driven by the migration sequence. No
new daemon, no new transport.

## Shape

Each SQL migration may ship a paired markdown note:

```
store/migrations/0024-foo.sql
store/migrations/0024-foo.md   optional, user-facing changelog
```

Missing `.md` = silent (schema-only change). The `.md` body is the
literal message sent to affected groups. Plain markdown, one screenful.

```markdown
arizuko upgraded — v0.26.0

- voice replies now transcribe in under 2s
- `/remember` persists across sessions
- web dashboard at krons.fiu.wtf/dash
```

## Trigger

`db_utils.Migrate` already loops pending migrations. After a
migration's tx commits, if paired `.md` exists, it lands in:

```sql
CREATE TABLE announcements (
  service    TEXT NOT NULL,
  version    INTEGER NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (service, version)
);
```

(Bootstrap migration creates this table first.)

## Delivery

`gated` on startup drains announcements not yet fanned out:

1. Select rows without matching `announcement_sent(service, version, group_folder)`.
2. For each active group, insert outbound row into `messages` via the
   bot-message path. Router picks up; adapter ships.
3. Record `(service, version, group_folder)` in `announcement_sent`
   inside the same tx.

Groups added after migration still get the announcement first time
`gated` starts with them active — idempotent per `(version, folder)`,
not per-run.

## Targeting

Default: every active group. Migration touches shared schema, so every
group is potentially affected. Opt-out (`groups.announce_mute`) is a
future extension.

## Failure modes

- Adapter offline: outbound row sits in `messages`; existing retry
  handles it.
- Missing `.md`: silent.
- Duplicate after crash: prevented by `announcement_sent` ledger check.

## Files

- `db_utils/db_utils.go` — write `announcements` row after tx commit
  if paired `.md` is present
- `gated/main.go` or `gateway/gateway.go` startup — drain into messages
- `store/migrations/NNNN-announcements.sql` — create tables
- `store/migrations/NNNN-<feature>.md` — per release

## Out of scope

- HTML / rich formatting beyond what adapter already strips
- Per-user DM notification
- Release-manager dashboard
