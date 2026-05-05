---
status: planned
---

# Slink — Typing Indicator

The slink web chat has no typing indicator. When the agent is processing
a message, users see nothing until the full reply appears.

## What to add

- Typing indicator in the slink chat UI (animated dots or similar)
- Agent emits a typing event before/during response generation
- SSE stream delivers it to the client; UI shows it until the reply lands

## How

Gateway already sends `typing` events on native platforms (teled/whapd).
For slink:

1. `gated` emits a typing SSE event on the slink stream when a container
   starts processing a message for a group
2. `webd` forwards it down the `/slink/stream` EventSource
3. Slink frontend (`/workspace/web/pub/slink/`) renders it

No new API surface — SSE event type `typing` alongside existing `message`.
