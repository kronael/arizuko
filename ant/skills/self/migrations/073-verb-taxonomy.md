# 073 — verb taxonomy

Tool renames (hard cutover, no aliases):

- `send_message` → `send`
- `send_reply`   → `reply`

Five new MCP tools (split chat vs feed surface):

- `forward(sourceMsgId, targetJid, comment?)` — chat: relay an inbound
  message to another chat with provenance preserved. Native on Telegram
  + WhatsApp. Other adapters return unsupported with a hint to
  `send` with quoted text.
- `quote(chatJid, sourceMsgId, comment)` — feed: republish a message
  with commentary. Native on Bluesky. Mastodon has no quote primitive
  and returns unsupported with a hint to `post(content=..., source_url=...)`.
- `repost(chatJid, sourceMsgId)` — feed: amplify without commentary.
  Native on Mastodon (boost) + Bluesky.
- `dislike(chatJid, targetId)` — feed: native 👎 on Discord, unsupported
  with hint elsewhere.
- `edit(chatJid, targetId, content)` — feed: modify a previously-sent
  bot message in-place. Native on Discord, Mastodon, Telegram, WhatsApp.
  Bluesky records are immutable — returns unsupported with a hint to
  `delete` + `post`. Email is unsupported (sent mail is immutable).

Errors with hints: when an adapter returns unsupported, the error
carries a structured `{tool, platform, hint}` body. The IPC layer
formats this as `unsupported: <tool> on <platform>\nhint: <alt>`.
Do not retry — read the hint and call the suggested alternative.

Tier scoping unchanged: tier 0–2 keep the same surface; tier 3+ gains
`edit` (defaults: `reply`, `send_file`, `like`, `edit`) so leaf rooms
can correct their own messages.

Inbound verb enums also gain `forward`, `quote`, `dislike` for symmetry
when those events arrive over an adapter that emits them. No router
behavior change — these are passthrough categories.

Action checklist:

- Replace any literal `"send_message"` / `"send_reply"` strings in your
  helpers/scripts with `"send"` / `"reply"`.
- When a verb is unsupported, read the hint and pick the suggested
  alternative tool. Do not retry the same call.
