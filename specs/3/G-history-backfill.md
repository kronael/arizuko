---
status: unshipped
---

# History Backfill

Channel adapters fetch and store messages that arrived while offline.
On startup, each adapter backfills before entering the live loop.

## Contract

`POST /v1/messages` (to gated) — same endpoint as live. `timestamp`
carries original platform timestamp. Adapters deduplicate against their
own cursor state.

Each adapter persists a high-water mark (HWM). On startup: fetch
everything after HWM, deliver, update HWM. First run: last 7 days or
1000 messages (whichever smaller).

## Per-platform

| Platform | Backfill | Method                                  | Depth            | HWM                  |
| -------- | -------- | --------------------------------------- | ---------------- | -------------------- |
| Telegram | Yes      | `getUpdates` offset resume              | 24h (API limit)  | offset file (exists) |
| Discord  | Yes      | `GET /channels/{id}/messages?after=HWM` | unlimited        | state file           |
| WhatsApp | No       | Baileys sync unreliable — exception     | —                | —                    |
| Reddit   | Yes      | listing pagination with `after`         | ~1000 items      | state file           |
| Bluesky  | Yes      | cursor-based `listNotifications`        | no known cap     | state file           |
| Mastodon | Yes      | `/notifications?since_id=`              | server-dependent | state file           |
| Email    | Yes      | `SEARCH SINCE <date>`                   | unlimited        | IMAP seen flag       |

Discord needs `VIEW_CHANNEL` + `READ_MESSAGE_HISTORY`. Reddit rate
limit 100 req/min. Mastodon max 80/page, filter `type=mention`. Email
already works via `SEARCH UNSEEN`.

## HWM state file

```
/srv/data/arizuko_<instance>/<adapter>-hwm-<name>
```

Single line, platform-specific cursor. Read on startup, written after
each successful batch. Telegram already uses `teled-offset-<name>`.

## Ordering & dedup

Backfilled messages delivered oldest-first; `messageLoop` processes in
timestamp order. Adapters set platform message ID as `id`; store uses
`INSERT OR IGNORE` so duplicates drop silently.

## Media

Same pipeline as live — adapter sends URL/data, gateway enricher
downloads to `groups/<folder>/media/<YYYYMMDD>/`.
