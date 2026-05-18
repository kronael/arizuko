---
status: spec
depends: [B-route-mode-ingestion, F-topic-lineage, L-reply-to-bot-verb]
relates-to: [3/Y-thread-routing, 5/Y-output-styles-per-surface]
---

# specs/5/G — engagement: stay-in-conversation after mention; thread by default

## What this solves

Two operator complaints, one primitive:

1. **Mention-per-turn is brutal on Slack.** User has to `@bot` every
   reply. Slack autocomplete is laggy; falling back to `@U…` is
   miserable. Conversation flow dies after turn one.
2. **Bot replies pollute channels.** Long replies land top-level in
   the channel. Operators want bot output in threads, not in the main
   feed.

Single primitive: **engagement**. Once the bot has spoken in a
`(jid, topic)`, that pair is **engaged** until a TTL expires. Engaged
inbounds fire even when the route table wouldn't otherwise route
them to the bot. Bot can disengage explicitly. Combined with
thread-by-default on Slack channels, the engagement scope becomes
"the thread" and the channel stays clean.

## The primitive

A `(jid, topic)` has one of two states: **engaged** or **idle**.

Storage: two columns on the existing `chat_reply_state` table
(`store/migrations/0014-reply-state.sql` — keyed on `(jid, topic)`).

```sql
ALTER TABLE chat_reply_state ADD COLUMN engaged_until TEXT;
-- RFC3339Nano UTC. NULL = idle.
ALTER TABLE chat_reply_state ADD COLUMN engaged_folder TEXT NOT NULL DEFAULT '';
-- The folder claiming the engagement. Set on EVERY engagement write
-- (mention promotion, bot reply, MCP engage) so EngagedFolder() is a
-- single-row read independent of any prior reply having succeeded.
-- Empty = no claim.
```

`engaged_until` carries the **deadline** directly. `IsEngaged(jid,
topic)` is a single-row read. `engaged_folder` is the routing target
the engagement-fallback resolves to; written at the same site as
`engaged_until` so the pair never drifts.

## State transitions

All writes upsert the `(jid, topic)` row and set `engaged_until` in
one statement.

| Event                                                    | Write                                  |
| -------------------------------------------------------- | -------------------------------------- |
| Bot reply delivered (conversational outbound)            | `engaged_until = now + ENGAGEMENT_TTL` |
| Inbound `verb=mention` (includes 5/L reply-to-bot promo) | `engaged_until = now + ENGAGEMENT_TTL` |
| MCP `engage(jid, topic)`                                 | `engaged_until = now + ENGAGEMENT_TTL` |
| MCP `disengage(jid, topic)`                              | `engaged_until = NULL`                 |

**Outbound write sites — one renderer, many sinks.** Three sites write
on conversational outbound (gateway steered echo, gateway output
callback, MCP `recordOutbound`). They split the write into TWO calls
so the engagement bump shares ONE policy:

```go
func (s *Store) SetLastReply(jid, topic, replyID, folder string) error
// Always called. Writes last_reply_id + engaged_folder.

func (s *Store) BumpEngagement(jid, topic, folder string, until time.Time) error
// Called only when the active turn's trigger sender is NOT timed-*.
// Writes engaged_until + engaged_folder.
```

Every call site checks `strings.HasPrefix(triggerSender, "timed-")`
before invoking `BumpEngagement`. MCP `recordOutbound` reads the
current turn's trigger via `StoreFns.CurrentTriggerSender(folder)` —
gateway publishes the value at `processSenderBatch` entry and clears
on exit. Audit invariant: every `BumpEngagement` callsite must be
guarded by the same `!strings.HasPrefix(triggerSender, "timed-")`.

Why not `MarkMessageDelivered`: its signature is `(id, platformID)`,
no jid/topic in scope.

Why not `MarkMessageDelivered`: its signature is `(id, platformID)`,
no jid/topic in scope. Bumping there would require a JOIN-back to
`messages` for jid/topic, and the empty-text early-exit at
`gateway.go:981` calls it unconditionally (no actual send) — that
would falsely engage a never-delivered row.

