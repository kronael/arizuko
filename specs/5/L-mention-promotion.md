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
