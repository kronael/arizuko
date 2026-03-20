---
name: recall-messages
description: Search older chat messages for relevant information.
user_invocable: true
arg: <question>
---

# Recall Messages

Search older chat messages for information relevant to a question.
Use this when looking for something a user said, a decision made in
conversation, or context from past exchanges — not for knowledge stored
in facts/, diary/, or episodes/ (use `/recall-memories` for those).

## v1 Protocol (simplistic)

v1 uses an Explore subagent to grep through message history files.
No vector search, no FTS — just structured text scanning.

### Step 1 — Fetch message history

Request older messages via the `get_history` IPC action:

```json
{ "type": "get_history", "before": "<ISO timestamp or null>", "limit": 200 }
```

The gateway returns messages as an array:

```json
[{ "id": "...", "sender": "Alice", "content": "...", "timestamp": "..." }, ...]
```

Write the result to `~/tmp/messages.json` for the Explore subagent to scan.

If `get_history` is not available (IPC error), explain to the user that
message history retrieval is not supported in this environment.

### Step 2 — Spawn Explore subagent

Launch an Explore subagent with the question and the path to `~/tmp/messages.json`:

> Search `~/tmp/messages.json` for messages related to: `<question>`.
> Return matching messages with sender, timestamp, and content.
> Summarize what you found and how it relates to the question.

### Step 3 — Report

Summarize the findings. If nothing relevant found, say so clearly.
Do NOT fabricate matches or infer from partial text.

## Pagination

If the first 200 messages don't contain a match, ask the user whether
to go further back. Each subsequent call uses `before: <oldest timestamp seen>`.

## When to use

- "what did X say about Y last week?"
- "did we discuss Z before?"
- "what was the decision on X?"
- Anything referencing past conversation content (not stored knowledge)

For stored knowledge (facts, past research, diary): use `/recall-memories`.