**Inbound mention write site.** Spec 5/L already collapses
reply-to-bot to `verb=mention` BEFORE `PutMessage` at
`api/api.go:237-240`. There is one verb to watch. The write fires
immediately after the verb-promotion block, before `PutMessage`:

```go
if verb == "mention" {
    folder := s.store.DefaultFolderForJID(req.ChatJID)
    _ = s.store.SetEngagement(req.ChatJID, req.Topic, folder, now.Add(EngagementTTL))
}
```

`SetEngagement` writes both `engaged_until` and `engaged_folder` so
the mention path resolves the routing fallback on the first inbound,
not just after a prior bot reply has succeeded.

## Read path

```sql
SELECT engaged_until IS NOT NULL AND engaged_until > ?
  FROM chat_reply_state WHERE jid=? AND topic=?
```

One row, no JOIN, no subquery. No row → false (no prior bot
interaction in that pair). Helper: `Store.IsEngaged(jid, topic, now)
bool`.

## EngagedFolder helper

```go
func (s *Store) EngagedFolder(jid, topic string) string
// SELECT engaged_folder FROM chat_reply_state WHERE jid=? AND topic=?
```

One row, one column — the `engaged_folder` column is the single
source of truth. Survives turn-by-turn rewrites and is correct
even when `verb=mention` engaged the pair before any bot reply
has succeeded. Used by the routing fallback to deliver engaged
inbounds and by the MCP authz check below.

## Routing logic change

The current poll loop (`gateway/gateway.go:533-604`) has **no
mention gate**. Routing is route-table-driven via
`router.ResolveRouteTarget` (`:563`) plus sticky/reply-aware
`resolveTarget` (`:1846`). When no route matches earlier,
`resolveGroup` returns `!ok` at `:535` and the loop drops with
`"poll: no route for message"` (`:543`), advancing the cursor.

Engagement is the **implicit-route fallback**. After the existing
observe short-circuit (`:564`) and BEFORE the
`router.ResolveRouteTarget` fallthrough that delivers to the
resolved folder, the loop checks: was the prior bot reply in this
`(jid, topic)` still inside its engagement window? If yes, deliver
to that folder even if the route table wouldn't have.

Concretely, one helper used by both `pollOnce` and
`processGroupMessages`:

```go
func (g *Gateway) resolveOrEngaged(chatJid string, last core.Message) (core.Group, bool)
```

Falls through to engagement on `resolveGroup` miss: reads
`IsEngaged + EngagedFolder` and returns the engaged group if any.
Both poll sites (`pollOnce` and `processGroupMessages`) call this
helper instead of `resolveGroup`. Without the shared helper,
rescued-by-engagement inbounds drop on the worker path whenever the
container isn't already running.

Observe short-circuit is unchanged. Operators set `target=#observe`
precisely to silence the bot in that channel; engagement state must
not override.

Mention and reply-to-bot already route via the existing path
(route table + spec 5/L verb promotion). Engagement adds **no new
gate** for them; it only catches the inbounds that would otherwise
not route at all.

**Precedence over onboarding.** The `resolveGroup` miss branch
(`gateway.go:535-546`) also fires onboarding when enabled.
Engagement check fires **first**: if engaged, skip onboarding and the
cursor-drop, fall through to the engaged folder. Cross-cuts
onboarding because engagement requires a prior bot reply, which
already required a prior grant check at the original route time.
Grants are not re-validated on the engagement path — the lifecycle
invariant is "engaged ⇒ formerly granted." Operators revoke engaged
conversations via `disengage()` or wait for TTL.

## Thread-by-default on Slack channels

Today `slakd/bot.go:Send` already threads on `ReplyTo`
(`threadTS := cmp.Or(req.ThreadID, req.ReplyTo)`). Engagement does
not modify `Send()`. Instead the gateway ensures `ReplyTo` is set on
conversational outbounds — already done at `gateway.go:1009-1011`
where `replyTo = sentID` chains successive turns into the same
thread.

