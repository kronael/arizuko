<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Prompt Format

## ContainerInput (stdin JSON)

| Field             | Type     | Notes                          |
| ----------------- | -------- | ------------------------------ |
| `prompt`          | string   | XML `<messages>` block         |
| `sessionId`       | string?  | Resume; omit for new           |
| `groupFolder`     | string   | Filesystem-safe folder name    |
| `chatJid`         | string   | Channel JID                    |
| `messageCount`    | number?  | Messages in this batch         |
| `delegateDepth`   | number?  | Delegation nesting depth       |
| `isScheduledTask` | boolean? | Scheduled task header if true  |
| `assistantName`   | string?  | `NANOCLAW_ASSISTANT_NAME` env  |
| `secrets`         | object?  | API keys; stripped, not logged |

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

## IPC close sentinel

`_close` sentinel file (empty, no ext) in `/workspace/ipc/input/`
ends agent loop.
