---
status: shipped
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

## Trigger — files are the source

`.md` files live alongside `.sql` migrations in `store/migrations/` and
are embedded into the `gated` binary via `//go:embed migrations`. No DB
table caches bodies — `store.Announcements()` scans the embedded FS at
query time.

## Delivery — root dispatches, per-jid once

Root agent owns fanout. On startup, `gated` inserts one `system_message`
(origin=`migration`) into the root group containing every pending
announcement as `<announcement service="…" version="…">…</announcement>`
blocks parsed from the `.md` bodies. The root skill parses these blocks
and dispatches via `send_message` per jid.

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

- `store/announcements.go` — scans embedded migrations FS for `.md`
  files; `UnsentTo(jid)`, `AnyPending(jids)`, `RecordSent(...)`
- `gated/announce.go` — on startup, if any jid is still owed any
  announcement, enqueue one root `system_message` with full bodies
- `ant/skills/announce-migrations/SKILL.md` — root-only fan-out skill
- `store/migrations/NNNN-announcements.sql` — creates `announcement_sent`
  ledger (jid-keyed)
- `store/migrations/NNNN-<feature>.md` — per release

## Out of scope

- HTML / rich formatting beyond what adapter already strips
- Per-user DM notification
- Release-manager dashboard
