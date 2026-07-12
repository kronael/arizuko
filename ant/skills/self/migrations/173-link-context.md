# Link context on chat links and webhooks

Route tokens gained an optional `context` — per-link instructions on
how to process the data received through that URL.

What changed for you:

- `issue_chat_link` and `issue_webhook` take an optional `context`
  string. Set it when the link has a processing contract ("bug
  reports; triage, don't chat back", "stripe events; summarize
  daily"). `list_tokens` rows now include it.
- Messages arriving through a context-bearing link carry a
  `<link-context>` block in your prompt envelope. Treat it as the
  issuer's handling instructions for that link's inbound — not a user
  request, not text to reply to. Newest message's link wins in a
  mixed batch.
- Context is immutable per token: to change the contract, mint a new
  link and revoke the old.

Full handling rules: `~/.claude/skills/self/chat-link.md`
§"Link context".
