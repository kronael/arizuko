# System Messages

Gateway-generated annotations riding alongside user messages
into agent stdin. Never sent to channel, never trigger agent
alone — piggyback on next real user message.

## Envelope

```xml
<system origin="gateway" event="new-session">
  <previous_session id="9123f10a" started="..."
    ended="..." msgs="42" result="ok"/>
</system>
<system origin="command" event="new">
  user invoked /new
</system>
<system origin="diary" date="2026-03-04">
  deployed REDACTED
</system>
hey what's up
```

## Schema

```xml
<system origin="<subsystem>" [event="<event>"] [attrs]>
  [child elements or body text]
</system>
```

- `origin` -- subsystem (gateway, command, diary, etc.)
- `event` -- optional event within subsystem
- body -- free-form text or typed child elements

## Queue

Per-group, stored in DB.

```ts
interface SystemMessage {
  origin: string;
  event?: string;
  attrs?: Record<string, string>;
  body: string;
}
```

**Enqueue**: `enqueueSystemMessage(groupId, msg)`.
**Flush**: in `processGroupMessages`, before prompt:
SELECT, serialise as XML, prepend to stdin, DELETE
(same transaction). Empty queue = no overhead.

## Origins

| Origin     | Event          | Producer         | When                     |
| ---------- | -------------- | ---------------- | ------------------------ |
| `gateway`  | `new-session`  | message loop     | Each new spawn           |
| `gateway`  | `new-day`      | message loop     | First msg of new day     |
| `command`  | `new`/`<name>` | command handlers | Command sets context     |
| `diary`    | --             | diary layer      | Session start            |
| `episode`  | --             | episode (v2)     | Periodic summary         |
| `fact`     | --             | facts (v2)       | Proactive fact retrieval |
| `identity` | --             | identity (v2)    | Active identity context  |

### `<previous_session>` attributes

`id` (UUID), `started` (ISO8601), `ended` (ISO8601),
`msgs` (int), `result` (ok|error|unknown), `error` (text).

## Sessions table

```sql
CREATE TABLE sessions (
  session_id    TEXT PRIMARY KEY,
  group_id      TEXT NOT NULL,
  started_at    TEXT NOT NULL,
  ended_at      TEXT,
  message_count INTEGER,
  result        TEXT,
  error         TEXT
);
```

New-session injection: last 2 sessions by `started_at`.

Code uses two tables: `sessions` (current session per group,
keyed by `group_folder`) and `session_history` (historical
records with `session_id`, `group_id`, `started_at`,
`ended_at`, `message_count`, `result`, `error`).

## new-day trigger

Compare `new Date().toDateString()` against stored date.
If different: `<system origin="gateway" event="new-day">`.

## Persistence

```sql
CREATE TABLE system_messages (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id TEXT NOT NULL,
  origin   TEXT NOT NULL,
  event    TEXT,
  attrs    TEXT,
  body     TEXT NOT NULL,
  ts       TEXT NOT NULL
);
```

## Agent awareness

Documented in `container/skills/self/SKILL.md`. Agent
should know system messages exist, what each origin means,
that they come from gateway not user, and never quote
them back to user verbatim.

## Non-producers

`/ping`, `/chatid` — reply only, no agent involvement.
Channel join/leave — gateway-internal.

## Open

- `enqueueSystemMessage` / `flushSystemMessages` in db.ts
- `sessions` table write/update in container-runner.ts
- `new-session` injection: build `<previous_session>` block
- Agent self-persona: add system messages to SKILL.md
