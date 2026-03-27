---
status: draft
---

## <!-- trimmed 2026-03-15: migration notes removed, rich facts only -->

## status: superseded by specs/8/5-jid-format.md

# JID Format Normalization

All JIDs use `platform:account/id`. See specs/8/5-jid-format.md for full spec.
`local:folder` has no account segment.

~~Old format was `scheme:id` — no longer used.~~

## Clock Header

Injected once per agent invocation, before messages:

```xml
<clock time="2026-03-11T17:23:00.000Z" tz="Europe/Prague" />
```

Only on initial prompt, not piped messages.

## Message XML Attributes

```xml
<message sender="Alice" sender_id="telegram:main/REDACTED"
         chat_id="telegram:main/-1001234567890" chat="Support"
         platform="telegram" time="2026-03-11T14:00:00Z" ago="3h">
  Hello
</message>
```

| Attribute   | Source            | Present                          |
| ----------- | ----------------- | -------------------------------- |
| `sender`    | sender_name col   | always (falls back to sender ID) |
| `sender_id` | messages.sender   | always                           |
| `chat_id`   | messages.chat_jid | always                           |
| `chat`      | chats.name        | when is_group                    |
| `platform`  | platform          | always                           |
| `time`      | timestamp         | always                           |
| `ago`       | computed          | always                           |

## Session Context Injection (not yet implemented)

Prepend `<context>` block before `<messages>`:

```xml
<context>
  <agent group="atlas/support" name="Atlas Support" tier="2" world="atlas"/>
  <chat jid="telegram:main/-1001234567890" name="Support" platform="telegram" is_group="true"/>
</context>
```

### `<agent>` attributes

| Attribute | Source               |
| --------- | -------------------- |
| `group`   | ARIZUKO_GROUP_FOLDER |
| `name`    | ARIZUKO_GROUP_NAME   |
| `tier`    | ARIZUKO_TIER         |
| `world`   | folder.split('/')[0] |

### `<chat>` attributes

| Attribute  | Source               |
| ---------- | -------------------- |
| `jid`      | ARIZUKO_CHAT_JID     |
| `name`     | chats.name           |
| `platform` | platformFromJid(jid) |
| `is_group` | chats.is_group       |
