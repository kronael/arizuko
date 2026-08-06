---
status: shipped
depends: [B-route-mode-ingestion, F-topic-lineage, L-mention-promotion]
relates-to: [3/Y-thread-routing, 5/Y-output-styles-per-surface]
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

## Write sites — one policy, two calls

`store/messages.go:504` `SetLastReply` and `:525` `BumpEngagement` are
deliberately split:

- `SetLastReply(jid, topic, replyID, folder)` — **always** on
  conversational outbound. Writes the thread anchor + `engaged_folder`.
- `BumpEngagement(jid, topic, folder, until)` — **only** when the active
  turn's trigger sender is not `timed-*`. Writes `engaged_until` +
  `engaged_folder`.

Audit invariant: every `BumpEngagement` call site is guarded by
`!strings.HasPrefix(triggerSender, "timed-")` and by a non-empty
platform reply id (`ipc/ipc.go:630`) — a scheduled broadcast or a failed
send must not open an engagement window the user didn't ask for.

Why not bump inside `MarkMessageDelivered`: its signature is
`(id, platformID)` with no jid/topic in scope, and it is called
unconditionally on the empty-text early-exit — that would falsely engage
a never-delivered row.

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
every threaded follow-up misses its own engagement.

**Precedence over onboarding.** The route-miss branch also fires
onboarding. Engagement is checked first — engaged means skip onboarding
and the cursor drop. Grants are not re-validated on the engagement path;
the lifecycle invariant is "engaged ⇒ formerly granted". Operators revoke
with `disengage()` or wait out the TTL.

Mention and reply-to-bot already route via the route table plus
[`L-mention-promotion.md`](L-mention-promotion.md) verb promotion.
Engagement adds no new gate for them — it only catches inbounds that
would otherwise not route at all.

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

**Three-arm authorization** — an agent may engage only its own
conversations. Accept if any holds:

1. `EngagedFolder(jid, topic) == callerFolder` — already owns it.
2. `JIDRoutedToFolder(jid, callerFolder)` — is the jid's route target.
3. `EngagedFolder(jid, topic) == ""` — fresh chat. Escape hatch so an
   autonomous turn can bootstrap a conversation with no pre-existing
   route. Stealing an _active_ engagement still needs arm 1 or 2.

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
`authd/service_dashd_test.go` pins the ceiling at exactly these three by count,
so a fourth scope fails there whether or not anyone thought to blacklist it.

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

`ENGAGEMENT_TTL`, defaulting to `routd.DefaultEngagementTTL` (30m). One
constant, read by `cmd/routd`'s env fallback and re-applied by `NewServer` on a
zero, because it was previously spelled in three places — including a
`core.Config` field nothing read, whose 20m was the number three doc pages
quoted (BUGS `F12`). That field is deleted; `TestDefaultEngagementTTL` pins the
survivor as a written-out literal so a change here forces the docs to be
revisited.

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
