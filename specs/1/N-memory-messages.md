---
status: shipped
---

# Memory: Messages

Recent message history piped to agent on each invocation.
Assembled by `router.FormatMessages`.

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
  <message id="m100" sender="Alice" sender_id="telegram:user/111218"
           chat_id="telegram:group/100123"
           platform="telegram" time="..." ago="3h">hey</message>
</messages>
```

If the user is replying to a specific message, a `<reply-to>` block
sits as a sibling header **immediately above** the `<message>` it
points to:

```xml
<messages>
  <reply-to id="m99" sender="bot"/>
  <message id="m100" sender="Alice" ...>do you see what I'm replying to?</message>
</messages>
```

Self-closing when the parent is in agent session (no body needed —
no duplication). Body-bearing when the parent is out of session
window:

```xml
<reply-to id="m42" sender="bot">how are you?</reply-to>
<message id="m100" ...>fine.</message>
```

Assembly order: clock > system messages > pendingArgs > messages.

## Rules

- **100 message limit**, most recent, ordered by time, no time window
- **Bot messages filtered** (`is_bot_message = 0`)
- **Injection on new session only** (not resume -- SDK transcript
  has context, injection would duplicate)
- System messages always flushed regardless of new/resume

Attributes on `<message>`: `id`, `sender`, `sender_id` (JID),
`chat_id`, `platform`, `time` (ISO 8601), `ago` (relative). When
the message is a reply, the preceding `<reply-to>` carries `id`
(parent's id) and `sender`, with optional excerpt body.
