---
status: shipped
shipped: 2026-05-18
---

# Gateway-side reply-to-bot → verb=mention promotion

## Problem

A reaction or reply pointing at one of the bot's own messages should
fire the agent — it's the user directly engaging. Today this is
adapter-side and inconsistent:

- `discd` promotes (both reactions and text replies) via local
  `botMsgs` ring buffer (`discd/bot.go:251, 584-590`)
- `teled`, `whapd`, `slakd` do NOT promote — reactions on bot
  messages ship as `verb=like` / `verb=dislike` only

Operators who scope routes to `verb=mention` to filter noise see
Discord work and the other three silently miss every reaction +
text-reply directed at the bot.

## Why not duplicate the discd pattern to three more adapters

Each adapter would need its own ring buffer of sent message IDs +
register-on-Send + check-on-receive. That's 4× the maintenance, and
the ring buffer races container restarts (loses recently-sent IDs).
The information already lives in `messages.is_bot_message` — gateway
writes it on every outbound (`gateway/gateway.go:955`). Adapter-side
duplication is the wrong layer.

## What ships

One renderer at the right ring: **gateway** promotes verb at inbound
ingest, BEFORE PutMessage and routing. Adapters stay dumb; they ship
the raw verb (`like` / `dislike` / `message`) and the gateway upgrades
to `mention` when the parent is bot-authored.

### Promotion rule

```
if msg.Verb != "mention" && msg.ReplyToID != "" &&
   store.IsBotMessageByID(msg.ReplyToID) {
    msg.Verb = "mention"
}
```

Three terms; all already present today.

### Where in code

`api/api.go` `handleMessage` immediately before `s.store.PutMessage`.
The store row carries the promoted verb so all downstream paths
(routing, observed-window, agent prompt) see one truth.

### Adapter cleanup

`discd/bot.go`:

- Remove `onReactionAdd` local promotion (line 251-253).
- Remove `isMentioned` reply-to-bot branches (lines 584-590) —
  the `botMsgs` ring-buffer check and the `ReferencedMessage.Author.ID
== botID` branch.
- KEEP the explicit `@<bot>` text-mention loop (lines 579-583) —
  that's a different signal (the user typed `<@BOT_ID>` in body).
- `botMsgs` ring buffer becomes vestigial; remove it.

`teled`, `whapd`, `slakd`: no change. They already emit
`ReplyTo: <bot-msg-id>` correctly; gateway now promotes uniformly.

## Tests

`api/api_test.go` (new or extend):

1. Inbound with `verb=like, reply_to=<bot-msg-id>` → stored verb is
   `mention`.
2. Inbound with `verb=like, reply_to=<user-msg-id>` → stored verb
   stays `like`.
3. Inbound with `verb=message, reply_to=<bot-msg-id>` → stored verb
   is `mention` (text reply to bot, all adapters).
4. Inbound with `verb=mention, reply_to=<bot-msg-id>` → stored verb
   stays `mention` (no double-promotion, no overwrite).
