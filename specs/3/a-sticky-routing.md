---
status: shipped
---

# Sticky Routing — @group and #topic commands

## Problem

In a group chat with multiple agents or topics, users must prefix every
message with `@agentname` or `#topic`. Noisy for extended conversations.

## Command syntax

A message is a sticky command if its **entire trimmed content** is one of:

| Input    | Action                                       |
| -------- | -------------------------------------------- |
| `@name`  | Set sticky group to `name`                   |
| `@`      | Clear sticky group (back to default routing) |
| `#topic` | Set sticky topic to `topic`                  |
| `#`      | Clear sticky topic                           |

Messages with additional content (`@name hello`) are NOT sticky commands —
they go through normal inline routing.

## State model

Two nullable columns on `chats` table: `sticky_group TEXT`,
`sticky_topic TEXT`. Scoped per `chat_jid` — all users in the same chat
share one sticky state.

## Routing resolution

`resolveRoutingTarget` merges sticky state with the message:

- Group: sticky overrides normal group lookup
- Topic: sticky overrides message topic

Sticky overrides the group/topic resolution step, not the routing rules
engine. Route rules (command/verb/pattern) still fire within the sticky
target group.

## Feedback messages

- `"routing -> name"` on set
- `"routing reset to default"` on clear
- `"Failed: group \"xyz\" not found"` on bad target

## Edge cases

- **Sticky + inline @**: inline routing takes precedence for that one
  message only. Sticky persists.
- **Cross-group @-routing**: delegation from sticky target resolves
  normally. Reply returns to original chatJid.
- **Bot messages**: sticky commands from `is_bot_message=true` ignored.
- **Scheduled tasks**: sticky state does not apply. Tasks always route
  to their own group.
- **Deleted group**: if sticky group folder no longer exists, fall
  through to default routing and log warning.
