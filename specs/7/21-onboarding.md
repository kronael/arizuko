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
are written to the messages table with a marker (e.g.
`origin: "onboarding"`). onbod reads those to drive the
state machine.

## State machine

```
onbod polls onboarding table; receives commands via HTTP POST /send

  awaiting_name (new entry, no user response yet):
    -> wait for message from this jid
    -> validate input (a-z0-9-, not taken, not reserved)
    -> invalid: send "Try again — lowercase letters, numbers, hyphens only"
    -> valid: set status=pending, store world_name
    -> notify() root
    -> send "Got it! Waiting for approval."

  pending:
    -> send "Still waiting for approval."

  rejected:
    -> silence
```

## State table

```sql
CREATE TABLE onboarding (
  jid        TEXT PRIMARY KEY,
  status     TEXT NOT NULL,  -- awaiting_name | pending | approved | rejected
  sender     TEXT,
  channel    TEXT,
  world_name TEXT,
  created    TEXT NOT NULL
);
```

## Commands

onbod handles `/approve` and `/reject`. gated routes these via
the channels table — onbod receives them as HTTP POST to its
`/send` endpoint. Routing table entries:

```
match=/approve  →  onbod
match=/reject   →  onbod
```

### /approve <jid>

- Root-only (tier 0)
- Reads `world_name` from onboarding table
- Creates world folder: `groups/<world_name>/`
- Copies prototype if `ONBOARDING_PROTOTYPE` set
- Inserts group in DB (tier 1)
- Adds default route
- Enqueues welcome system message
- Sets onboarding status to `approved`
- notify(): "Approved: alice -> alice-studio/"

### /reject <jid>

- Root-only
- Sets status to `rejected`
- notify(): "Rejected: <jid>"

## Notifications and replies

onbod imports `notify/` to send operator notifications
(same shared library as gated). Replies to onboarding users
sent via channel adapter HTTP API. All outbound messages
stored via `store.StoreOutbound` with `source: "onboarding"`.

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

## Config

```
ONBOARDING_ENABLED=0              # off by default
ONBOARDING_PROTOTYPE=             # optional: clone from prototype/
```

## Layout

```
services/onbod/
  main.go
```

## Not in scope

- Auto-approve (allowlist, rate limit)
- Multi-step onboarding (language, purpose, etc.)
- Onboarding for adding JIDs to existing worlds
