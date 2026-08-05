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

`routd/server.go:480`:

```go
if verb != "untrusted" &&
    ((m.ReplyTo != "" && s.db.ReplyTargetIsBot(m.ReplyTo)) ||
        s.db.ThreadHasBotMessage(m.ChatJID, m.Topic)) {
    verb = "mention"
}
```

**The guard is `verb != "untrusted"`, not `!= "mention"`.** An untrusted
sender (emaid marks unverified senders `verb=untrusted`,
[`11/17-emaid-auth.md`](../11/17-emaid-auth.md)) must
not escalate to a mention this way — that would let a spoofed inbound
drive an agent turn. Re-promoting an already-`mention` inbound is a
harmless no-op, so no anti-double-promotion guard is needed.

## Thread _participation_, not just thread _root_

A platform thread doesn't deliver each in-thread message as an explicit
reply — it carries a thread anchor (Slack `thread_ts`, Discord parent
channel). Two refinements, both driven by live debugging:

**Adapters set `ReplyTo` = the thread root for in-thread messages.** The
adapter is the layer that knows the thread shape. Without it a follow-up
in a thread the bot started arrives as `verb=message`, so the agent
re-attends only while the [`G-engagement.md`](G-engagement.md) window is
open and then goes silent until re-@mentioned. Human-rooted threads don't
resolve as bot messages, so this never over-triggers.

**Root-only was still too narrow** (atlas/Slack, 2026-06-09). The common
case is that the bot _joined_ a human-started thread: root is human, so
later replies arrive `verb=message` and the user experiences "it stopped
listening mid-thread". Operator's words: _a reply is a mention if it
replies to a bot message OR lands in a thread the bot started or
participated in._ Hence the second term,
`ThreadHasBotMessage(chat_jid, topic)` (`routd/db.go:1220`) — any bot row
in the thread, which subsumes the root-only rule.

**Keyed on `chat_jid`, not `routed_to`/folder.** In the split the owning
folder is unresolved at ingest (route resolution runs after the row is
stored). The chat is the precise thread container anyway — one folder
serves many chats — and `ChatJID` is present on the inbound.

**Topic inheritance runs BEFORE promotion** (`routd/server.go:475`) so
the participation check sees the thread topic rather than an empty one.

**Empty topic → false.** The chat's main timeline is not a thread;
promoting there would re-fire on every root message in any channel the
bot once spoke in. DMs and no-topic replies fall through to the
reply-to-bot rule unchanged.

**A bot message that STARTS a thread must be stamped with the thread as
its topic** (fix 2026-06-14, `routd/turns.go:314`). Such rows were stored
with `topic=""`, so `ThreadHasBotMessage` never found them and Discord
threads never promoted.

Adapter models still differ by platform — slakd sets
`ReplyTo = thread_ts`; discd uses channel-as-thread with the topic
inherited via `TopicByID`; teled sets `ReplyTo` for explicit replies.
Telegram forum topics are a known edge case, unaddressed until reported
broken.

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
