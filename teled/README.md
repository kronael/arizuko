# teled

Telegram channel adapter.

## Purpose

Bridges Telegram Bot API to the router. Long-polls Telegram for updates,
posts inbound messages and reactions to `/v1/messages`, exposes the
universal verb surface for outbound. Serves `/files/<fileID>` as a CDN
proxy because Telegram bot-token URLs are short-lived.

## Verb coverage

| Verb        | Telegram primitive             | Status                                |
| ----------- | ------------------------------ | ------------------------------------- | ------- |
| `send`      | `sendMessage`                  | native                                |
| `send_file` | `sendPhoto`/`Video`/`Document` | native (extension-routed)             |
| `reply`     | `sendMessage` + `reply_to`     | native (folded into `send`)           |
| `post`      | `sendMessage` to channel chat  | native (text only; media → send_file) |
| `like`      | `setMessageReaction`           | native (defaults to 👍)               |
| `dislike`   | —                              | hint → `like(emoji='👎')`             |
| `delete`    | `deleteMessage`                | native                                |
| `forward`   | `forwardMessage`               | native (`source_msg_id="<chatJid>     | <id>"`) |
| `quote`     | —                              | hint → `reply`                        |
| `repost`    | —                              | hint → `forward`                      |
| `edit`      | `editMessageText`              | native (own messages only)            |
| `typing`    | `sendChatAction`               | native (refreshed every 4 s)          |

`fetch_history` returns `source: "unsupported"` — Telegram's Bot API has
no per-chat history surface; the gateway falls back to its local cache.

## Limitations

- Reactions are constrained to a per-chat allow-list set by the chat
  admins; `setMessageReaction` 400s for emojis outside that list.
- `delete` requires `can_delete_messages` admin right for messages
  authored by other users; the bot can always delete its own messages
  within 48 h.
- `edit` only works on the bot's own messages and within 48 h.
- `post` to a channel requires the bot to be added as a channel admin
  with post rights.
- Inline keyboards / buttons are not exposed (out of universal verb set).
- Long-poll only; no webhook mode.

## Responsibilities

- Authenticate with `TELEGRAM_BOT_TOKEN`, long-poll `getUpdates` with
  `allowed_updates=["message","message_reaction"]`.
- Post inbound messages and added emoji reactions to the router with
  `telegram:<chat_id>` JIDs.
- Persist poll offset under `$DATA_DIR/teled-offset-<name>` (atomic write).
- Proxy Telegram file downloads through `/files/<fileID>`.

## Entry points

- Binary: `teled/main.go`
- Listen: `$LISTEN_ADDR` (default `:9001`)
- Capabilities advertised: `send_text`, `send_file`, `typing`,
  `fetch_history`, `post`, `fwd`, `edit`, `like`, `delete`, `dislike`,
  `quote`, `repost`. Hint-only verbs are advertised so the agent reaches
  the adapter and receives the structured `UnsupportedError` hint instead
  of a generic "capability not advertised" message.

## Dependencies

- `chanlib` (Run, RouterClient, AdapterMux, env helpers, ClassifyEmoji)
- `matterbridge/telegram-bot-api/v6` (typed Message, raw `do()` for
  Bot API methods added after v6.5: `setMessageReaction`, `deleteMessage`,
  `getUpdates` with reactions)

## Configuration

- `TELEGRAM_BOT_TOKEN`, `ROUTER_URL`, `CHANNEL_SECRET`
- `LISTEN_ADDR`, `LISTEN_URL`, `CHANNEL_NAME`
- `DATA_DIR`, `MEDIA_MAX_FILE_BYTES`, `ASSISTANT_NAME`

## Health signal

`GET /health` returns 200 when connected to Telegram AND inbound activity
in the last 5 min (`chanlib.handleHealth`); 503 `{status:"disconnected"}`
when long-poll is failing.

## Files

- `main.go` — config, capability map, wiring via `chanlib.Run`
- `bot.go` — long-poll, verb implementations, `do()` helper
- `server.go` — adapter handlers for `/files/`

## Related docs

- `specs/4/1-channel-protocol.md`
- `EXTENDING.md` (adding a channel)
