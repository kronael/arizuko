---
status: spec
depends: [B-route-mode-ingestion, F-topic-lineage]
relates-to: [3/Y-thread-routing]
---

# specs/6/G — engagement: stay-in-conversation after mention; thread by default

## What this solves

Two operator complaints, one primitive:

1. **Mention-per-turn is brutal on Slack.** User has to `@bot` every
   reply. Slack autocomplete is laggy; falling back to `@U…` is
   miserable. Conversation flow dies after turn one.
2. **Bot replies pollute channels.** Long replies land top-level in
   the channel. Operators want bot output in threads, not in the main
   feed.

Single primitive: **engagement**. Once the bot has spoken in a
`(jid, topic)`, that pair is **engaged** for a TTL. Engaged turns
fire on every inbound, no re-mention needed. Bot can disengage
explicitly. Combined with thread-by-default on Slack channels, the
engagement scope becomes "the thread" and the channel stays clean.

## The primitive

A `(jid, topic)` has one of two states: **engaged** or **idle**.

- **Triggered to engaged**: mention OR reply-to-bot OR explicit
  `fork_topic` AND first bot reply in that topic.
- **Engaged → engaged**: any inbound in the pair while `now() -
last_reply_at < ENGAGEMENT_TTL` fires the agent without
  requiring mention.
- **Engaged → idle**: TTL expires OR bot calls `disengage()` MCP.
- **Disengaged stays idle**: subsequent inbounds need a fresh
  mention to re-engage.

`chat_reply_state` already keys on `(jid, topic, last_reply_id)`.
Add one column:

```sql
ALTER TABLE chat_reply_state ADD COLUMN last_reply_at TEXT;
```

RFC3339Nano UTC, set on every bot outbound. The freshness check at
routing time is a column comparison; no new table, no new index
(existing PK is enough).

## Routing logic change

In gateway's poll loop, after route resolution and before observe-
vs-trigger decision (gateway.go:554):

```go
switch {
case routeMode == "observe":
    // existing: store and continue
case verb == "mention":
    // existing: trigger
case engaged(jid, topic):
    // NEW: trigger (no mention needed)
case replyToBotMsg(last):
    // NEW: trigger (reply-to-bot is implicit re-engagement)
default:
    // existing: observe-only or drop
}
```

`engaged()` is: `chat_reply_state.last_reply_at > now() - ENGAGEMENT_TTL`.

`replyToBotMsg(last)` is: `last.ReplyToID` resolves to a row with
`is_bot_message=1`. Already trivially computable from existing
schema — one query per turn, indexed on (id, chat_jid).

## Thread-by-default on Slack channels

Two rules, both adapter-local in `slakd/bot.go:Send()`:

1. **Always thread in non-DM, non-mpim channels** — when sending to
   `slack:T/channel/C…`, set `thread_ts` to the parent message ts
   (the trigger's ts, or its ReplyTo if it's already in a thread).
2. **Preserve existing thread** — if `req.ThreadID` or `req.ReplyTo`
   is already set (today's path for in-thread replies), leave it.

Net effect: bot opens a thread on the user's mention. Subsequent
thread replies engage automatically (rule above). Channel feed stays
free of bot output.

No new field on outbound: `chanlib.SendRequest.ThreadID` exists;
slakd just chooses to populate it when missing AND target is a
top-level channel message.

## Corrective exchanges fork to a side thread

A "correction" turn is when the user is _meta-talking about the
bot's last reply_ rather than continuing the substantive
conversation. Examples: "no that's wrong because…", "you missed
the context", "rephrase that please", "ignore the last bit".

These exchanges shouldn't pollute the main thread — they're
plumbing, not content. The main flow should look like
`[user-q] [bot-a] [user-q2] [bot-a2]…` even when corrections
happened along the way.

**Mechanism**: when the agent detects it's mid-correction (a tool
the agent calls explicitly, or a heuristic on user inbound), it
forks a side topic via `fork_topic(current, current+"#fix")` and
continues the correction loop there. Once converged, agent can
post a single corrected reply back into the main topic and call
`disengage()` on the fix topic.

On Slack specifically: the side-fork lands in a new thread off
the bot's incorrect reply (sets `thread_ts` to the bot's message
ts). User sees the correction loop in-thread; main channel/thread
stays clean.

Implementation: leverages existing `fork_topic` MCP from spec 6/F

- thread-by-default outbound (rule above). No new primitive. Agent
  prompt rule (added to ant CLAUDE.md): "if the user is correcting
  your last reply rather than asking a new question, fork the topic
  to `<current>#fix` and continue there. Return a clean answer to
  the parent topic when convergence reached."

This is a **convention**, not enforced by the gateway. If the
agent doesn't fork, corrections happen inline (today's behavior).
Operators who want enforcement can configure a route mode (future)
or a skill that wraps the detection + fork.

## Length policy per surface

`buildAgentPrompt` gains a one-line `<surface>` hint:

```
<surface>slack-channel-thread</surface>     # threaded reply, full ceiling
<surface>slack-channel</surface>             # rare: top-level, hard-cap 200ch
<surface>slack-dm</surface>                  # full ceiling
<surface>slack-pane</surface>                # AI sidebar, full ceiling
<surface>discord-channel</surface>           # mention-triggered, default ceiling
<surface>telegram-group</surface>            # same
<surface>telegram-dm</surface>               # full ceiling
```

