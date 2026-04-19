---
status: unshipped
---

# Passive listener groups

Group mode that accumulates inbound messages without per-message agent
runs. A scheduled digest task compiles and delivers a summary.

Schema:

```sql
ALTER TABLE groups ADD COLUMN mode TEXT NOT NULL DEFAULT 'active';
ALTER TABLE groups ADD COLUMN message_ttl_days INTEGER;
```

Gateway skips `EnqueueMessageCheck` when `mode='listener'`. Cursor
advances only on digest runs. Digest = `scheduled_tasks` row with cron

- prompt containing `<digest><dest>JID</dest>...</digest>`. Agent uses
  existing `send_message`. `timed` runs daily TTL cleanup on groups with
  `message_ttl_days` set.

Rationale: high-volume read-only channels (monitor a subreddit, watch
a Discord) don't need per-message agent runs; batch into scheduled
digests.

Unblockers: `Mode`/`MessageTTLDays` on `core.Group`, store reads,
gateway skip branch, timed TTL cleanup job, `--mode listener --ttl 7`
on `arizuko group add`.
