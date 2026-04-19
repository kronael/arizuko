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

## Delivery — root dispatches, per-jid once

Root agent (the `root` group) owns fanout. It has the `arizuko` binary
CLI and the authority to speak on each channel's behalf. On startup,
`gated` inserts a single message into the `root` group describing which
migrations have pending announcements; the root agent reads the `.md`
bodies and dispatches via its normal outbound MCP tools (`send_message`
per jid).

The `announcement_sent(service, version, user_jid)` ledger keys by **jid**,
not by group. Each jid that arizuko talks to gets each announcement
exactly once, regardless of how many groups share that jid.

```sql
CREATE TABLE announcement_sent (
  service   TEXT NOT NULL,
  version   INTEGER NOT NULL,
  user_jid  TEXT NOT NULL,
  sent_at   TEXT NOT NULL,
  PRIMARY KEY (service, version, user_jid)
);
```

Root runs the fanout loop itself: select pending announcements, iterate
over all known jids, skip those already in `announcement_sent`,
`send_message`, write ledger row. Inner (non-root) groups are **not**
involved in sending — they just get notified that their underlying
arizuko instance changed.

## Targeting inner groups

Simplest: root notifies inner agents too, via a short `system_message`
insertion into each active group's message stream (origin=`migration`,
one line per upgrade). Inner agent reads on next turn, reacts however
it wants (re-read skills, note in diary). This keeps the migration
flow one-directional: root dispatches, everyone else receives.

Deferred to inner agents to migrate themselves: no. Root does it
completely for now.

## Failure modes

- Adapter offline: outbound row sits in `messages`; existing retry
  handles it.
- Missing `.md`: silent.
- Duplicate after crash: prevented by `announcement_sent` ledger check.

## Files

- `db_utils/db_utils.go` — write `announcements` row after tx commit
  if paired `.md` is present
- `gated/main.go` — on startup, insert one system message into the
  root group listing pending announcements
- Root agent's CLAUDE.md / a skill — handles dispatch via `send_message`
  - writes to `announcement_sent`
- `store/migrations/NNNN-announcements.sql` — create tables
  (`announcements` + `announcement_sent` keyed by jid)
- `store/migrations/NNNN-<feature>.md` — per release

## Out of scope

- HTML / rich formatting beyond what adapter already strips
- Per-user DM notification
- Release-manager dashboard
