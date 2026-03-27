---
status: draft
---

# Sticky Routing — @group and #topic commands

## Problem

In a group chat with multiple agents or topics, users have to prefix every
message with `@agentname` or `#topic` to route it correctly. This is noisy
for extended conversations directed at one agent or within one thread.

## Design

### Command syntax

A message is a sticky command if its **entire trimmed content** is one of:

| Input    | Action                                       |
| -------- | -------------------------------------------- |
| `@name`  | Set sticky group to `name`                   |
| `@`      | Clear sticky group (back to default routing) |
| `#topic` | Set sticky topic to `topic`                  |
| `#`      | Clear sticky topic                           |

Any message with additional content (`@REDACTED hello` or `hey @REDACTED`) is NOT
a sticky command — it goes through normal routing with the @ parsed inline.

### State model

Two new nullable columns on the `chats` table:

```sql
ALTER TABLE chats ADD COLUMN sticky_group TEXT;   -- group folder name or NULL
ALTER TABLE chats ADD COLUMN sticky_topic TEXT;   -- topic string or NULL
```

Scoped per `chat_jid`. All users in the same chat share one sticky state.
This matches group chat semantics — the channel context is shared.

Store methods:

```go
SetStickyGroup(chatJID, folder string)  // "" = clear
GetStickyGroup(chatJID string) string
SetStickyTopic(chatJID, topic string)   // "" = clear
GetStickyTopic(chatJID string) string
```

### Gateway logic

In `processChat` (and `processWebTopics`), before routing:

```go
// 1. Detect and handle sticky commands
content := strings.TrimSpace(last.Content)
if isStickyCommand(content) {
    handleStickyCommand(g, chatJid, content, ch)
    g.advanceAgentCursor(chatJid, msgs)
    return true, nil
}

// 2. Apply sticky state to message routing
group, topic := g.resolveRoutingTarget(chatJid, last)
```

`isStickyCommand` returns true if content matches `^[@#]\S*$` exactly (one
`@` or `#` optionally followed by non-space chars, nothing else).

`handleStickyCommand`:

```go
func (g *Gateway) handleStickyCommand(chatJid, content string, ch core.Channel) {
    switch {
    case content == "@":
        g.store.SetStickyGroup(chatJid, "")
        g.sendMessage(chatJid, "routing reset to default")
    case strings.HasPrefix(content, "@"):
        name := content[1:]
        // validate group exists
        if g.folders.GroupExists(name) {
            g.store.SetStickyGroup(chatJid, name)
            g.sendMessage(chatJid, fmt.Sprintf("routing → %s", name))
        } else {
            g.sendMessage(chatJid, fmt.Sprintf("Failed: group %q not found", name))
        }
    case content == "#":
        g.store.SetStickyTopic(chatJid, "")
        g.sendMessage(chatJid, "topic reset to default")
    case strings.HasPrefix(content, "#"):
        topic := content[1:]
        g.store.SetStickyTopic(chatJid, topic)
        g.sendMessage(chatJid, fmt.Sprintf("topic → %s", topic))
    }
}
```

`resolveRoutingTarget` pulls the sticky state and merges with the message:

```go
func (g *Gateway) resolveRoutingTarget(chatJid string, msg core.Message) (group core.Group, topic string) {
    // group: sticky overrides normal group lookup
    stickyGroup := g.store.GetStickyGroup(chatJid)
    if stickyGroup != "" {
        // find group by folder name
        group = g.findGroupByFolder(stickyGroup)
    } else {
        group = g.findGroupForJID(chatJid)
    }

    // topic: sticky overrides message topic
    stickyTopic := g.store.GetStickyTopic(chatJid)
    if stickyTopic != "" {
        topic = stickyTopic
    } else {
        topic = msg.Topic
    }
    return
}
```

### Interaction with existing routing rules

Existing per-JID routes (`routes` table) and group lookup still fire when no
sticky is set. Sticky overrides silently — it replaces the group/topic
resolution step, not the routing rules engine. This means:

- Route rules (command/verb/pattern) still fire on the message content within
  the sticky target group
- If the sticky group folder no longer exists (deleted), fall through to
  default routing and log a warning

### Feedback messages

Confirmations use lowercase (info pattern per CLAUDE.md):

- `"routing → REDACTED"` on set
- `"routing reset to default"` on clear
- `"topic → support"` on set
- `"topic reset to default"` on clear
- `"Failed: group \"xyz\" not found"` on bad target

### Edge cases

- **Sticky + inline @**: if sticky_group is set and the message also contains
  an inline `@other`, the inline routing takes precedence for that one message
  only (the per-message router already handles this). Sticky persists.

- **Cross-group @-routing**: when sticky_group routes to group B, and group B
  uses @-routing to delegate to group C, the delegation resolves normally.
  Reply returns to original chatJid (per spec 7/1).

- **Bot messages**: sticky commands from `is_bot_message=true` senders are
  ignored (bots cannot change routing state).

- **Scheduled tasks**: tasks run with `sender = "scheduler-*"` — sticky state
  does not apply to scheduled messages. Tasks always route to their own group.

---

## Implementation order

1. Schema: add `sticky_group`, `sticky_topic` to `chats`
2. Store: add get/set methods
3. Gateway: `isStickyCommand` + `handleStickyCommand` + `resolveRoutingTarget`
4. Tests: unit test command detection, store round-trip, routing override

Migration file: `NNN-sticky-routing.sql`

```sql
ALTER TABLE chats ADD COLUMN sticky_group TEXT;
ALTER TABLE chats ADD COLUMN sticky_topic TEXT;
```
