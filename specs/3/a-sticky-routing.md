---
status: shipped
---

# Sticky Routing — @group and #topic commands

Users in multi-agent/multi-topic chats shouldn't prefix every message.

## Command syntax

A message is a sticky command only if the **entire trimmed content** is
one of:

| Input    | Action                               |
| -------- | ------------------------------------ |
| `@name`  | Set sticky group to `name`           |
| `@`      | Clear sticky group (default routing) |
| `#topic` | Set sticky topic to `topic`          |
| `#`      | Clear sticky topic                   |

`@name hello` is not a sticky command — normal inline routing.

## State

Two nullable columns on `chats`: `sticky_group TEXT`, `sticky_topic
TEXT`. Scoped per `chat_jid` — all users in the same chat share state.

## Resolution

`resolveRoutingTarget` merges sticky with message:

- Group: sticky overrides normal group lookup
- Topic: sticky overrides message topic

Sticky overrides the group/topic resolution step; route rules still
fire within the sticky target.

## Feedback

- `"routing -> name"` on set
- `"routing reset to default"` on clear
- `"Failed: group \"xyz\" not found"` on bad target

## Edge cases

- Sticky + inline `@`: inline takes precedence for that one message;
  sticky persists.
- Cross-group @-routing: delegation from sticky target resolves
  normally. Reply returns to original chatJid.
- Bot messages: sticky commands from `is_bot_message=true` ignored.
- Scheduled tasks: sticky does not apply.
- Deleted group: fall through to default routing, log warning.
