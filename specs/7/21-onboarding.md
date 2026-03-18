# onbod

**Status**: design

Onboarding daemon. Polls `onboarding` table, runs state machine,
sends replies via channel adapter HTTP API. Standalone service
like timed — reads/writes shared SQLite DB, no gateway code.

## User journey

1. User finds the bot, sends "hello"
2. No route exists — gated inserts into onboarding table (status: awaiting_name)
3. onbod picks it up, replies: "Pick a name for your workspace:"
4. User types: "alice-studio"
5. onbod validates, stores, notifies root
6. onbod replies: "Got it! Waiting for approval."
7. Operator approves — world created, route added
8. User sends next message — routes to their world
9. Agent wakes with welcome system message

## Admin journey

1. Enable: `ONBOARDING_ENABLED=1` in `.env`
2. Root gets notification: "alice wants 'alice-studio'
   — `/approve telegram:-12345`"
3. `/approve telegram:-12345` — creates world, routes JID
4. `/reject telegram:-12345` — suppresses forever

## Gateway hook

gated's only role: when no route found and `ONBOARDING_ENABLED=1`,
write to onboarding table. gated does NOT run the state machine.

```go
if group == nil && cfg.OnboardingEnabled {
    insertOnboarding(chatJid, sender, channel)
    return
}
```

Messages from unrouted JIDs with existing onboarding entries
are written to the messages table normally. onbod reads them
using: `SELECT * FROM messages WHERE chat_jid = ? AND
is_bot_message = 0 ORDER BY timestamp DESC LIMIT 1`
to find the latest user message for each onboarding JID.

## State machine

onbod polls onboarding table every `ONBOARD_POLL_INTERVAL` (default 10s).

```
awaiting_name, prompted_at IS NULL:
  -> send "Pick a name for your workspace:"
  -> SET prompted_at = now

awaiting_name, prompted_at IS NOT NULL:
  -> query latest user message: SELECT content FROM messages
       WHERE chat_jid = jid AND is_bot_message = 0
       AND timestamp > prompted_at ORDER BY timestamp DESC LIMIT 1
  -> no message yet: skip
  -> message found:
     -> validate (a-z0-9-, not taken, not reserved)
     -> invalid: send "Try again — lowercase letters, numbers, hyphens only"
                 SET prompted_at = now (re-prompt)
     -> valid:   SET status=pending, world_name=<input>
                 notify() root: "<jid> wants '<world_name>' — /approve <jid>"
                 send "Got it! Waiting for approval."

pending (on every new message from this jid while status = pending):
  -> same message query as above (timestamp > last_sent or prompted_at)
  -> message found: send "Still waiting for approval."

rejected:
  -> silence (no reply)

approved:
  -> skip (handled by /approve, nothing to poll)
```

## State table

```sql
CREATE TABLE onboarding (
  jid         TEXT PRIMARY KEY,
  status      TEXT NOT NULL,  -- awaiting_name | pending | approved | rejected
  sender      TEXT,
  channel     TEXT,
  world_name  TEXT,
  prompted_at TEXT,           -- ISO8601; NULL until initial prompt sent
  created     TEXT NOT NULL
);
```

## Commands

onbod handles `/approve` and `/reject`. gated routes these via
the channels table — onbod receives them as HTTP POST to its
`/send` endpoint. Routing table entries seeded by onbod at
startup via DB upsert (INSERT OR IGNORE):

```sql
INSERT OR IGNORE INTO routes (jid, seq, type, match, target)
  VALUES ('*', -10, 'command', '/approve', 'onbod'),
         ('*', -10, 'command', '/reject',  'onbod');
```

`jid = '*'` is a wildcard matching any source JID (evaluated
by gated when no exact-match route exists for the source JID).

### Tier-0 enforcement

onbod receives the sender's `chat_jid` in the HTTP POST payload.
It verifies root-only access by querying the routes table:
`SELECT target FROM routes WHERE jid = ? AND seq = 0 LIMIT 1`
to find the target folder for the sender's JID, then checks
`SELECT parent FROM registered_groups WHERE folder = ?` — if
`parent IS NULL`, the sender is tier 0 (root). Otherwise reject
with "Permission denied."

### /approve <jid>

