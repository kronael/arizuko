# Onboarding

**Status**: design

Hardcoded gateway flow for unrouted JIDs. No LLM, no container,
no group until approval. State machine in `gateway/onboarding.go`,
state in `onboarding` table.

## User journey

1. User finds the bot, sends "hello"
2. No route exists — gateway runs onboarding flow
3. Bot replies: "Pick a name for your workspace:"
4. User types: "alice-studio"
5. Bot validates, stores, notifies root
6. Bot replies: "Got it! Waiting for approval."
7. Operator approves — world created, route added
8. User sends next message — routes to their world
9. Agent wakes with welcome system message

## Admin journey

1. Enable: `ONBOARDING_ENABLED=1` in `.env`
2. Root gets notification: "alice wants 'alice-studio'
   — `/approve telegram:-12345`"
3. `/approve telegram:-12345` — creates world, routes JID
4. `/reject telegram:-12345` — suppresses forever

## How it works

Gateway's existing "no group" branch is the only hook:

```go
if group == nil {
    if cfg.OnboardingEnabled {
        handleOnboarding(chatJid, messages, channel)
        return
    }
    return
}
```

`handleOnboarding` is a state machine driven by the onboarding
table. Pure gateway code — `channel.Send()` responses, no LLM.

## State machine

```
message arrives, no route exists
  -> look up jid in onboarding table

  no entry:
    -> insert (status: awaiting_name)
    -> send "Pick a name for your workspace (lowercase, no spaces):"

  awaiting_name:
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

### /approve <jid>

- Root-only (tier 0)
- Reads `world_name` from onboarding table
- Creates world folder: `groups/<world_name>/`
- Copies prototype if `ONBOARDING_PROTOTYPE` set
- Inserts group in DB (tier 1)
- Adds default route
- Enqueues welcome system message
- Sets onboarding status to `approved`
- `notify()`: "Approved: alice -> alice-studio/"

### /reject <jid>

- Root-only
- Sets status to `rejected`
- `notify()`: "Rejected: <jid>"

## Config

```
ONBOARDING_ENABLED=0              # off by default
ONBOARDING_PROTOTYPE=             # optional: clone from prototype/
```

## Not in scope

- Auto-approve (allowlist, rate limit)
- Multi-step onboarding (language, purpose, etc.)
- Onboarding for adding JIDs to existing worlds
