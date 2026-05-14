# 016 — inject_message action

New gateway action `inject_message` available via IPC requests.

Inserts a message directly into the DB without sending to the channel.
Useful for programmatic retry after OOM kills or admin intervention.

## Usage

Write a JSON request file to the IPC requests directory:

```json
{
  "id": "req-1",
  "type": "inject_message",
  "chatJid": "123456@g.us",
  "content": "retry this message",
  "sender": "system",
  "senderName": "system"
}
```

The message loop picks it up via `getNewMessages()` and processes
it normally, spawning a container. The message never appears in
the channel — only in the DB.

Also clears the `errored` flag on the chat, so the message loop
won't skip it.

## Authorization

Root and world groups only (tier ≤ 1). Other groups get
`unauthorized` error.
