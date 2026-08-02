---
status: shipped
---

# Thread-aware routing and reply chains

Three bugs drove this, and each fix is a durable rule.

## 1. Reply anchors must outlive the container

`lastSentID` was local to each run, so a multi-message conversation
never threaded. The anchor is now a persisted row —
`chat_reply_state(jid, topic, last_reply_id, engaged_folder)`
(`store/migrations/0014-reply-state.sql`, `store.SetLastReply`), keyed
by `(chat_jid, topic)` and updated after every bot send. Steering
messages write it immediately so the output callback picks up the new
target mid-run.

## 2. `Topic` is the single thread identifier

Every adapter maps its native concept onto `Message.Topic` — Telegram's
`MessageThreadID`, a Discord thread's channel ID, a Slack `thread_ts`, a
web URL slug — and the platform-specific shape stops there. Sessions key
on `(folder, topic)`, and `Channel.Send(jid, text, replyTo, threadID)`
passes it back out.

Topic is **opaque and never compared across platforms**. Making it one
string rather than a per-adapter union is what lets `get_thread` and
session lookup work with no adapter-specific branches.

## 3. Reply-chain routing resolution order

First match wins:

1. Inline `@name` in content → that group
2. `ReplyToID` present → the `routed_to` recorded on that message
3. Sticky group set → that group
4. Default group for this JID

`routed_to` is set on `messages` at bot-send time, which is what makes
step 2 possible: without it, a reply to an old message would re-resolve
through current routing and could land in a different folder than the
message it answers.

## Session continuity falls out of this

Per-`(folder, topic)` session IDs _are_ the reply context. Once Topic
maps to the inbound thread ID, every message in a thread shares one
session, so explicit `reply_to_text` threading is redundant for the
sessionized case.
