---
status: shipped
---

# JID Format Normalization — spec (shipped)

Add platform prefix to senders, enrich message XML attributes,
add clock header. Keep WhatsApp suffixes (no collisions).

## Format

All JIDs use `scheme:id`. Same format for chats and senders.

**Chat JIDs** (routing targets):

```
telegram:-1001234567890                    telegram group
telegram:REDACTED                        telegram DM
whatsapp:REDACTED@g.us           whatsapp group
whatsapp:972501234567@s.whatsapp.net       whatsapp DM
discord:1234567890                         discord channel
email:a1b2c3d4e5f6                         email thread
web:main                                   web channel
```

**Sender JIDs** (message attribution):

```
telegram:REDACTED                        telegram user
whatsapp:972501234567@s.whatsapp.net       whatsapp user
discord:9876543210                         discord user
email:user@example.com                     email sender
```

Display names are NOT in the JID. They stay in
`messages.sender_name` (existing column).

## Changes from v1

### WhatsApp suffixes

Keep suffixes as-is (`@g.us`, `@s.whatsapp.net`). Stripping could cause
collisions between group IDs and user IDs. The suffixes are ugly but safe.

### Sender platform prefix

All senders get `scheme:` prefix. Before: bare `REDACTED`.
After: `telegram:REDACTED`. Stored in `messages.sender`.

### Clock header

Injected once per agent invocation, before all messages:

```xml
<clock time="2026-03-11T17:23:00.000Z" tz="Europe/Prague" />
```

| Attribute | Source             | Present |
| --------- | ------------------ | ------- |
| `time`    | `new Date()` (UTC) | always  |
| `tz`      | `TIMEZONE` env var | always  |

Injection point: `index.ts` prompt assembly, prepended before
system messages and `<messages>`. Not injected on piped
messages (only the initial prompt gets the clock).

### Message XML attributes

Full metadata on each `<message>` tag:

```xml
<message sender="Alice" sender_id="telegram:REDACTED"
         chat_id="telegram:-1001234567890" chat="Support"
         platform="telegram" time="2026-03-11T14:00:00Z" ago="3h">
  Hello
</message>
```

| Attribute   | Source             | Present                          |
| ----------- | ------------------ | -------------------------------- |
| `sender`    | sender_name col    | always (falls back to sender ID) |
| `sender_id` | messages.sender    | always                           |
| `chat_id`   | messages.chat_jid  | always                           |
| `chat`      | chats.name         | when is_group                    |
| `platform`  | platform           | always                           |
| `time`      | timestamp          | always                           |
| `ago`       | computed at format | always                           |

`sender` = display name, `sender_id` = JID. `chat` = chat
group name (message origin, not agent run group), `chat_id` = JID.
`ago` = human-readable relative time (e.g. `3h`, `2d`, `1w`).

## Session context injection

**Status: phase 3 — not yet implemented**

When the gateway builds the prompt for an agent invocation, prepend
a `<context>` block before `<messages>`. This replaces the agent's
need to read env vars and settings.json for identity/location info.

```xml
<context>
  <agent group="atlas/support" name="Atlas Support" tier="2" world="atlas"/>
  <chat jid="telegram:-1001234567890" name="Support" platform="telegram" is_group="true"/>
</context>
<messages>
  ...
</messages>
```

### `<agent>` attributes

| Attribute | Source                | Present |
| --------- | --------------------- | ------- |
| `group`   | NANOCLAW_GROUP_FOLDER | always  |
| `name`    | NANOCLAW_GROUP_NAME   | always  |
| `tier`    | NANOCLAW_TIER         | always  |
| `world`   | folder.split('/')[0]  | always  |

### `<chat>` attributes

| Attribute  | Source               | Present        |
| ---------- | -------------------- | -------------- |
| `jid`      | NANOCLAW_CHAT_JID    | always         |
| `name`     | chats.name           | when available |
| `platform` | platformFromJid(jid) | always         |
| `is_group` | chats.is_group       | always         |

### Injection point

`index.ts` builds the prompt string passed to `container-runner`.
The `<context>` block is prepended before the `formatMessages()`
output. `formatMessages()` in `router.ts` is unchanged — it still
returns `<messages>...</messages>`. The gateway concatenates:

```
<context>...</context>\n<messages>...</messages>
```

The agent sees a self-contained prompt with identity, location,
and conversation in one XML document.

## Migration

SQL migration adds platform prefix to existing sender values in the
messages table, matching by `chat_jid` prefix. WhatsApp suffixes kept as-is.
