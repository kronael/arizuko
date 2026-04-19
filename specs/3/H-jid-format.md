---
status: shipped
---

# JID Format

All JIDs use `platform:account/id`. `local:folder` has no account segment.

## Clock header

Injected once per agent invocation, before messages:

```xml
<clock time="2026-03-11T17:23:00.000Z" tz="Europe/Prague" />
```

Initial prompt only, not piped messages.

## Message XML

```xml
<message sender="Alice" sender_id="telegram:main/..."
         chat_id="telegram:main/-100..."
         platform="telegram" time="..." ago="3h">
  Hello
</message>
```

| Attribute   | Source            | Present                          |
| ----------- | ----------------- | -------------------------------- |
| `sender`    | sender_name col   | always (falls back to sender ID) |
| `sender_id` | messages.sender   | always                           |
| `chat_id`   | messages.chat_jid | always                           |
| `platform`  | platform          | always                           |
| `time`      | timestamp         | always                           |
| `ago`       | computed          | always                           |

## Session context injection (not yet implemented)

```xml
<context>
  <agent group="atlas/support" name="Atlas Support" tier="2" world="atlas"/>
  <chat jid="telegram:main/-100..." platform="telegram"/>
</context>
```

`<agent>`: `group`=`ARIZUKO_GROUP_FOLDER`, `name`=`ARIZUKO_GROUP_NAME`,
`tier`=`ARIZUKO_TIER`, `world`=folder first segment.

`<chat>`: `jid`=`ARIZUKO_CHAT_JID`, `platform`=`platformFromJid(jid)`.
