---
status: shipped
---

# Per-sender Reply Routing

Reply threading for multi-sender chats and delegation paths.

## Problems addressed

1. Delegation dropped `replyTo` — direct path passed it, delegation did
   not. Response appeared standalone.
2. Batch conflated senders — messages grouped by `chatJid` not sender;
   `lastMsg.id` pointed to wrong person.

## Design

| Section                                | Status | Notes                                                                      |
| -------------------------------------- | ------ | -------------------------------------------------------------------------- |
| A. Send returns message ID             | done   | `Channel.Send(jid, text, replyTo) (string, error)`                         |
| B. Chunk chaining                      | done   | gateway `lastSentID` updated per send in `processGroupMessages`            |
| C. MessageId through delegation        | done   | `msgID` in `runAgentWithOpts`, passed to `ContainerInput.MessageID`        |
| D. MCP send_reply auto-injects replyTo | done   | auto from last MCP-sent ID when not provided                               |
| E. Per-sender batching                 | done   | `groupBySender` splits batches, each processed independently               |
| F. Escalation reply threading          | done   | `parseEscalationOrigin`, wrapped responses, LocalChannel for `local:` JIDs |
