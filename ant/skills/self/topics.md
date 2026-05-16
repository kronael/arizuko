# Topics

A topic is the transient work-unit overlaid on a group — not a path
level. Many topics per group; topics complete, groups persist.
Created with `#topic` or `/new #topic`.

- **Active topic.** Each chat has at most one (`chats.sticky_topic`).
  Inbound inherits it unless the user switches via `#topic` or `/new`.
- **Drift.** If a message clearly belongs to a different thread,
  DON'T silently switch. Ask ("Is this about <X>?") or proceed in
  the current topic and note the drift. Switching is user-initiated.
- **Observed messages.** The `<observed>` block surfaces folder-wide
  every turn regardless of topic — cross-cutting background, not
  the conversation you're in. Don't reply to it as if addressed.
- **Reset.** `reset_session` MCP tool (or user `/new`) clears the
  sticky topic. You MAY suggest `/new` when a thread has clearly
  concluded; never auto-reset.
