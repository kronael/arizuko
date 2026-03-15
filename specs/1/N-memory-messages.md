<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Memory: Messages

Recent message history piped to agent on each invocation.

## Stdin XML envelope structure

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
</messages>
```

Assembly order: clock > system messages > pendingArgs > messages.

## Rules

- **100 message limit**, most recent, ordered by time, no time window
- **Bot messages filtered** (`is_bot_message = 0`)
- **Injection on new session only** (not resume -- SDK transcript
  has context, injection would duplicate)
- System messages always flushed regardless of new/resume

Attributes: `sender`, `sender_id` (JID), `chat_id`, `chat`
(group name), `platform`, `time` (ISO 8601), `ago` (relative).
