# History Backfill

All channel adapters MUST fetch and store messages that arrived while
the adapter was offline. On startup, each adapter backfills unfetched
messages before entering the normal polling/streaming loop.

## Contract

```
POST /v1/messages  (to gated)
```

Backfilled messages use the same inbound endpoint as live messages.
The `timestamp` field carries the original platform timestamp so
gateway ordering is preserved. Adapters MUST deduplicate against
their own cursor/offset state — never re-send already-delivered
messages.

Each adapter persists a **high-water mark** (HWM) — the timestamp or
platform-specific cursor of the last successfully delivered message.
On startup: fetch everything after the HWM, deliver to gated, then
update the HWM. If no HWM exists (first run), fetch up to 7 days or
1000 messages, whichever is less.

## Per-Platform

| Platform | Backfill | Method                                  | Depth            | HWM storage          |
| -------- | -------- | --------------------------------------- | ---------------- | -------------------- |
| Telegram | Yes      | `getUpdates` offset resume              | 24h (API limit)  | offset file (exists) |
| Discord  | Yes      | `GET /channels/{id}/messages?after=HWM` | unlimited        | state file           |
| WhatsApp | No       | exception — Baileys sync is unreliable  | —                | —                    |
| Reddit   | Yes      | listing pagination with `after`         | ~1000 items      | state file           |
| Bluesky  | Yes      | cursor-based `listNotifications`        | no known cap     | state file           |
| Mastodon | Yes      | `GET /api/v1/notifications?since_id=`   | server-dependent | state file           |
| Email    | Yes      | `SEARCH SINCE <date>` (already works)   | unlimited        | IMAP seen flag       |

### Telegram

Bot API only retains unconfirmed updates for 24 hours. The existing
offset file already handles this — if the adapter restarts within 24h,
unprocessed updates are re-fetched. No additional work needed beyond
ensuring the offset file is persisted across container restarts.

### Discord

`GET /channels/{channel.id}/messages` with `after=<last_message_id>`.
Paginate with `limit=100`, follow `after` cursor. Rate limit: ~5 req/s
per route. On first run, fetch last 7 days or 1000 messages.

Requires `VIEW_CHANNEL` + `READ_MESSAGE_HISTORY` permissions.

### WhatsApp

Exception. Baileys `shouldSyncHistoryMessage` and
`messaging-history.set` are buggy and unreliable across reconnects.
WhatsApp history sync may be revisited if Baileys stabilizes, but is
not required for this spec.

### Reddit

Listing endpoints (`/message/inbox.json`, `/r/{sub}/new.json`) with
`before`/`after` fullname pagination. Hard cap ~1000 items per source.
Rate limit: 100 req/min.

### Bluesky

`app.bsky.notification.listNotifications` with cursor pagination.
Filter to mentions/replies relevant to the bot. No documented depth
cap.

### Mastodon

`GET /api/v1/notifications` with `since_id=<HWM>` and `min_id` for
forward pagination. Max 80 per page. Filter by type: `mention`.

### Email

Already implemented. `SEARCH UNSEEN` fetches all unread messages on
startup. Could be enhanced to `SEARCH SINCE <date>` for date-based
backfill, but current behavior is sufficient — IMAP unseen flag is a
natural HWM.

## HWM State File

Adapters store HWM in the data dir:

```
/srv/data/arizuko_<instance>/<adapter>-hwm-<name>
```

Format: single line, platform-specific cursor (message ID, timestamp,
or offset). The file is read on startup and written after each
successful batch delivery.

Telegram already uses this pattern (`teled-offset-<name>`). Other
adapters should follow the same convention.

## Ordering

Backfilled messages are delivered oldest-first. The gateway's
`messageLoop` processes them in timestamp order, same as live messages.
Agents see a continuous timeline regardless of whether messages arrived
live or via backfill.

## Deduplication

Adapters MUST set the platform message ID as the `id` field in the
POST body. The store uses `INSERT OR IGNORE` on the messages table
primary key, so duplicates are silently dropped.

## Media

Backfilled messages with attachments follow the same media pipeline as
live messages — adapter sends URL/data, gateway enricher downloads and
writes to `groups/<folder>/media/<YYYYMMDD>/`.
