---
status: shipped
shipped: 2026-05-18
---

# routd-side reply-to-bot → `verb=mention` promotion

## Problem

A reaction or reply pointing at one of the bot's own messages should fire
the agent — it's the user directly engaging. It used to be adapter-side
and inconsistent: `discd` promoted (via a local `botMsgs` ring buffer);
`teled`, `whapd`, `slakd` did not, shipping reactions as
`verb=like`/`dislike` only. Operators who scoped routes to `verb=mention`
to filter noise saw Discord work and the other three silently miss every
reaction and text-reply directed at the bot.

## Decision: one renderer at the right ring

Duplicating the discd pattern to three more adapters means four ring
buffers of sent message ids, four register-on-Send hooks, four
check-on-receive paths — and the ring buffer races container restarts,
losing recently-sent ids. The information already lives in
`messages.is_bot_message`, which routd writes on every outbound. Adapters
stay dumb: they ship the raw verb and routd upgrades it at ingest,
**before `PutMessage` and before routing**, so every downstream path
(routing, observed window, prompt) sees one truth.

Explicit mentions, replies directed at a bot message, and reactions to bot
messages can engage. An untrusted sender cannot gain mention status.

## The thread root decides implicit mentions

Ambient thread messages promote only when the recorded root belongs to the
bot. `MessageByID(chat_jid, topic)` resolves the root by stored ID or platform
ID within that chat. A missing or human root does not promote, even when the
thread contains bot replies. Bot participation adds no addressing permission.

Slack sends `Topic = ReplyTo = thread_ts` for ambient thread messages.
That equal root pointer is implicit participation, not a deliberate reply.
Discord sends the parent-channel JID and thread-channel Topic; `ReplyTo`
comes only from an explicit message reference. A Discord root reply remains
a deliberate reply even when its reference equals Topic.

An explicit disengage blocks implicit bot-root follow-ups. The stop check
reads the thread's reply state and its root fallback. A fresh engagement
retires the stop. Explicit mentions and direct bot replies remain valid.

Topic inheritance runs before promotion. An empty topic names the main
timeline and adds no implicit mention. Adapter code does not decide root
ownership; the stored root's `BotMsg` does.

## What this is NOT

- **NOT a behavior change for catch-all routes.** They fired on
  `verb=like` already and keep firing.
- **NOT a routing-layer change.** `verb=mention` rules already existed;
  this makes them fire consistently across adapters.
- **NOT a cross-adapter id collision risk.** `messages.id` is the PRIMARY
  KEY, unique across the table.

## Migration

No schema change. Stored verbs are immutable, so promotion affects only
inbound from the moment of deploy. Routes need no touching.

The discd cleanup that came with it — removing `onReactionAdd`'s local
promotion, the `botMsgs` ring buffer, and the reply-to-bot branches of
`isMentioned`, while KEEPING the explicit `@<bot>` text-mention loop
(a different signal: the user typed `<@BOT_ID>`) — shrank production code
by more than the routd addition.
