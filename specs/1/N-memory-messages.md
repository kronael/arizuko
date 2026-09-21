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
- **Bot messages filtered from the injected history** (`is_bot_message`
  absent on the folder-feed and activity reads), and **marked wherever they
  are deliberately returned**: `inspect_messages`, `get_thread` and
  `fetch_history`'s cache fallback read the chat partition unfiltered and
  render the agent's own rows as `bot="true"`. The filter and the marker are
  the same answer for two audiences — a feed the agent must not re-answer,
  and an audit it asked for.
- **Injection on new session only** (not resume -- SDK transcript
  has context, injection would duplicate)
- System messages always flushed regardless of new/resume

Attributes on `<message>`: `id`, `sender`, `sender_id` (JID),
`chat_id`, `chat_name`, `platform`, `platform_id`, `time` (ISO 8601),
`ago` (relative), `verb`, `thread`, and the two flags emitted only when
true — `bot` (the agent wrote this row) and `errored`. When
the message is a reply, the preceding `<reply-to>` carries `id`
(parent's id) and `sender`, with optional excerpt body.

`bot` reads `is_bot_message`, never `is_from_me`: an imported archive sets
`is_from_me` for the linked ACCOUNT's own messages, which are the operator's.
Own-authorship is READ off the row, never inferred by comparing `sender`
against the folder name — that inference holds only for rows this instance
wrote and is silently wrong for every platform-fetched one.
