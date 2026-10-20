---
status: shipped
depends: [F-topic-lineage, L-mention-promotion]
relates-to: [5/Y-output-styles-per-surface]
---

# specs/5/G — engagement: stay in the conversation after a mention

## What this solves

Two operator complaints, one primitive:

1. **Mention-per-turn is brutal on Slack.** The user has to `@bot` every
   reply; autocomplete is laggy and falling back to `@U…` is miserable.
   Conversation flow dies after turn one.
2. **Bot replies pollute channels.** Long replies land top-level.
   Operators want bot output in threads.

Single primitive: **engagement**. Once the bot has spoken in a
`(jid, topic)`, that pair is engaged until a TTL expires; engaged
inbounds fire even when the route table wouldn't route them. Combined
with thread-by-default on Slack, the engagement scope _is_ the thread
and the channel stays clean.

## The primitive

A `(jid, topic)` is **engaged** or **idle**. Storage is two columns on
`chat_reply_state`: `engaged_until` (RFC3339Nano deadline, NULL = idle)
and `engaged_folder` (who claims it). `IsEngaged` and `EngagedFolder`
are single-row reads — no JOIN, no subquery, no row → false.

**The column carries the deadline, not the last-reply timestamp.** TTL
is applied once at write time, so every read is a plain comparison.

**`engaged_folder` is written at every site that writes `engaged_until`**
so the pair can never drift, and so the routing fallback resolves on the
first mention rather than only after a prior bot reply succeeded.

## Write sites — explicit choice and automatic renewal

`SetLastReply(jid, topic, replyID, folder)` records the reply anchor.
`BumpEngagement(jid, topic, folder, until)` makes an explicit choice.
A zero or expired deadline records `disengaged_at` on the same reply-state row.
Explicit engagement can open a window at any time. Explicit engagement and
accepted fresh renewal remove the prior stop marker atomically. Natural TTL
expiry does not restore that prior stop.

`RenewEngagement(turnID, jid, topic, folder, until)` handles every automatic
renewal from final prose and delivered MCP answers. It reads the turn's stored
`started_at`. A disengage at or after dispatch blocks renewal for that turn,
including replies that create a thread from the source conversation.
The write guards the source and destination clear timestamps atomically.
A concurrent disengage refuses renewal. A fresh mention starts a new turn and
can engage again. Clear state stays scoped to its `(jid, topic)`.

Timed turns never renew engagement. An empty platform reply id cannot renew
an MCP answer. The reply anchor survives disengage.

The agent calls `disengage` when the user asks it to stop or when a clearly
bounded request is complete. The agent can send the answer before or after
that call. The answer cannot cancel the explicit choice to stop.

## Engagement is claimed at DISPATCH, not at ingress

`routd/server.go:530`: the owning folder is unresolved at ingress —
route resolution runs after the row is stored. A pre-`PutMessage` claim
with an empty folder makes `Engaged` return `("", true)` and misroute.
routd defers the claim to dispatch, where the resolved folder is known.

(An earlier revision of this spec specified the write at ingress, before
`PutMessage`, for transactional tidiness. The split made that
impossible; the code comment records the reason.)

**Reaction topic inheritance** (`routd/server.go:516`). Reactions arrive
with an empty `Topic` and a `ReplyTo` pointing at the reacted-to message,
so routd sets `Topic = TopicByID(ReplyTo)` before promotion. Without it,
a reaction on a threaded message lands in the main engagement scope.
Adapters with no thread id produce an empty `ReplyTo`; the topic stays
empty and that is correct.

## Engagement is a full route override

`routd/loop.go:611` `resolve` checks engagement inside the route-hit
branch **and** as a route-miss fallback: an engaged pair beats the route
table entirely — including `#observe` targets and routes pointing at a
different folder — and rescues an inbound that matches nothing.

Rationale: once the bot has replied in a thread the user expects a live
conversation. Silencing it mid-thread — via `#observe`, a route
elsewhere, or a miss — is worse than the operator's original intent to
reduce noise on cold messages.

**Topic-root normalization is load-bearing.** Engagement is recorded on
the root topic (`topic=""`) when the agent answers an @mention, but
thread replies arrive with `topic="<thread_ts>"`. `resolve` falls back to
the root when the thread topic has no record of its own; without it
every threaded follow-up misses its own engagement. An explicit clear on the
thread blocks root fallback, even while the root window stays live.

