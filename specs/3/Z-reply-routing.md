---
status: shipped
---

## status: shipped (partial)

# Per-sender Reply Routing

Reply threading for multi-sender chats and delegation paths.
Ported from kanipi design (specs/3/R-reply-routing, 3/Q-auto-threading).

## Problem

Two issues break reply threading:

1. **Delegation drops replyTo** — direct path passes replyTo,
   delegation path does not. Response appears as standalone.

2. **Batch conflates senders** — messages grouped by chatJid,
   not sender. lastMsg.id points to wrong person.

## Design

### A. Send returns message ID

`Channel.Send` returns the platform-native sent message ID.
All platform APIs provide this — currently discarded.

| Channel  | ID source              | Field           |
| -------- | ---------------------- | --------------- |
| Telegram | `sendMessage` response | `message_id`    |
| Discord  | `ChannelMessageSend`   | `.ID`           |
| WhatsApp | `SendMessage` response | `.ID`           |
| Local    | self-generated UUID    | return directly |

### B. Chunk chaining

Multi-message responses chain: each reply threads to the
**previous sent message**, not all to the original.

```
Alice: "help"              <- original
  Bot: "Sure..."           <- replyTo: Alice's msg (id=801)
    Bot: "Also..."         <- replyTo: 801 (id=802)
      Bot: "Done."         <- replyTo: 802
```

Gateway streaming callback and MCP send path both maintain
`lastSentId`. First message uses triggering messageId,
each subsequent uses the returned ID from previous send.

### C. MessageId through delegation

`delegate_group` and `escalate_group` MCP tools carry
`messageId` via `forwarded_from` and `reply_to_id` on the
delegated message row. Streaming callback uses it as
initial `replyTo`. Flows into `ContainerInput` for agent
visibility.

### D. MCP send_reply auto-injects replyTo

When agent uses `send_reply` or `send_message` targeting
bound chatJid, gateway auto-injects `replyTo` from current
chain position if agent does not provide one explicitly.
`lastSentId` tracked per chatJid, shared with streaming.

### E. Per-sender batching

Messages from same chatJid split by sender before dispatch.
Each sender's batch gets its own messageId for threading.

```
poll: [Alice:"help", Bob:"hi", Alice:"thanks"]
split:
  Alice: ["help","thanks"] -> replyTo = Alice's last id
  Bob:   ["hi"]            -> replyTo = Bob's id
```

Applies to ALL routes:

- **Static routes**: each sender batch dispatched separately.
  Group sees one sender at a time, replies thread correctly.
- **Template routes** (`{sender}`): each sender routes to own
  group. Per-sender batching ensures correct messageId.

Cursor tracking stays per-chatJid. Per-sender split happens
at dispatch time, not cursor time.

### F. Escalation reply threading

Escalation is LLM-to-LLM: parent responds to `local:worker`,
worker forwards to user. Threading matters on the final hop.

`escalate_group` embeds `reply_to` and `reply_id` in the
`<escalation>` XML sent to parent. When parent responds,
gateway wraps the LocalChannel message with origin metadata:

```xml
<escalation_response origin_jid="telegram:12345"
                     origin_msg_id="567">
  Parent's response text
</escalation_response>
```

Worker sees origin, uses `send_message` with correct JID
and replyTo. Chunk chaining handles subsequent threading.

Escalation metadata tracked on the queue entry (label =
`escalate`). Gateway wraps response before storing via
LocalChannel.

## Implementation Status

| Section                                | Status            | Notes                                                                     |
| -------------------------------------- | ----------------- | ------------------------------------------------------------------------- |
| A. Send returns message ID             | shipped           | `Channel.Send(jid, text, replyTo string) (string, error)`                 |
| B. Chunk chaining                      | shipped           | gateway `lastSentID` updated per send in `processGroupMessages`           |
| C. MessageId through delegation        | shipped           | `msgID` param in `runAgentWithOpts`, passed to `ContainerInput.MessageID` |
| D. MCP send_reply auto-injects replyTo | shipped (cc50def) | auto-injects replyTo from last MCP-sent ID when not provided explicitly   |
| E. Per-sender batching                 | shipped (1fb565a) | `groupBySender` splits messages, each batch processes independently       |
| F. Escalation reply threading          | shipped (29f1982) | `parseEscalationOrigin`, wrapped responses, LocalChannel for local: JIDs  |
