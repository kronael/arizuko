<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Prompt Format

## ContainerInput (stdin JSON)

| Field           | Type     | Notes                                 |
| --------------- | -------- | ------------------------------------- |
| `prompt`        | string   | XML `<messages>` block                |
| `sessionId`     | string?  | Resume; omit for new                  |
| `groupFolder`   | string   | Filesystem-safe folder name           |
| `chatJid`       | string   | Channel JID                           |
| `topic`         | string?  | Topic session name                    |
| `messageCount`  | number?  | Messages in this batch                |
| `delegateDepth` | number?  | Delegation nesting depth              |
| `assistantName` | string?  | `ASSISTANT_NAME` env                  |
| `secrets`       | object?  | API keys; stripped, not logged        |
| `channelName`   | string?  | Channel adapter name                  |
| `messageId`     | string?  | Triggering message ID                 |
| `grants`        | string[] | Authorization rules                   |
| `sender`        | string?  | Message sender                        |
| `soul`          | string?  | SOUL.md content for persona           |
| `systemMd`      | string?  | SYSTEM.md full system prompt override |

## Prompt assembly order

```
clock (clockXml) -> system messages (flushSystemMessages)
  -> pendingArgs (command context) -> message history (formatMessages)
```

`pendingArgs`: raw text following a command trigger (e.g.,
`/ask what is X` -> `"what is X"`). Consumed once, deleted after read.

## ContainerOutput (stdout JSON)

Wrapped in sentinel markers:

```
---NANOCLAW_OUTPUT_START---
{"status":"success","result":"...","newSessionId":"abc123"}
---NANOCLAW_OUTPUT_END---
```

| Field          | Type             | Notes                     |
| -------------- | ---------------- | ------------------------- |
| `status`       | `success\|error` |                           |
| `result`       | `string\|null`   | null = silent             |
| `newSessionId` | string?          | Persisted by host         |
| `error`        | string?          | When `status === 'error'` |

Multiple marker pairs per run (streaming). `<internal>` tags
in `result` stripped before sending.

## SOUL.md and SYSTEM.md Injection

If `SOUL.md` exists in the group folder, its content is passed as the
`soul` field. Agents should read and embody this persona.

If `SYSTEM.md` exists in the group folder, its content replaces the
default system prompt via `systemMd`.

## IPC close sentinel

`_close` sentinel file (empty, no ext) in `/workspace/ipc/input/`
ends agent loop.
