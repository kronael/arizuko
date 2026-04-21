---
name: announce-migrations
description: Root-only fan-out of per-migration upgrade notes. Use when a system message with origin=migration lands, or when asked to "announce migrations", "dispatch upgrade notes", "send pending announcements".
---

# Announce migrations

Root group owns fan-out of migration upgrade notes to every jid arizuko
talks to. Delivery is tracked per-(service, version, jid) in the
`announcement_sent` ledger. The incoming `migration/pending` system
message carries full bodies as `<announcement service="..." version="...">`
blocks — that is the source.

## Root-only

```bash
[ "$ARIZUKO_IS_ROOT" = "1" ] || { echo "ERROR: root-only"; exit 1; }
```

## Wiring

```bash
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT
DB=/workspace/store/messages.db
```

## 1. Parse announcements from the triggering sysmsg

Split the sysmsg body on `<announcement service="S" version="V">…</announcement>`
blocks. For each, fan out the inner body.

## 2. Iterate known outbound jids

```bash
jids=$(sqlite3 "$DB" "SELECT jid FROM chats")
```

## 3. Fan out per (service, version, jid)

Skip jids already in `announcement_sent`:

```bash
for jid in $jids; do
  sent=$(sqlite3 "$DB" \
    "SELECT 1 FROM announcement_sent
     WHERE service='$SERVICE' AND version=$VERSION AND user_jid='$jid'")
  [ -n "$sent" ] && continue

  mcpc @s tools-call send_message jid:="$jid" text:="$BODY"

  sqlite3 "$DB" \
    "INSERT OR IGNORE INTO announcement_sent
     (service, version, user_jid, sent_at)
     VALUES ('$SERVICE', $VERSION, '$jid', datetime('now'))"
done
```

## 4. Notify inner groups

After fan-out, drop a one-liner `system_message` into every active inner
group (origin=`migration`). Agents react on next turn.

```bash
for folder in $(sqlite3 "$DB" \
  "SELECT folder FROM groups WHERE parent IS NOT NULL AND parent != ''"); do
  sqlite3 "$DB" \
    "INSERT INTO system_messages (group_id, origin, event, body, created_at)
     VALUES ('$folder', 'migration', 'applied',
             'arizuko upgraded: $SERVICE v$VERSION', datetime('now'))"
done
```

## Failure modes

- Adapter offline → `send_message` queues in outbox, normal retry path.
- Crash mid-fanout → ledger has per-jid rows, re-run skips them.
- No `<announcement>` blocks in sysmsg → nothing pending, skip.
