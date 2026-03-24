# Unified Message Routing

## Problem

Current routing architecture conflates multiple concerns:

1. **Two message paths**: User messages go through DB+router, agent outputs bypass via callbacks
2. **Mixed addressing**: Folders (`main/foo`) vs JIDs (`telegram:123`) with no uniform format
3. **Routing bypasses**: `delegate_group` directly enqueues instead of using router
4. **Permission confusion**: `send_message` with @mentions could bypass `delegate_group` grants
5. **No audit trail**: Agent outputs never written to messages table

## Design Principles

1. **All messages flow through DB** — user input and agent output, same path
2. **Router is sole decision point** — no direct enqueue/callback shortcuts
3. **Uniform addressing** — everything is a JID with `prefix:identifier` format
4. **Routing is explicit** — `routed_to` field determines destination, no content parsing for agents

---

## Addressing Format

**Channels use JID format** (`prefix:identifier`):

| Type            | Format                 | Example                        |
| --------------- | ---------------------- | ------------------------------ |
| Telegram chat   | `telegram:<chat_id>`   | `telegram:-1001234`            |
| Discord channel | `discord:<channel_id>` | `discord:9876543`              |
| Web session     | `web:<user@domain>`    | `web:alice@example.com`        |
| WhatsApp        | `whatsapp:<jid>`       | `whatsapp:1234@s.whatsapp.net` |

**Groups use folder paths** (no prefix):

| Type        | Format     | Example                |
| ----------- | ---------- | ---------------------- |
| Agent group | `<folder>` | `main`, `main/reports` |

**Distinction:** Presence of `:` character distinguishes channel JIDs from group folders.

---

## Message Schema

```sql
CREATE TABLE messages (
  id TEXT PRIMARY KEY,
  chat_jid TEXT NOT NULL,        -- conversation context
  sender TEXT NOT NULL,           -- who wrote it (JID format)
  sender_name TEXT,               -- display name
  content TEXT NOT NULL,
  timestamp TEXT NOT NULL,

  -- Routing
  routed_to TEXT NOT NULL DEFAULT '',  -- where it goes (JID format)

  -- Threading
  reply_to_id TEXT,               -- message this replies to (UI threading)
  topic TEXT NOT NULL DEFAULT '', -- thread/topic identifier

  -- Metadata
  is_from_me INTEGER DEFAULT 0,
  forwarded_from TEXT,
  reply_to_text TEXT,
  reply_to_sender TEXT,
  source TEXT,
  group_folder TEXT,              -- DEPRECATED: use sender/routed_to instead
  attachments TEXT                -- JSON array of file paths
);
```

**New column: `attachments`** — files are part of the message, not separate operation.

---

## Router Logic

**Single decision point:**

```go
func (r *Router) Route(msg Message) Destination {
  target := msg.RoutedTo

  // 1. Explicit routing
  if target == "" {
    target = r.resolveTarget(msg)
  }

  // 2. Presence of colon determines type
  if strings.Contains(target, ":") {
    // JID format → channel delivery
    return ChannelDestination{JID: target}
  }

  // No colon → folder path → agent
  return AgentDestination{Folder: target}
}

func (r *Router) resolveTarget(msg Message) string {
  // 1. Reply chain routing
  if msg.ReplyToID != "" {
    if parent := r.store.GetMessage(msg.ReplyToID); parent != nil {
      // If parent was routed to agent, continue there
      if parent.RoutedTo != "" && !strings.Contains(parent.RoutedTo, ":") {
        return parent.RoutedTo
      }
    }
  }

  // 2. Content-based routing (ONLY for user messages, not agent messages)
  if strings.Contains(msg.Sender, ":") {  // user has colon, agent doesn't
    if mention := parseAtMention(msg.Content); mention != "" {
      return mention  // folder path
    }
  }

  // 3. Sticky routing
  if sticky := r.store.GetStickyGroup(msg.ChatJID); sticky != "" {
    return sticky
  }

  // 4. Default group
  return r.getDefaultGroup(msg.ChatJID)
}
```

**Key rule:** Agent messages (`sender` without `:`) NEVER get content-based routing. Only explicit `routed_to`.

---

## Gateway Loop

**Gateway becomes simple orchestration:**

