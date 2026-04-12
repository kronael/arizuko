---
name: recall-messages
description: >
  Search past chat messages for what someone said, past
  decisions, or conversation history. Not for stored knowledge.
user_invocable: true
arg: <question>
---

# Recall Messages

Search chat message history for past conversations. Use for "what did X say",
"did we discuss Y", decisions made in conversation.
NEVER use for stored knowledge (facts/diary/episodes) — use `/recall-memories` for those.

## Protocol

### 1. Fetch message history

```
get_history({ chat_jid: "<jid from message context>", limit: 200 })
```

Optional `before` param (ISO 8601 timestamp) for pagination.
Write the result to `~/tmp/messages.json` for the Explore subagent.

### 2. Spawn Explore subagent

> Search `~/tmp/messages.json` for messages related to: `<question>`.
> Return matching messages with sender, timestamp, and content.
> Summarize what you found and how it relates to the question.

### 3. Report

Summarize the findings. Do NOT fabricate matches or infer from partial text.

## Pagination

If the first 200 messages don't contain a match, ask the user whether
to go further back. Each subsequent call uses `before: <oldest timestamp seen>`.

## When to use

- "what did X say about Y last week?"
- "did we discuss Z before?"
- "what was the decision on X?"
- Anything referencing past conversation content (not stored knowledge)

For stored knowledge (facts, past research, diary): use `/recall-memories`.