Computed from `chat_jid` shape + thread context. Agent's prompt
rule (in `ant/CLAUDE.md`) reads this and self-caps. Surface is a
hint, not enforcement — the agent decides.

## MCP tools

- **`disengage(jid?: string)`** — clears `last_reply_at` for
  current `(jid, topic)` (or the explicit jid). Bot calls this when
  it's done helping. Subsequent inbounds need mention to re-engage.
- **`engage(jid?, topic?)`** — explicit re-engagement without a
  user mention. Useful for scheduled / autonomous turns.

Both wrap a single store helper `SetEngagement(jid, topic, t)`.

## Migration

```sql
-- 0056-engagement.sql
ALTER TABLE chat_reply_state ADD COLUMN last_reply_at TEXT;
-- No backfill: NULL = idle. First bot reply per topic populates it.
```

One column. No index changes (existing PK covers the lookup). Zero
behavior change at migration — until a bot replies, every topic is
idle, today's mention-required behavior holds.

## Env vars

- `ENGAGEMENT_TTL` (default `10m`) — sliding window after last bot
  reply during which inbounds auto-trigger.
- `SLACK_CHANNEL_HARD_CAP_CHARS` (default `200`) — char ceiling for
  the rare top-level channel reply.

## What this is NOT

- **NOT a per-topic engagement override.** Operators can't pin a
  topic to "always engaged" via config — they'd use a route
  `target=#bare` (already exists) to make every inbound fire.
  Engagement is per-conversation-instance, not per-route.
- **NOT a multi-bot engagement model.** If multiple bots are routed
  to the same folder, they each track their own `chat_reply_state`
  rows. No coordination.
- **NOT thread-creation for Discord/Telegram.** Discord threads need
  explicit API calls (`startThreadFromMessage`); Telegram threads
  are forum-channel-specific. Slack is the only platform with cheap
  threading at every channel message. The thread-by-default rule
  ships Slack-only; other adapters keep current top-level reply.
- **NOT a disengage on every bot reply.** The bot stays engaged
  until TTL or explicit call. A single reply doesn't end the
  conversation.

## Code changes

| File                                      | Change                                                        | LOC |
| ----------------------------------------- | ------------------------------------------------------------- | --- |
| `store/migrations/0056-engagement.sql`    | new                                                           | 3   |
| `store/chat_reply_state.go` (or wherever) | `SetEngagement`, `IsEngaged`                                  | ~30 |
| `gateway/gateway.go`                      | engagement check in routing; surface hint in buildAgentPrompt | ~25 |
| `slakd/bot.go`                            | thread_ts auto-set for top-level channel replies              | ~15 |
| `ipc/ipc.go`                              | `disengage`, `engage` MCP tools                               | ~50 |
| `core/config.go`                          | `ENGAGEMENT_TTL`, `SLACK_CHANNEL_HARD_CAP_CHARS`              | ~6  |
| `ant/CLAUDE.md`                           | `<surface>` rule + per-surface caps                           | ~15 |
| Tests                                     | engagement TTL, thread auto-set, disengage path               | ~80 |

**Net: ~225 LOC.**

## Migration order

1. **Schema migration 0056** — adds nullable column, no behavior change.
2. **`SetEngagement` write path** — gateway sets `last_reply_at` on
   every bot outbound. Read path unused yet. No behavior change.
3. **`IsEngaged` read path + routing logic** — engaged topics
   auto-trigger. **First behavior change.** Ship to sloth for live
   validation.
4. **Thread-by-default in slakd** — channel mentions reply in
   thread. Validate on marinade (atlas).
5. **`<surface>` hint + ant/CLAUDE.md rule** — agent self-caps.
6. **`disengage`/`engage` MCP tools** — operator/agent explicit
   control.

Each phase ships and verifies live before the next.

## Risks

- **TTL too long**: bot fires on stale inbounds long after the
  user moved on. Mitigated by 10-minute default; operator-tunable.
- **Engagement after a bot error**: failed turn shouldn't set
  engagement (it would re-engage on every retry). `last_reply_at`
  is set only on successful outbound (already the gate for
  `chat_reply_state.last_reply_id`).
- **Slack thread auto-creation on every mention is loud**: every
  bot reply creates a new thread the user didn't ask for. Mitigated
  by the engagement model — second reply in the same thread doesn't
  create another thread; it continues.
- **Bot "stuck" engaged**: TTL prevents permanent engagement. If
  bot crashes after writing `last_reply_at` but before another
  inbound, next inbound just triggers (correct, since the bot WAS
  engaged when it crashed).

## Open questions

- **TTL reset on user inbound vs only on bot reply?** Spec proposes
  reset on bot reply only (sliding window from last bot turn).
  Alternative: reset on every inbound while engaged (sliding window
  from last activity either side). The former is simpler; the
  latter avoids the "10 minutes of typing → bot replies once → 10
  minutes more typing → bot times out" failure.
- **Should `disengage` be silent or send a "ok, stepping out"
  message?** Spec leaves it silent; operator can override via a
  skill that wraps `disengage` + `send`.
- **`engage(jid?, topic?)` without args — does it engage the
  current `(jid, topic)` or all open conversations?** Spec says
  current only; explicit args required for cross-conversation.
