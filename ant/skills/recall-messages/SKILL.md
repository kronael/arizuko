---
name: recall-messages
description: >
  Use when asked what someone said, what was discussed, or to find a past
  conversation. Searches chat message history, not facts/diary.
user_invocable: true
arg: <question>
---

# Recall Messages

Search chat message history for past conversations ("what did X say",
"did we discuss Y", decisions made in conversation).
For stored knowledge (facts, diary, episodes) use `/recall-memories`.

## Protocol

1. Fetch history:
   ```
   get_history({ chat_jid: "<jid from message context>", limit: 200 })
   ```
   Write the result to `~/tmp/messages.json`.

2. Spawn Explore subagent:

   > Search `~/tmp/messages.json` for messages related to: `<question>`.
   > Return matching messages with sender, timestamp, and content.
   > Summarize what you found and how it relates.

3. Report findings. Do NOT fabricate matches or infer from partial text.

## Pagination

If no match in 200 messages, ask the user whether to go further back.
Each subsequent `get_history` call uses `before: <oldest timestamp seen>`.
