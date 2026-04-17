---
status: shipped
---

# Per-sender Reply Routing

Reply threading for multi-sender chats and delegation paths.

## Problem

1. **Delegation drops replyTo** — direct path passes replyTo, delegation
   path does not. Response appears as standalone.
2. **Batch conflates senders** — messages grouped by chatJid, not sender.
   lastMsg.id points to wrong person.

## Design Summary

**A. Send returns message ID** — `Channel.Send` returns platform-native
sent message ID (previously discarded).

**B. Chunk chaining** — multi-message responses chain to previous sent
message, not all to the original.

**C. MessageId through delegation** — `delegate_group` and
`escalate_group` carry `messageId` via `forwarded_from` and
`reply_to_id` on the delegated message row.

**D. MCP send_reply auto-injects replyTo** — when agent uses
`send_reply`/`send_message` targeting bound chatJid, gateway auto-injects
`replyTo` from current chain position if not provided explicitly.

**E. Per-sender batching** — messages from same chatJid split by sender
before dispatch. Each sender's batch gets its own messageId for threading.
Applies to static routes and template routes (`{sender}`).

**F. Escalation reply threading** — `escalate_group` embeds `reply_to`
and `reply_id` in `<escalation>` XML. Worker sees origin metadata, uses
`send_message` with correct JID and replyTo. Escalation metadata tracked
on queue entry (label = `escalate`).

## Implementation Status

| Section                                | Status            | Notes                                                                     |
| -------------------------------------- | ----------------- | ------------------------------------------------------------------------- |
| A. Send returns message ID             | shipped           | `Channel.Send(jid, text, replyTo string) (string, error)`                 |
| B. Chunk chaining                      | shipped           | gateway `lastSentID` updated per send in `processGroupMessages`           |
| C. MessageId through delegation        | shipped           | `msgID` param in `runAgentWithOpts`, passed to `ContainerInput.MessageID` |
| D. MCP send_reply auto-injects replyTo | shipped (cc50def) | auto-injects replyTo from last MCP-sent ID when not provided explicitly   |
| E. Per-sender batching                 | shipped (1fb565a) | `groupBySender` splits messages, each batch processes independently       |
| F. Escalation reply threading          | shipped (29f1982) | `parseEscalationOrigin`, wrapped responses, LocalChannel for local: JIDs  |