5. Inbound with `verb=like, reply_to=""` → stored verb stays `like`
   (reactions to nothing don't promote).
6. Inbound with `verb=like, reply_to=<missing-id>` → stored verb
   stays `like` (lookup misses, no promotion).

`discd` tests: any existing test of `onReactionAdd` that asserted
`verb=mention` for bot-msg reactions must move to api/api_test.go
(the assertion is now valid for ALL adapters, not just discord).

## What this is NOT

- NOT a behavior change for catch-all routes (`match=**`). They
  fired on `verb=like` already; they keep firing.
- NOT a change to the routing layer. `verb=mention` rules already
  exist; this just makes them fire across all adapters consistently.
- NOT cross-adapter ID collision. `messages.id` is unique across
  the table (PRIMARY KEY); a discord message ID and a telegram
  message ID can't collide on lookup.

## Thread replies are implicit reply-to-bot

The promotion keys on `ReplyTo`, but a platform thread doesn't deliver
each in-thread message as an explicit reply — it carries a thread anchor
(Slack `thread_ts`, Discord parent channel, …). An adapter that only sets
`Topic` from that anchor and leaves `ReplyTo` empty defeats the promotion:
a follow-up in a thread the bot started arrives as `verb=message`, so the
agent only re-attends while the spec 5/G engagement window is open, then
goes silent until the user re-@mentions.

Fix at the adapter (the layer that knows the thread shape): **set
`ReplyTo` = the thread root** for any in-thread message. The gateway
promotion then flips it to `mention` only when that root resolves via
`IsBotMessageByID` (`id` OR `platform_id`) — i.e. the thread was started
by one of the bot's own messages. Human-rooted threads don't resolve, so
they keep the engagement/mention path; no over-triggering.

`slakd` sets `ReplyTo = thread_ts` for `thread_ts != ts` messages
(`slakd/bot.go`). Other threaded adapters (`discd` parent channel,
`teled` forum topics) should follow the same rule when their thread model
is wired.

## Refinement — thread _participation_, not just thread _root_ (shipped)

> status of this section: **shipped** 2026-06-10. Requested 2026-06-09
> after the atlas/Slack channel debugging.

The shipped rule promotes a thread reply only when the thread _root_ is
bot-authored (`ReplyTo` = root → `IsBotMessageByID`). That misses the
common case: the bot **joined** a human-started thread (answered once),
the user replies again later in that thread — root is human, so it
arrives `verb=message` and the bot only re-attends while the `5/G`
engagement window is open, then goes silent until re-@mentioned. The
user experiences "it stopped listening mid-thread."

Refined intent (operator words): _a reply is a mention if it replies to
a bot message OR lands in a thread the bot **started or participated
in**; otherwise the normal engagement/attention window applies._

The ONE promotion site (routd `handleMessages`, before `PutMessage`) gains a
second test — keyed on **`(chat_jid, topic)`, not `(topic, folder)`**:

```go
if verb != "untrusted" && m.ReplyTo != "" &&
    (s.replyTargetIsBot(m.ReplyTo) ||                    // reply to a bot msg (shipped)
     s.threadHasBotMessage(m.ChatJID, m.Topic)) {        // bot participated in this thread (new)
    verb = "mention"
}
```

- **`threadHasBotMessage(chatJID, topic)`** — has the bot already posted in
  this thread? (`SELECT 1 FROM messages WHERE chat_jid=? AND topic=? AND
is_bot_message=1 LIMIT 1`). "Started" is the bot root; "participated" is
  _any_ bot row in the thread — this subsumes the shipped root-only rule.
- **Why `chat_jid`, not `routed_to`/`folder`** (correction to the draft): in
  the split, the owning folder is **unresolved at ingest** — route resolution
  runs after the row is stored (see `server.go` "engagement is NOT committed at
  ingress"). The chat is the precise thread container anyway (one folder serves
  many chats), and `m.ChatJID` is present on the inbound. Topic-inheritance
  (`m.Topic` from `TopicByID(ReplyTo)`) is resolved BEFORE the promotion so the
  check sees the thread topic.
- **Empty topic → false.** The chat's main timeline is not a thread; promoting
  there would re-fire on every root message in a channel the bot once spoke in.
  DMs / no-topic replies fall through to the reply-to-bot rule unchanged.
- Lives in `routd/server.go` as `threadHasBotMessage`, a sibling of the existing
  raw-SQL `replyTargetIsBot` — routd holds its own DB handle, not a
  `store.Store`, so the query is routd-local (consistent with `replyTargetIsBot`,
  not `store/messages.go`).
- Threads the bot never spoke in stay on the engagement/attention path (`5/G`)
  — no over-triggering in busy channels the bot merely observes.

This is still **one renderer at one ring** (routd ingest, before `PutMessage`):
participation is a property of the stored thread, not new adapter state. No
adapter change beyond the already-required `ReplyTo`/`Topic` wiring.

### Code surface (refinement, shipped)

| File                   | Change                                                                                                                |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `routd/server.go`      | new `threadHasBotMessage(chatJID, topic) bool`; promotion gains the OR term; topic-inheritance moved before promotion |
| `routd/parity_test.go` | `TestThreadParticipationPromotion`: participated-thread → mention; silent-thread → message; no-topic → message        |

## Migration

No schema change. Existing rows in `messages` keep their stored
verb (immutable). The promotion only affects NEW inbound from the
moment of deploy. Routes don't need touching.

## Code surface

| File                | Change                                            | LOC  |
| ------------------- | ------------------------------------------------- | ---- |
| `store/messages.go` | new `IsBotMessageByID(id) bool`                   | ~10  |
| `api/api.go`        | promotion block before PutMessage                 | ~5   |
| `discd/bot.go`      | remove `botMsgs` field + uses; trim `isMentioned` | ~−40 |
| Tests               | 6 cases above + discd test migration              | ~120 |

Net: **~95 LOC** including tests. Production code SHRINKS by ~25 LOC
(discd cleanup outweighs the gateway add).

## Open questions

None. The promotion rule is mechanical; ReplyToID is universally
present on the verbs that need promoting.
