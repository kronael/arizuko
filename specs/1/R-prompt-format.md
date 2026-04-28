---
status: shipped
---

# Prompt Format

Stdin/stdout contract between `gated` and the in-container agent
(`ant/`). Assembled by `router/`, consumed by `ant/src/runner.ts`.

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

## Per-turn output (`submit_turn` JSON-RPC)

Per-turn results return over the gated MCP unix socket via the
`submit_turn` method (hidden from `tools/list`):

```jsonc
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "submit_turn",
  "params": {
    "turn_id": "<originating message id>",
    "session_id": "abc123",
    "status": "success",
    "result": "...",
  },
}
```

| Field        | Type             | Notes                       |
| ------------ | ---------------- | --------------------------- |
| `turn_id`    | string           | Idempotency key with folder |
| `status`     | `success\|error` |                             |
| `result`     | string?          | Empty/missing = silent      |
| `session_id` | string?          | Persisted by host           |
| `error`      | string?          | When `status === 'error'`   |

One `submit_turn` per turn. `<internal>` tags in `result` stripped
before sending.

## SOUL.md and SYSTEM.md Injection

If `SOUL.md` exists in the group folder, its content is passed as the
`soul` field. Agents should read and embody this persona.

If `SYSTEM.md` exists in the group folder, its content replaces the
default system prompt via `systemMd`.

## IPC close sentinel

`_close` sentinel file (empty, no ext) in `/workspace/ipc/input/`
ends agent loop.