- Root-only (tier 0)
- Reads `world_name` from onboarding table
- Creates world folder: `groups/<world_name>/`
- Copies prototype from `groups/root/prototype/` if `ONBOARDING_PROTOTYPE` set,
  or from `ONBOARDING_PROTOTYPE` path if configured
- Inserts group in DB (tier 1)
- Adds routes: default (seq 0), `@` (seq -2), `#` (seq -1)
- Grants: tier 1 defaults (see action-grants spec)
- Enqueues welcome system message
- Sets onboarding status to `approved`
- notify(): "Approved: alice -> alice-studio/"

Welcome system message enqueued for the new group:

```xml
<system origin="gateway" event="onboarding">
  <user jid="telegram:-12345" />
  <group folder="alice-studio" tier="1" />
  <instructions>
    This is a new user's first interaction.
    1. Run /hello to welcome the user.
    2. Run /howto to build a getting-started web page for them.
  </instructions>
</system>
```

### /reject <jid>

- Root-only
- Sets status to `rejected`
- notify(): "Rejected: <jid>"

## HTTP /send endpoint

onbod's `/send` endpoint accepts the same payload gated sends to
any channel adapter:

```json
{ "jid": "<chat_jid>", "text": "<message content>" }
```

`jid` is the source chat JID of the incoming message (e.g.
`telegram:-12345`). `text` is the raw message text. This is how
gated delivers `/approve` and `/reject` commands to onbod.

## Outbound replies

To send a reply to a user, onbod POSTs to the channel adapter's
`/send` endpoint (found in the channels table for the JID prefix):

```
POST <adapter_url>/send
Content-Type: application/json
Authorization: Bearer <channel_token>

{"jid": "<chat_jid>", "text": "<reply>"}
```

The adapter URL and token are looked up from the channels table
by matching `jid` prefix to `jid_prefixes` column.

## Notifications and replies

onbod uses the `notify/` library (`notify.Send(jids, text, sendFn)`)
for operator notifications. `sendFn` wraps the outbound POST above.
Replies and notifications stored via `store.StoreOutbound`:

`store.StoreOutbound` and `store.OutboundEntry` already exist
(shipped in Round 1, `store/outbound.go`):

```go
type OutboundEntry struct {
    ChatJID     string
    Content     string
    Source      string
    GroupFolder string
    Timestamp   time.Time
}
func (s *Store) StoreOutbound(e OutboundEntry) error
```

onbod uses `Source: "onboarding"`, `GroupFolder: ""`.

## Target routing (onbod as a channel service)

When gated's route table returns `target = "onbod"` for an
incoming message, gated looks up `"onbod"` in the channels
table (not the groups table) and HTTP-POSTs to the channel's
`/send` URL. This is the same path as routing to any channel
service. See `specs/7/1-channel-protocol.md` §service-routes
for the full lookup mechanism.

## Service contract

onbod registers in the channels table on startup (same as teled/discd):

```
name:         "onbod"
url:          "http://onbod:8091"
capabilities: {receive_only: true}
```

`receive_only` — gated routes messages to onbod but onbod does not
deliver inbound messages from a user platform.

- **Input**: shared SQLite DB (onboarding table, messages table, channels table, groups table, routes table); HTTP POST to `/send`
- **Output**: HTTP POST to channel adapter `/send` endpoint, writes to SQLite tables
- **No imports** from gateway, core, or any arizuko Go package
- **Dependencies**: `database/sql`, `modernc.org/sqlite`

## Notifications

onbod determines root JIDs by querying:
`SELECT jid FROM routes WHERE target = (SELECT folder FROM registered_groups WHERE parent IS NULL LIMIT 1) AND seq = 0`
then calls `notify.Send(jids, text, sendFn)` from the `notify/` library.

## Config

```
DATABASE=                         # required (or DATA_DIR)
DATA_DIR=                         # used to derive DATABASE if not set
ONBOARDING_ENABLED=0              # off by default; onbod exits if 0
ONBOARDING_PROTOTYPE=             # path to prototype dir to clone; empty = no cloning
ONBOARD_POLL_INTERVAL=10s         # poll interval for onboarding table
```

## Layout

```
onbod/
  main.go
```

## Not in scope

- Auto-approve (allowlist, rate limit)
- Multi-step onboarding (language, purpose, etc.)
- Onboarding for adding JIDs to existing worlds
