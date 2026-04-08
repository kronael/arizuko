---
status: draft
---

# Unified Message Routing

## Problem

Current routing architecture conflates multiple concerns:

1. **Two message paths**: User messages go through DB+router, agent outputs bypass via callbacks
2. **Mixed addressing**: Folders (`root/foo`) vs JIDs (`telegram:123`) with no uniform format
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

| Type            | Format                 | Example                       |
| --------------- | ---------------------- | ----------------------------- |
| Telegram chat   | `telegram:<chat_id>`   | `telegram:-1001234`           |
| Discord channel | `discord:<channel_id>` | `discord:9876543`             |
| Web session     | `web:<user@domain>`    | `web:alice@example.com`       |
| WhatsApp        | `whatsapp:<jid>`       | `whatsapp:REDACTED@lid` |

**Groups use folder paths** (no prefix):

| Type        | Format     | Example                |
| ----------- | ---------- | ---------------------- |
| Agent group | `<folder>` | `root`, `root/reports` |

**Distinction:** Presence of `:` character in a routing target distinguishes
typed targets (`folder:`, `daemon:`, `builtin:`) from plain folder paths.
`folder:` is optional — bare paths without a `:` are always folders. JIDs
on their own (`telegram:123`) are channel addresses, not route targets.

**Deprecation:** The `local:` prefix (e.g., `local:root`) is removed. It
added no value — folder paths alone are unambiguous.

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
  source TEXT,                    -- adapter-of-record (inbound only)
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
    target = r.resolveTarget(msg)  // walks routes table, match language
  }

  // 2. Typed prefix disambiguates destination kind
  switch {
  case strings.HasPrefix(target, "daemon:"):
    return DaemonDestination{Name: strings.TrimPrefix(target, "daemon:")}
  case strings.HasPrefix(target, "builtin:"):
    return BuiltinDestination{Name: strings.TrimPrefix(target, "builtin:")}
  case strings.HasPrefix(target, "folder:"):
    return AgentDestination{Folder: strings.TrimPrefix(target, "folder:")}
  default:
    // Bare paths and legacy rows are folder targets
    return AgentDestination{Folder: target}
  }
}

func (r *Router) resolveTarget(msg Message) string {
  // 1. Content-based @mention (ONLY for user messages, explicit override)
  if strings.Contains(msg.Sender, ":") {  // user has colon, agent doesn't
    if mention := parseAtMention(msg.Content); mention != "" {
      return mention  // explicit @mention wins
    }
  }

  // 2. Reply chain routing (implicit continuation)
  if msg.ReplyToID != "" {
    if parent := r.store.GetMessage(msg.ReplyToID); parent != nil {
      // If parent was routed to agent, continue there
      if parent.RoutedTo != "" && !strings.Contains(parent.RoutedTo, ":") {
        return parent.RoutedTo
      }
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

**Priority order:**

1. **@mention** (explicit) overrides reply chain
2. **Reply chain** (implicit continuation) preserves conversation thread
3. **Sticky** (session default) for repeated interactions
4. **Default** (fallback) when nothing else applies

When @mention overrides reply chain, `reply_to_text` and `reply_to_sender` are still preserved — the new agent sees the context even though they weren't the original recipient.

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
  route_to string,    // explicit routing (root/foo, ""), requires delegate_group grant
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
    RoutedTo: routeTo,    // "" = router decides, "root/foo" = explicit
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
- `delegate_group(target=root/*)` — can explicitly invoke child agents
- These never overlap because agent messages don't trigger @mention parsing

---

## Implementation Status

**Completed (v0.25):**

1. **Agent outputs written to messages table** — `makeOutputCallback` writes
   via `PutMessage` with `sender=groupFolder, is_from_me=1, is_bot_message=1,
routed_to=chatJid`. Callback still delivers immediately for low latency.
2. **Delegation as messages** — `delegateViaMessage` writes a message to
   `local:targetFolder` with `forwarded_from=originJid` as return address.
   `processSenderBatch` checks `forwarded_from` to route output back.
3. **Escalation as messages** — same path as delegation. `<escalation_origin>`
   tag parsed to extract worker folder as return address.
4. **Removed `EnqueueTask`** — no more closure-based task queue. Delegation
   and `#topic` prefix both use `PutMessage` + `EnqueueMessageCheck`.
5. **Removed `OutboundEntry`/`StoreOutbound`** — unified into messages table.
6. **MCP tools** — `delegate_group`/`escalate_group` write messages directly.
   `send_message`/`send_reply` record via `PutMessage`.

**Future (not yet implemented):**

- Remove `local:` prefix — use bare folder paths for inter-group routing
- Poll-based outbound delivery — remove callback, gateway delivers from DB
- `status` column for message processing state tracking
- Unify `send_file` into `send_message(..., files=[...])`

---

## Benefits

1. **Audit trail** — all messages in DB, complete conversation history
2. **Crash recovery** — delegation messages survive restarts (DB-persisted)
3. **No closures** — no captured state lost on crash
4. **Debuggable** — inspect `forwarded_from` and `routed_to` in DB
5. **Simpler queue** — no task/message priority, just messages
6. **Extensible** — new channel types just add prefix case in router