```go
func (g *Gateway) messageLoop() {
  for {
    msg := g.store.PollNextUnprocessedMessage()
    if msg == nil {
      time.Sleep(pollInterval)
      continue
    }

    dest := g.router.Route(msg)

    switch dest := dest.(type) {
    case AgentDestination:
      g.handleAgentMessage(msg, dest.Folder)

    case ChannelDestination:
      g.handleChannelMessage(msg, dest.JID)

    case ErrorDestination:
      log.Error("routing failed", "msg", msg.ID, "err", dest.Error)
      g.store.MarkMessageProcessed(msg.ID, "error")
    }
  }
}

func (g *Gateway) handleAgentMessage(msg Message, folder string) {
  output := g.runner.Spawn(folder, msg)

  // Write output as new message
  g.store.PutMessage(Message{
    ChatJID: msg.ChatJID,
    Sender: folder,         // folder path (no colon)
    Content: output.Text,
    RoutedTo: msg.ChatJID,  // deliver back to channel (has colon)
    ReplyToID: msg.ID,      // thread it
    Topic: msg.Topic,
  })

  g.store.MarkMessageProcessed(msg.ID, "completed")
}

func (g *Gateway) handleChannelMessage(msg Message, jid string) {
  channel := g.channels.Get(jid)
  if channel == nil {
    log.Error("channel not found", "jid", jid)
    return
  }

  channel.Send(jid, msg.Content, msg.ReplyToID, msg.Topic)
  g.store.MarkMessageProcessed(msg.ID, "delivered")
}
```

**No callbacks, no direct enqueue.** Everything round-trips through DB.

---

## MCP Tools

**Unified send operation:**

```go
send_message(
  chat string,        // conversation context (telegram:123, web:alice@domain)
  text string,        // message content
  route_to string,    // explicit routing (main/foo, ""), requires delegate_group grant
  reply_to string,    // message ID to thread to
  files []string,     // attachment paths
)
```

**Authorization:**

```go
func handleSendMessage(req, agentFolder string, grants []Rule) error {
  chat := req.GetString("chat")
  text := req.GetString("text")
  routeTo := req.GetString("route_to", "")

  if routeTo != "" {
    // Explicit routing requires delegation grant
    if !grantslib.CheckAction(grants, "delegate_group", map[string]string{"target": routeTo}) {
      return error("unauthorized: explicit routing requires delegate_group grant")
    }
  } else {
    // Normal message send
    if !grantslib.CheckAction(grants, "send_message", map[string]string{"jid": chat}) {
      return error("unauthorized: cannot send to " + chat)
    }
  }

  store.PutMessage(Message{
    ChatJID: chat,
    Sender: agentFolder,  // folder path (no colon)
    Content: text,
    RoutedTo: routeTo,    // "" = router decides, "main/foo" = explicit
    Attachments: files,
  })

  return nil
}
```

**Simplified tool set:**

- `send_message(chat, text, route_to?, reply_to?, files?)` — the core send operation
- `delegate_group(group, prompt, chat, grants?)` — sugar for `send_message(chat, prompt, route_to=group)`
- `escalate_group(prompt, chat)` — sugar for `send_message(chat, prompt, route_to=parentFolder())`

Keeping separate tools preserves semantic clarity for agents and simplifies grant checks.

---

## Grant Model

**Tier-based defaults:**

```go
func deriveTier1Rules(worldJIDs []string) []string {
  rules := []string{
    "escalate_group",
    "delegate_group(target=*/*)",  // can delegate to immediate children
    "schedule_task",
    "reset_session",
  }

  // Platform-specific send grants
  for _, platform := range extractPlatforms(worldJIDs) {
    rules = append(rules,
      "send_message(jid="+platform+":*)",
      "send_file(jid="+platform+":*)",
    )
  }

  return rules
}

func deriveTier2Rules(folderJIDs []string) []string {
  // Tier 2: can only send to chats that have messaged this folder
  rules := []string{"send_reply"}
  for _, jid := range folderJIDs {
    rules = append(rules, "send_message(jid="+jid+")")
  }
  return rules
}
```

**Key separation:**

- `send_message(jid=telegram:*)` — can send to users, router decides agent routing
- `delegate_group(target=main/*)` — can explicitly invoke child agents
- These never overlap because agent messages don't trigger @mention parsing

---

## Migration Path

**Phase 1: Add `` prefix support (non-breaking)**

- Router accepts both `main` and `main` formats
- Store lookups try both formats
- New messages written with `` prefix
- Old code continues using folder paths

**Phase 2: Write agent outputs to messages table**

- Keep current callback for delivery
- Also write to messages table with `routed_to=chat_jid`
- Audit trail now complete

**Phase 3: Route agent outputs through router**

- Remove direct callback delivery
- Gateway polls agent output messages, routes to channels
- Validate no behavior change

**Phase 4: Deprecate folder path addressing**

- All new code uses `` prefix
- Migration script updates existing messages
- Remove legacy format support

---

## Benefits

1. **Audit trail** — all messages in DB, complete conversation history
2. **Uniform flow** — one routing algorithm, no special cases
3. **Testable** — router is pure function, no side effects
4. **Debuggable** — inspect `routed_to` in DB to understand message flow
5. **Secure** — no routing bypass via content parsing for agents
6. **Extensible** — new channel types just add prefix case in router

---

## Open Questions

1. **Message status tracking** — add `processed_at`, `status` columns?
2. **Performance** — polling vs notification for new messages?
3. **Concurrent routing** — how to prevent double-processing?
4. **Error handling** — dead letter queue for unroutable messages?
5. **Attachments storage** — file paths vs blob storage vs external URLs?
