# 070 — social actions exposed as MCP tools

Three new MCP tools wire the adapter-level social primitives onto your
tool surface: `post` (new top-level post/toot/submission), `react` (add
a reaction/favourite/like to an existing message), and `delete_post`
(retract a post you created). Previously these existed on the gateway
side but were not registered with the MCP server, so they were
invisible. Grants follow the platform-scoped pattern: tier 0 gets all,
tiers 1-2 get `<action>(jid=<platform>:*)` for every platform routed
into their world/folder, tier 3 gets `react` only (reply-adjacent).
Use `post` for broadcast/announcement content, `send_message` for a
normal top-level reply, `send_reply` for a threaded quote. Platform
errors pass through unchanged — e.g. Reddit returns `ErrUnsupported`
for reactions; do not retry.