**Precedence over onboarding.** The route-miss branch also fires
onboarding. Engagement is checked first — engaged means skip onboarding
and the cursor drop. Grants are not re-validated on the engagement path;
the lifecycle invariant is "engaged ⇒ formerly granted". Operators revoke
with `disengage()` or wait out the TTL.

Mention and reply-to-bot already route via the route table plus
[`L-mention-promotion.md`](L-mention-promotion.md) verb promotion.
Engagement adds no new gate for them — it only catches inbounds that
would otherwise not route at all.

## Mention promotion follows root ownership

An ambient message can gain mention status only in a bot-rooted thread.
Joining a human-rooted thread adds no automatic mention permission.
Explicit mentions, direct bot replies, and reactions still address the bot.
The policy lives in [`L-mention-promotion.md`](L-mention-promotion.md).

## Thread-by-default on Slack

`slakd.Send` threads on the existing thread or a new root
(`threadTS := cmp.Or(req.ThreadID, req.ThreadRoot)`). `ReplyTo` is
**deliberately excluded**: it is often a prior bot row, and rooting a
thread there buries it. Engagement does not modify `Send()` — routd
chains successive turns into the same thread by setting
`ThreadRoot`/`ReplyTo` on conversational outbounds. Net change in
`slakd`: zero.

**Broadcast discriminator is the `timed-` sender prefix**, not
`TurnID != ""` — scheduled outbounds also carry non-empty turn ids.

## MCP tools

`engage(jid, topic)` and `disengage(jid, topic)` (`ipc/ipc.go`), both
wrapping `SetEngagement` (zero time clears). Both args are **required**:
MCP sockets are per-folder, not per-conversation, so there is no
implicit "current" jid to default to.

**Ownership** — either tool requires the caller's live engagement, a route
binding, or the active turn's immutable source-chat binding. A turn can stop
its source conversation before its first answer, even when the default route
points elsewhere. The source binding adds no access to another chat.

Only `engage` can claim a conversation with no current owner. `disengage`
cannot place a persistent stop on a fresh foreign conversation or thread.

`role:member` grants `disengage` and skips approval for it. Each responding
agent can end its own conversation. `engage` still needs an explicit grant.
The ownership check applies to both tools.

## The read surface

`routd/engagement_http.go`, three faces on one pair of routes:

- `GET /v1/engagement?jid=&topic=` — one pair: `folder` + `engaged_until`
  (both empty when the window is not live) + `last_reply_id`. The anchor
  OUTLIVES the window, so an idle pair still reports one.
- `GET /v1/engagement` (no `jid`) — every live window the caller may see,
  newest deadline first. `DB.ListEngaged`.
- `POST /v1/engagement` — engage; `ttl_seconds <= 0` disengages.

