---
status: shipped
---

# Memory: Messages

Recent message history piped to agent on each invocation.

See `specs/1/K-knowledge-system.md` for memory layer overview.

## Stdin envelope

Agent receives a single `prompt` string — clock header,
system messages, then message history:

```xml
<clock time="2026-03-11T17:23:00.000Z" tz="Europe/Prague" />
<system origin="gateway" event="new-session">
  <previous_session id="9123f10a" started="..."
    ended="..." msgs="42" result="ok"/>
</system>
<system origin="diary" date="2026-03-04">
  deployed REDACTED, auth flow open
</system>
<messages>
  <message sender="Alice" sender_id="telegram:111218"
           chat_id="telegram:-100123" chat="Support"
           platform="telegram" time="..." ago="3h">hey</message>
  <message sender="Bob" sender_id="telegram:222333"
           chat_id="telegram:-100123" chat="Support"
           platform="telegram" time="..." ago="1m">sure</message>
</messages>
```

1. **Clock** -- `<clock>` with UTC time and timezone (initial prompt only)
2. **System messages** -- zero or more `<system>` blocks
   (see `system-messages.md`)
3. **Message history** -- `<messages>` block, always present

## Message history -- shipped

`formatMessages()` in `src/router.ts`. Gateway stores every
inbound message in `messages` table. On each invocation:
**last 100 messages** (most recent, ordered by time). No time
window filter — all 100 come from the `messages` table
regardless of age.

```xml
<messages>
  <!-- 47 older messages not shown -->
  <message sender="Alice" time="...">hey</message>
  ...
</messages>
```

Attributes: `sender` (display name), `sender_id` (JID),
`chat_id` (chat JID), `chat` (group name, when is_group),
`platform`, `time` (ISO 8601), `ago` (relative time).
Content XML-escaped. See `specs/3/H-jid-format.md` for
full attribute table.
Bot messages filtered (`is_bot_message = 0`).

## Session interaction

Two history sources: gateway pipe (this spec) and SDK
session (`.jl` transcript replay).

**Rule**: `<messages>` injected on **new session only**.
On resume, SDK transcript has context — injection would
duplicate. System messages always flushed regardless.

Gateway determines new vs resume by stored `session_id`.

## Open

- `reply_to` not emitted (see `channels.md`)
- No compaction on `messages` table
- `get_history` IPC for on-demand lookups
