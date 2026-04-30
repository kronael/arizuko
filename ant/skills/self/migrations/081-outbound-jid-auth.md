# 081 — outbound MCP tools enforce JID ownership

`send`, `send_file`, `like`, `delete`, `forward`, `quote`, `repost`,
`dislike`, `edit`, `post` now check that the JID you're addressing
belongs to a folder in YOUR subtree.

The rule:

- **Root (tier 0)**: unrestricted. You can send to any JID.
- **World / branch / room (tier 1+)**: you can only send to JIDs whose
  routed target folder is in your own subtree. e.g., agent in `mayai`
  can send to chats that route to `mayai`, `mayai/content`, `mayai/x/y`,
  but NOT to chats that route to `happy` or `rhias/content`.
- **Unrouted JIDs**: reachable only by root.

Cross-world sends now return `forbidden: chat <jid> belongs to folder
<X>, not in your subtree`. If you see this in a tool result, the JID
isn't yours to write to — don't retry, the answer is "this is the
wrong JID, route to a sibling agent via `delegate_group` instead."

Why: routing already enforces 1:1 inbound (one chat → one folder).
Outbound was unconstrained — any agent could spam any chat. That
caused release-announcement fan-out where the same chat received the
same notice from multiple worlds' agents at once.

You probably won't notice this — the JIDs you have in your context
are already from your world. The check is defensive against
cross-world JIDs that show up via memory, diary, or shared facts.