**Engagement is hot-tier, so it is NOT a resreg resource.** Root `CLAUDE.md`
requires a resreg registration for every cold-tier MANAGEMENT entity; this is
conversational state, the same carve-out that keeps `engage`/`disengage`
hand-authored in `ipc/ipc.go` (spec [`5/17`](17-openapi-mcp.md) §"The model:
two tiers"). Promoting the table would put a second path into a seam those
tools already own, with a three-arm authorization resreg's `Gate` does not
express. The hand-rolled pair is extended instead.

**Containment: the list-all key is an EMPTY folder claim and nothing else.** A
top-level tenant carries a non-empty folder however shallow, so widening on
depth would hand it every sibling's chats. Per row, the predicate is
`descendant(row.engaged_folder, caller)` — deliberately NOT `ownsFolder`, which
reports true for an empty target and would expose windows no folder has
claimed. Guarded content-level in `TestEngagementList_NoCrossFolderLeak`: the
assertion reads the raw response BODY, so a jid arriving through any field
fails it.

## The operator surface

`/dash/engagement/` (`dashd/engagement_page.go`) lists the live windows —
chat, thread, group, time left — and ends one on demand. Both faces go over
HTTP with the `service:dashd` bearer through one client (`routdCall`; `routdGet`
is its GET wrapper). Neither touches `chat_reply_state` in `dbRoutd`, which
dashd has mounted: routd owns those columns, applies the containment, and writes
the audit row, and the direct-DB access is dashd's own recorded defect class.

**View AND control, both through `/v1`.** `POST /dash/engagement/disengage`
ends one window now — the remedy for a runaway engagement, which otherwise had
none but waiting out the TTL. It posts `ttl_seconds=0` to routd's
`POST /v1/engagement`, behind a confirm like every other dashd danger-zone
action, and never touches `dbRoutd`.

Going through routd is what makes the control safe rather than merely
convenient, and it is the argument that earned `routes:write` on
`serviceGrants["service:dashd"]` (`authd/http.go`) after the read half shipped
without it. The write half owed two things, and both are in routd:

- **The audit row rides the write's transaction.** `DB.SetEngagementAudited`
  opens one tx for the upsert and `audit.EmitInTx`
  (`engagement.set` / `engagement.clear`, category `mutation`). Before this,
  `POST /v1/engagement` wrote NO audit row at all — an operator ending someone's
  conversation appends no message and runs no turn, so nothing recorded it. The
  un-audited `SetEngagement` survives for the per-turn claim sites (dispatch,
  the agent's tools), which are already recorded and would otherwise bury the
  operator trail in per-turn noise.
- **The write contains on the CLAIMING folder.** The request's `folder` is
  caller-supplied, so `ownsFolder(caller, req.Folder)` bounds only what the
  caller ASKS to write, and `ownsJID` resolves the jid's ROUTE TARGET — neither
  looks at who holds the window. A tenant naming its own folder could therefore
  clear a sibling's window that `GET /v1/engagement` would never have shown it.
  `handleEngagementSet` now applies `ownsFolder(caller, live.Folder)`, the same
  predicate `ListEngaged` applies per row, so the write face can never reach
  what the read face hides. Guarded by
  `TestEngagementSet_NoCrossFolderWrite`, which sends the honest-looking
  `folder` — the lying value is the attack, and asserting on an obviously
  cross-folder one passes against the pre-existing check and proves nothing.

Neither scope is a widening of what dashd can reach. dashd is FS-mounted on
routd.db and already reads AND writes routes, groups and route tokens straight
out of the table, so both are strict subsets of the reach it holds; what they
buy is one page moving off the direct-DB path. `routes:read` exposes no secret
either: its widest read, `ListRouteTokens`, selects
jid/owner_folder/created_at/context and never the token value.
`authd/service_dashd_test.go` pins `service:dashd`'s ceiling at exactly nine
scopes by count (routes:read and routes:write among them), so a tenth scope
fails there whether or not anyone thought to blacklist it.

**Operator-only, and that is load-bearing.** dashd's service token carries an
EMPTY folder claim, which is exactly the list-all key above — so the page shows
every tenant's windows. routd authorizes that bearer, not the `X-User-*` headers
dashd forwards (there is no routd equivalent of proxyd's `trustedForwarders`),
so offering this page to a folder-scoped viewer would not narrow it.

## Corrective exchanges fork to a side thread

A correction turn is meta-talk about the bot's last reply ("no that's
wrong", "rephrase that") — plumbing, not content, so it shouldn't
pollute the main thread. Convention only, no enforcement: the agent
calls `fork_topic(current, current+"#fix")`
([`F-topic-lineage.md`](F-topic-lineage.md)), runs the correction there,
posts a clean answer into the parent topic, then `disengage()`s the fix
topic. If the agent skips it, corrections happen inline.

## Config

`ENGAGEMENT_TTL` defaults to `routd.DefaultEngagementTTL` (30m) when unset.
The command resolves that default before calling `NewServer`.
Explicit `0` disables automatic opening and renewal of engagement windows.
It does not change explicit mentions, direct bot replies, or `disengage`.
Existing windows keep their deadlines until they expire or are disengaged.
`TestDefaultEngagementTTL` pins the default and explicit-zero behavior.

## What this is NOT

- **NOT a sentinel-message scheme.** No `verb=disengage` synthetic row;
  engagement is a column read and the queue is unchanged.
- **NOT a `last_reply_at` column.** The column carries the deadline.
- **NOT a per-route override.** Operators wanting always-engaged use a
  bare route target. Engagement is per-conversation-instance.
- **NOT a multi-bot model.** Bots routed to the same folder each track
  their own rows; no coordination.
- **NOT thread creation for Discord/Telegram.** Discord threads need
  explicit API calls; Telegram threads are forum-specific. Slack only.
- **NOT disengage-on-reply.** The bot stays engaged until TTL or an
  explicit `disengage()`.

## Restart-safe

Nothing to recover — the columns live in SQLite (WAL) and every routing
decision re-reads. A crash between `SetLastReply` and `BumpEngagement`
costs at most one un-extended window; the next `verb=mention` re-engages.

## Per-surface output length

Lives in [`Y-output-styles-per-surface.md`](Y-output-styles-per-surface.md),
not here — when the agent fires and how it shapes output are independent.
