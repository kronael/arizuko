---
status: shipped
---

# System Messages

Gateway-generated annotations alongside user messages into agent
stdin. Never sent to channel, never trigger agent alone — piggyback
on next real user message. Table: `system_messages`. Code: `store/`.

`episode`, `fact`, `identity` origins reserved for v2.

## XML schema

```xml
<system origin="<subsystem>" [event="<event>"] [attrs]>
  [child elements or body text]
</system>
```

- `origin` -- subsystem (gateway, command, diary, etc.)
- `event` -- optional event within subsystem
- body -- free-form text or typed child elements

## Origin table

| Origin     | Event          | Producer         | When                     |
| ---------- | -------------- | ---------------- | ------------------------ |
| `gateway`  | `new-session`  | message loop     | Each new spawn           |
| `gateway`  | `new-day`      | message loop     | First msg of new day     |
| `command`  | `new`/`<name>` | command handlers | Command sets context     |
| `diary`    | --             | diary layer      | Session start            |
| `episode`  | --             | episode (v2)     | Periodic summary         |
| `fact`     | --             | facts (v2)       | Proactive fact retrieval |
| `identity` | --             | identity (v2)    | Active identity context  |

## `<previous_session>` attributes

`id` (UUID), `started` (ISO8601), `ended` (ISO8601),
`msgs` (int), `result` (ok|error|unknown), `error` (text).

## Flush semantics

Per-group queue stored in DB. On flush: SELECT, serialize as XML,
prepend to stdin, DELETE (same transaction). Empty queue = no overhead.