**Broadcast discriminator.** `m.Sender` prefix `timed-` is the
existing convention for scheduled / autonomous outbounds
(`gateway.go:1884, 1957`). Broadcast detection in the gateway
engagement bump: `strings.HasPrefix(triggerSender, "timed-")` →
no bump, no auto-thread. Conversational turns (any non-timed
trigger) bump. (`TurnID != ""` is **not** a valid discriminator
because timed/scheduled outbounds also carry non-empty TurnIDs.)

Net change in `slakd`: **zero LOC**.

## Corrective exchanges fork to a side thread

A "correction" turn is when the user is _meta-talking about the
bot's last reply_ rather than continuing the substantive
conversation: "no that's wrong because…", "you missed the context",
"rephrase that please".

These shouldn't pollute the main thread — plumbing, not content.
Convention only (no enforcement): the agent calls
`fork_topic(current, current+"#fix")` from spec 6/F, runs the
correction loop in the fork, posts a clean answer back into the
parent topic, then calls `disengage()` on the fix topic. Slack
threads the fork under the bot's incorrect message via the
thread-by-default rule. If the agent skips the fork, corrections
happen inline (today's behavior).

Agent prompt rule added to `ant/CLAUDE.md`.

## Length policy per surface

Per-surface output rules live in `specs/5/Y-output-styles-per-surface.md`.

## MCP tools

- **`engage(jid: string, topic: string)`** — sets `engaged_until =
now + ENGAGEMENT_TTL`. For scheduled / autonomous turns or
  recovery after a failed reply.
- **`disengage(jid: string, topic: string)`** — clears
  `engaged_until` (writes NULL). Subsequent inbounds need a fresh
  mention to re-engage.

Both args required — MCP sockets are per-folder, not
per-conversation, so there is no implicit "current" jid the tool
can default to. The agent passes the jid it intends to act on.

Both wrap `SetEngagement(jid, topic, folder, t time.Time)` — zero
`t` clears (NULL), non-zero writes `t.Format(RFC3339Nano)`. The
`folder` argument is the caller's folder; `engage` writes
`engaged_folder = callerFolder` so future inbounds steer to the
agent that just claimed the conversation.

**Authorization (three arms).** Agents can engage/disengage only
their own conversations. The MCP handler accepts the call if any
of:

1. `EngagedFolder(jid, topic) == callerFolder` — caller already
   owns the active engagement.
2. `JIDRoutedToFolder(jid, callerFolder)` — caller is the jid's
   default route target.
3. `EngagedFolder(jid, topic) == ""` — no current engagement
   (fresh chat). Escape hatch so scheduled / autonomous turns can
   bootstrap a conversation without a pre-existing route. Stealing
   an active engagement still requires arm 1 or 2.

Cross-folder calls against an active engagement return a permission
error. Implemented in `ipc/ipc.go` as a shared `engagementAuthz`
closure around both tool handlers.

## Env vars

- `ENGAGEMENT_TTL` (default `10m`) — window after a triggering
  event during which inbounds auto-fire via the engagement
  fallback.

## Migration

Migration `0058-engagement-column.sql` adds both columns in one
file (single deploy, single migration):

```sql
ALTER TABLE chat_reply_state ADD COLUMN engaged_until TEXT;
ALTER TABLE chat_reply_state ADD COLUMN engaged_folder TEXT NOT NULL DEFAULT '';
```

No backfill: NULL/empty = idle. Pre-existing rows stay idle until
the next bot reply or mention populates the columns. Zero behavior
change at migration time.

## Restart-safe

Nothing to recover. The column lives in SQLite (WAL); every
routing decision re-reads. A crash between `SetLastReply` and `BumpEngagement` leaves
state consistent — at worst the engagement window doesn't extend
for one outbound. Next inbound that's verb=mention re-engages.

## What this is NOT

- **NOT a sentinel-message scheme.** No `verb=disengage` synthetic
  message in the queue. Engagement is a column read; the queue is
  unchanged.
- **NOT a `last_reply_at` column.** The column carries the
  deadline (`engaged_until`), not the timestamp of the last reply.
  The TTL is applied at write time once.
- **NOT a per-route engagement override.** Operators wanting
  "always engaged" use a route `target=#bare` (already exists).
  Engagement is per-conversation-instance, not per-route.
- **NOT a multi-bot engagement model.** Multiple bots routed to
  the same folder each track their own `chat_reply_state` rows.
  No coordination.
- **NOT thread-creation for Discord/Telegram.** Discord threads
  need explicit API calls; Telegram threads are forum-channel
  specific. Slack-only.
- **NOT a disengage on every bot reply.** The bot stays engaged
  until TTL or explicit `disengage()`. A single reply doesn't end
  the conversation.

## Code surface

| File                                          | Change                                                                                            | LOC |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------- | --- |
| `store/migrations/0058-engagement-column.sql` | new — two `ALTER TABLE` (engaged_until, engaged_folder)                                           | 4   |
| `store/messages.go`                           | `SetLastReply` + `BumpEngagement` (split renderer), `SetEngagement`, `IsEngaged`, `EngagedFolder` | ~70 |
| `gateway/gateway.go` (poll + worker)          | `resolveOrEngaged` helper shared by `pollOnce` + `processGroupMessages` (one renderer for both)   | ~25 |
| `gateway/gateway.go` (three bump sites)       | `SetLastReply` unconditional + `BumpEngagement` guarded by `!strings.HasPrefix(sender, "timed-")` | ~12 |
| `api/api.go handleMessage`                    | `SetEngagement` on `verb=mention` after promotion                                                 | ~6  |
| `slakd/bot.go`                                | **no change** (Send already threads on ReplyTo)                                                   | 0   |
| `ipc/ipc.go`                                  | `engage`, `disengage` MCP tools wrapping `SetEngagement`                                          | ~50 |
| `core/config.go`                              | `ENGAGEMENT_TTL` (default `10m`)                                                                  | ~3  |
| `ant/CLAUDE.md`                               | corrective-fork convention                                                                        | ~8  |
| Tests (`store/store_test.go`, gateway, ipc)   | TTL window, MCP set/clear, thread auto-set, fallback routing                                      | ~80 |

**Net: ~235 LOC.**

## Tests

- `store/store_test.go`:
  - `SetEngagement(now+10m)` → `IsEngaged` true.
  - `SetEngagement(zero)` → `IsEngaged` false.
  - No row → `IsEngaged` false.
  - Past deadline → `IsEngaged` false.
  - `SetLastReply` preserves prior `last_reply_id` when called with empty replyID.
  - `BumpEngagement` writes `engaged_until` and `engaged_folder` together.
  - `EngagedFolder` reads `chat_reply_state.engaged_folder` directly (one query, no JOIN).
- `gateway/gateway_test.go` (`TestPollOnce_EngagementFallback_*`):
  - Route-miss + engaged → does NOT advance cursor (delivers to last folder).
  - Route-miss + idle → cursor advances (drops).
- `api/api_test.go` (`TestDeliverMessage_MentionWritesEngagement`):
  - `verb=mention` inbound writes `engaged_until`.
  - `verb=message` inbound does not.
- `ipc/ipc_test.go` (`TestServeMCP_Engagement_Authz`):
  - `engage` allowed when `EngagedFolder == callerFolder`.
  - `engage` allowed when `JIDRoutedToFolder(callerFolder)`.
  - `engage` denied on unowned jid.
  - `disengage` writes zero time and is denied for unowned jid.
- `slakd/`: no test needed (zero LOC change).

## Risks

- **TTL too long**: bot fires on stale inbounds after the user
  moved on. Mitigated by 10-minute default; operator-tunable.
- **Engagement after a bot error**: failed turn shouldn't engage.
  All three `BumpEngagement` call sites are gated on a non-empty
  platform reply id (`sentID != ""`); a Send that errored returns ""
  and skips the bump. The `timed-` sender prefix is also skipped
  uniformly across all three sites — single audit invariant.
- **Slack thread auto-creation on every mention is loud**: every
  bot reply creates a new thread the user didn't ask for.
  Mitigated by the engagement model — second reply in the same
  thread doesn't create another thread; it continues.
