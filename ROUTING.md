# Routing

How messages get from a chat to the right agent group, and how replies
get back. A web request routes by path, not by chat:
[`proxyd/README.md` § A request through proxyd](proxyd/README.md#a-request-through-proxyd).

## Platform JID prefixes

Each adapter declares the `platform:` prefixes it owns when it registers
with routd. The post-`:` portion is platform-private; routing predicates
treat it as an opaque string with `path.Match` glob semantics over `/`.

| Adapter | Prefix      | Example                                                             |
| ------- | ----------- | ------------------------------------------------------------------- |
| discd   | `discord:`  | `discord:<guild>/<channel>`, `discord:dm/<channel>`                 |
| slakd   | `slack:`    | `slack:<workspace>/<kind>/<id>` (kind ∈ channel/dm/group)           |
| teled   | `telegram:` | `telegram:user/<id>`, `telegram:group/<id>`                         |
| whapd   | `whatsapp:` | `whatsapp:1234567@s.whatsapp.net`                                   |
| reditd  | `reddit:`   | `reddit:comment/<id>`, `reddit:user/<id>`, `reddit:submission/<id>` |
| bskyd   | `bluesky:`  | `bluesky:user/<did>`                                                |
| linkd   | `linkedin:` | `linkedin:user/<urn>`                                               |
| emaid   | `email:`    | `email:address/foo@bar.com`                                         |
| twitd   | `twitter:`  | `twitter:home`, `twitter:dm/<id>`                                   |
| webd    | `web:`      | `web:<folder>` (e.g. `web:atlas`) — see "Web JID model" below       |

`core.JidPlatform("twitter:dm/abc")` returns `twitter`; `core.JidRoom`
returns `dm/abc`. Use `platform=` predicates to match a whole platform
and `room=` globs to filter by kind/segment.

### Web JID model — 1:1 with groups, no route table

`web:<folder>` addresses group `<folder>` **directly**. The route table
does NOT apply to web JIDs; routd resolves them via
`GroupByFolder` without consulting `routes`. To carve a chat surface
into sub-flows (form intake threads, separate report channels), create
sub-groups (`<parent>/<sub>`) — each gets its own `web:<parent>/<sub>`
JID. `store.AddRoute` rejects `chat_jid=web:*` predicates at insert
time so the mistake fails loudly.

### Route-token surfaces (`/chat/<token>/`, `/hook/<token>`)

Public URLs that feed a chat: `/chat/<token>/` serves the human widget
(GET) and accepts messages (POST); `/hook/<token>` is machine
fire-and-forget POST ingest. Each URL composes three orthogonal axes:

1. **Routing (the token)** — which JID/folder the URL feeds. Opaque
   random secret, sha256 at rest in `route_tokens`, DB-looked-up by
   webd; revoke = delete the row.
2. **Identity (optional JWT overlay)** — who is posting. A logged-in
   caller's real `sub`/name is stamped from the proxyd-verified
   headers; anonymous callers get `anon:<ip-hash>` / "Anonymous". Same
   link either way (`webd/route_token.go`).
3. **Context (optional per-link instructions)** — how to process this
   link's inbound. Set at mint (`issue_chat_link(context=…)` /
   `issue_webhook(context=…)`, REST, dashd, `arizuko token issue
--context`), snapshotted onto each message at ingest
   (`messages.link_context`), rendered to the agent as
   `<link-context>` in the turn envelope.

### Token kinds — delivery vs pairing (`/pair/<token>`)

`route_tokens.kind` says what a row IS. `route` is a delivery bearer —
the two surfaces above. `pair` is an **identity-pairing link** served by
webd at `/pair/<token>`: redeeming it writes the `acl_membership` edge
that makes a channel identity (`telegram:user/123`) resolve to the
account of whoever confirms, which is the only way an anonymous channel
user acquires any authority at all.

Resolution is kind-scoped in both directions — delivery accepts only
`route`, redemption only `pair` — so neither is redeemable as the other.
A pairing is minted by the agent in the chat (`issue_pairing_link`, MCP
only; the JID must route to the agent's folder), lives ten minutes, is
single-use, and grants nothing by itself. `GET /pair/<token>` is
side-effect-free so an unfurl bot cannot spend it; the `POST` is the
consent step, and the threat model that shapes it is SECURITY.md
§"Pairing: consent is the boundary". The inverse verb is `unpair`: it
deletes one `acl_membership` edge scoped to `added_by='pairing'`, and either
end of the edge may call it. An unredeemed pairing link cannot be listed or
revoked: `list_tokens` and `revoke_token` read `kind='route'` rows only, so
the link lives until it is redeemed or its ten minutes pass.

## Route Table

`routes` is a flat list of rules evaluated against every inbound message.
Each rule is addressed by `(seq, target, match)`, with `seq` padded so a read
comes back in precedence order:

| Field    | Type | Purpose                                                         |
| -------- | ---- | --------------------------------------------------------------- |
| `id`     | int  | The handle `route rm` takes; minted through the `counters` item |
| `seq`    | int  | Evaluation order (lower = earlier)                              |
| `match`  | text | Space-separated `key=glob` predicates (AND)                     |
| `target` | text | `<folder>[#<mode>]` — group folder + optional mode              |

There is no `jid` or `type` field. Rules are filtered entirely by the
`match` expression. Rules are evaluated in `seq` order; first rule whose
predicates all match wins.

Registering a group writes exactly one rule: `seq` 0, `match`
`room=<the JID after the colon>`, `target` the new folder
(`registerGroup`, `routd/mcp.go`). onbod's route write has the same shape.

```bash
# All telegram chats default to atlas/content
arizuko route krons add 'platform=telegram' atlas/content --seq 0

# A specific telegram chat overrides
arizuko route krons add 'chat_jid=telegram:group/12345' atlas/legal --seq -10

# All Discord chats fall back to atlas/social
arizuko route krons add 'platform=discord' atlas/social --seq 9999
```

### Match expression

`router.RouteMatches` parses `match` as a whitespace-separated list of
`key=glob` predicates, AND'd together. An empty `match` matches every
message.

| Key        | Source                                 |
| ---------- | -------------------------------------- |
| `platform` | `core.JidPlatform(msg.ChatJID)`        |
| `room`     | `core.JidRoom(msg.ChatJID)` (post-`:`) |
| `chat_jid` | `msg.ChatJID` (full JID)               |
| `sender`   | `msg.Sender`                           |
| `verb`     | `msg.Verb`; the next table             |

Glob semantics use `path.Match` over `/`-separated segments:

| Pattern      | Meaning                                       |
| ------------ | --------------------------------------------- |
| `key=exact`  | value equals `exact`                          |
| `key=<glob>` | value matches glob (`*` `?` `[…]`, `*` ≠ `/`) |
| `key=*`      | value is present (non-empty)                  |
| `key=`       | value is absent (empty)                       |
| omit key     | unconstrained — no filter on this field       |

`verb` has no closed vocabulary. Each adapter emits its own set, and routd
rewrites a few at ingest:

| Source              | Verbs it emits                                                     |
| ------------------- | ------------------------------------------------------------------ |
| teled, discd, slakd | `mention` on an explicit `@bot`, else no verb, stored as `message` |
| bskyd               | `message`, `reply`                                                 |
| reditd              | `message`, `reply`, `post`                                         |
| linkd               | `comment`, `reply`                                                 |
| twitd               | `message`, `reply`                                                 |
| a reaction          | `like` or `dislike`, from `chanlib.ClassifyEmoji`                  |
| emaid               | `untrusted` on mail that fails DMARC and the allowlist             |
| routd, at ingest    | `mention` (§ Verb promotion); an empty verb becomes `message`      |

The four social adapters, bskyd, reditd, linkd and twitd, never emit
`mention`. On those platforms a rule keyed on `verb=mention` or `verb=like`
for a stranger's first post matches nothing, and nothing reports it.

```bash
# Reddit submissions only, any sub
arizuko route krons add 'platform=reddit verb=post' atlas/posts

# All twitter DMs (twitter:dm/<conv>) but not timeline/tweets
arizuko route krons add 'platform=twitter room=dm/*' atlas/dm
```

### Target expansion

Targets can contain `{sender}`, which expands to a sanitized sender ID.
This creates per-user child groups:

```bash
# Each Discord user gets their own group under atlas/
arizuko route krons add 'platform=discord' 'atlas/{sender}'
# discord:user/alice → atlas/dc-alice, discord:user/bob → atlas/dc-bob
```

### Modes

The `target` column is a URI: `<folder>` plus an optional `#<mode>`
fragment. The fragment selects the ingestion mode for matched messages.
A target with no fragment means **trigger** — the default and the only
mode that fires a turn.

| Fragment    | Effect                                                                           |
| ----------- | -------------------------------------------------------------------------------- |
| _(none)_    | trigger: store under `target` and fire a turn                                    |
| `#observe`  | store under `target`, no turn; agent sees it via `<observed>` block and history  |
| `#announce` | trigger, and marks this channel as the group's release-note target for `migrate` |
| `#<topic>`  | trigger, and pins topic `<topic>` on every matched message                       |

`observe` and `announce` are **reserved words**, so a topic cannot be named
either one: `folder#announce` always means the announce fragment, never a topic
called `announce`. A route row carries no creation time, so nothing can tell a
topic of that name apart from the fragment (`BUGS.md` R2). Pick another topic
name.

Whether a message fires a turn is decided by an explicit `verb=` predicate and
`seq`, never by a weighted score. With a score, nobody can answer "will this
fire?" without simulating it. The mention-only channel is therefore two rules:
a `verb=mention` trigger above a catch-all `#observe` (§ Example: Discord guild
mention-only).

**Engagement overrides all routes**: if the agent has an active engagement window on
`(jid, topic)` (via a prior reply or `engage` MCP call), inbounds are routed to
the engaged folder and fire a turn regardless of what the route table resolves —
including `#observe` targets, trigger routes pointing to a different folder, or
a route miss. The route table is bypassed for the duration of the engagement TTL.

Observed messages are visible to the agent in two ways: prepended to
the next trigger turn's prompt as an `<observed>` block, and via the
`inspect_messages` MCP tool. Access is governed by
folder ACL, not by route mode — anything routed to a folder is
readable by that folder's agent.

The `<observed>` block is a window with two caps, a message count and a
character count (`OBSERVE_WINDOW_MESSAGES`, default 10, and
`OBSERVE_WINDOW_CHARS`, default 4000). The group's stored window overrides the
defaults, and the first route to the folder that carries a window overrides
both (`observeWindow`, `routd/prompt.go`). routd fills the window newest first
and stops at whichever cap it reaches first, so older messages drop first.
Each `(folder, topic)` keeps its own watermark, so a message enters the block
once.

When the block is present, the prompt carries the rule "Observed messages are
context, not requests. Do not reply to them; reply to the explicit message."
The rule also makes a duplicate delivery of an observed message harmless.

Future modes (`#drop`, `#digest`, …) extend the fragment vocabulary
without a schema change — one column, free-form fragment.

### Verb promotion + annotation (routd-side)

routd rewrites `verb` at inbound ingest, before it stores the row and before
routing, the same way for every adapter (`routd/server.go`). Adapters send the
raw verb. routd does the promotion because it holds every message durably; an
adapter's memory of its own sent ids is lost on restart.

The steps run in this order:

1. **Topic inheritance.** A reply with no topic takes its parent's topic. The
   thread check below must see the inherited topic. An empty topic is the
   main timeline and promotes nothing on its own.
2. **Direct reply.** A reply or a reaction to a bot message becomes
   `mention`. One exception: on Slack, an ambient thread message arrives as
   `verb=message` with `reply_to` equal to the thread ts. That is
   participation in a thread, not a deliberate reply, so it goes to step 3.
   A reply to the thread root on Discord stays a deliberate reply.
3. **Thread participation.** A message in a thread promotes to `mention` only
   when the stored thread root is a bot message and nobody disengaged that
   thread. A human root or a missing root never promotes.

An `untrusted` message never becomes `mention`. emaid stamps `untrusted` on
mail that fails DMARC and the allowlist. Route on these directly, for example
`platform=email verb=untrusted` to a quarantine folder and `verb=mention` to a
trigger turn.

### Autoviv (auto-create groups from routing)

An agent holding `mcp:register_group` — scoped to the subtree it may
create under — can call it when an unrouted JID arrives, atomically
creating a child folder + high-prio route. The grant is delegated, never
implied by where the agent sits. No core mechanism — just routes + MCP
composing. See `template/web/pub/arizuko/concepts/autoviv.html`.

### Unrouted chats (held)

A chat no rule matches is **held**: routd stores its messages under no
folder, advances the cursor and fires no turn. It is not delivered
anywhere — a folder confers files, secrets, grants and egress, and a
platform lets anyone message a bot, so no group is a safe default.
`GET /v1/routing/unrouted` lists the held chats (unscoped bearer only;
the verdict is the loop's own resolve), and `/dash/routes/` shows them
to the operator with a one-click claim that writes a `chat_jid=` rule at
seq 0. A `platform=<name>` rule claims a whole platform. History does
not move: only messages after the claim reach the group. With
`ONBOARDING_ENABLED`, routd also hands a held chat to onbod, which greets it
with a pairing link; the flow is drawn in
[`onbod/README.md`](onbod/README.md#from-an-unrouted-chat-to-a-routed-one).

There is no default catch-all, and `arizuko create` seeds none:

- A trigger catch-all would let anyone who finds the bot drive the
  operator's agent, with its files, secrets and egress.
- An observe catch-all fires no turn, but it puts strangers' text into the
  target folder's `<observed>` window. It also ends onboarding on that
  platform: `routeMiss` takes the observe branch first, and only the other
  branch hands the chat to onbod (`routd/loop.go`). A
  `platform=X → staging#observe` rule therefore means "capture, never greet"
  for platform X; greeting and capturing exclude each other per platform.

Who may make the agent fire is a routing question, answered in the same
table. A `sender=` rule above an observe rule for the same room fires for
that sender, and everyone else matches only the observe row. Engagement
overrides per chat and topic. Firing never consults the ACL, so a second
permission check in the loop has no place.

A held chat looks identical to a broken one from the outside: nobody
answers either. `GET /v1/routing/answering` separates them. It lists every
chat whose newest human message is newer than its newest agent reply and
tags each with the loop's own verdict — `route` means a turn was due and
none answered, `observe` and `held` mean the chat is silent by the
configuration above. It also counts the window's turn outcomes, so
`expired` turns are readable without a hand-written query. The operator
sees both on `/dash/status/`.

### Example: Discord guild mention-only

A guild where the agent reads everything but only replies on mention:

```
seq  match                                       target
10   platform=discord room=guild/sloth           main
20   platform=discord room=guild/* verb=mention  main
30   platform=discord room=guild/*               main#observe
```

The verb-mention rule (seq 20) fires turns on `@bot` messages in any
guild; the catchall (seq 30) silently archives the rest under `main`
in observe mode. The named-guild rule (seq 10) opts `sloth` out of the
mention gate and treats every message as a trigger.

### Example: Slack workspace mention-only

The same pattern on Slack — agent reads all channels but only replies
when directly @-mentioned:

```
seq  match                                             target
40   chat_jid=slack:*/channel/* verb=mention           atlas
40   chat_jid=slack:*/group/*   verb=mention           atlas
100  chat_jid=slack:*/channel/*                        atlas#observe
100  chat_jid=slack:*/group/*                          atlas#observe
```

A message in any Slack channel or private group without `@atlas` hits
seq 100 and is stored as observed context — no turn fires, atlas stays
silent. An `@atlas` mention promotes `verb=mention` (routd-side verb
promotion), which matches seq 40 first and fires a turn.

DMs (`slack:*/dm/*`) are routed separately at a lower seq number with
a plain trigger target, so the agent always replies in DMs regardless
of mention.

This is correct, intentional behavior. To make the agent reply to
every channel message, change the seq-100 targets from `atlas#observe`
to `atlas`.

### Inline `@name` / `#topic` prefix layer

The `@` and `#` prefix layer is **not** in the routes table; it lives
in `routd.handlePrefixLayer`. An anchored regex
(`^\s*@(\w[\w-]*)` / `^\s*#(\w[\w-]*)`) matches a sigil at the very
start of `msg.Content`; mid-content `@handle` or `#hashtag` (e.g.
forwarded tweets) do not trigger.

- **`@name`** at start of message: routd looks up
  `<current-group>/<name>` as a child group. If found, delegates the
  message (with `@name` stripped) to that child group's agent.

  ```
  User in group atlas sends: "@content write a blog post about cats"
  parsePrefix → name="content", rest="write a blog post about cats"
  Child lookup: atlas/content exists
  Delegation: "write about cats" → atlas/content agent
  ```

- **`#topic`** at start of message: routd runs the message (with
  `#topic` stripped) in a topic-scoped session within the current group.

  ```
  User in atlas sends: "#support my account is locked"
  Agent runs: "my account is locked" in session (atlas, #support)
  ```

If a referenced child group doesn't exist, the prefix is left in place
and the message falls through to the agent unchanged.

The prefix layer is code, not `routes` rows, because `@name` is relative to
the folder that owns the chat. A row would repeat per group and need a rewrite
on every folder move.

The prefix layer reads one row per poll batch: the oldest slash command when
the batch holds one, else the newest user message (`steerRowIdx`,
`routd/dispatch.go`). An `@name` or `#topic` on an older row of the same batch
is not applied.

## Resolution Order

routd resolves a chat's newest user row in two stages (`routd/loop.go`
`resolveFolder`, then `routd/steer.go` `steer`). The first stage picks the
folder. The second stage runs inside that folder and may consume the message
before any turn.

```
inbound row
   │
   ▼
1. direct address: a web:/hook: JID or a bare folder ──────────────┐
   │ none                                                          │
   ▼                                                               │
2. route table, the first match by seq                             │
   │ a trigger rule, or any rule on an engaged chat ► a folder ────┤
   │ an observe rule ► stored, no turn                             │
   │ no match                                                      │
   ▼                                                               │
3. engagement on (jid, topic), else on (jid, "") ──────────────────┤
   │ none                                                          ▼
   ▼                                         steer, in that folder: sticky
route miss: held; onboarding if enabled      @ or #, slash command, prefix,
                                             delegation
                                                   │ not consumed
                                                   ▼
                                                runTurn
```

1. **Direct address.** A `web:` or `hook:` JID names its folder in the token
   record, and a JID with no `:` names a registered folder. The route table
   does not apply, and agent-authored rows never get content routing. A
   prefix that only meant "not a platform" would be noise.
2. **Route table**, in ascending `seq`; the first match wins (§ Match
   expression). An engaged chat overrides a matched rule's folder. A matched
   `observe` rule on an engaged chat counts as engagement alone.
3. **Engagement** rescues a miss. The lookup uses the effective topic, then
   falls back to the root topic `""` unless that thread was disengaged, so a
   reply inside a thread keeps the window its root started. Engagement is
   claimed at dispatch, never at ingress, because the folder is unknown until
   this step. Resolution runs before the miss handler, so an engaged chat
   never reaches onboarding. The engaged folder's grants are not checked again
   on this path: the folder held them when it engaged.

A route-table read failure is logged at ERROR and treated as a miss: a retry
would loop on a poison message.

The second stage, in order: a bare `@group` or `#topic` sets sticky
navigation; a slash command runs; an `@child` or `#topic` prefix delegates or
re-posts the message under that topic; then a delegation target is looked up,
in this order: the folder a replied-to bot row was routed to, the chat's
sticky group, the route table. A target is used only when it differs from the
current folder and passes the authorization check below. Each consumed row
advances the cursor and starts no turn. What `runTurn` does, through to the
adapter's send: [ARCHITECTURE.md § Message Flow](ARCHITECTURE.md#message-flow).

### Authorization check

`router.IsAuthorizedRoutingTarget(source, target)` enforces:

- `root` group can route to anything
- Otherwise, target must share the same world (first path segment)
  and be a direct child of source (one level deeper, no deeper)

```
source="atlas"       target="atlas/content"    → allowed (direct child)
source="atlas"       target="atlas/content/x"  → denied (grandchild)
source="atlas"       target="other/content"    → denied (different world)
source="root"        target="atlas/content/x"  → allowed (root)
```

## Topic Routing

Topics provide thread isolation within a single group. Each topic gets
its own Claude session, reply chain, and message history.

<!-- twin:topics -->

```
┌──────────────┐    ┌──────────────────────────────────────────────────┐
│ chat message ├───►│ (folder, topic): the platform thread, a route    │
└──────────────┘    │ pin, a sticky #topic, or the web chat's slug     │
                    └─────────────────────────┬────────────────────────┘
                                              ▼
                    ┌──────────────────────────────────────────────────┐
                    │ first turn in a topic: EnsureTopicLineage writes │
                    │ the sessions row: parent_topic, forked_at, the   │
                    │ parent's observed_cursor                         │
                    │ copyParentSession: the parent's jsonl under a    │
                    │ new id; no parent file ► the cursor is cleared   │
                    └─────────────────────────┬────────────────────────┘
                                              ▼
                    ┌──────────────────────────────────────────────────┐
                    │ idle past SESSION_IDLE_EXPIRY ► routd drops it   │
                    │ no id ► routd mints one and runs PutSession      │
                    │ BEFORE it calls runed                            │
                    └─────────────────────────┬────────────────────────┘
                                              │ RunRequest.SessionID
                                              ▼
                    ┌──────────────────────────────────────────────────┐
                    │ guest: the harness resumes                       │
                    │ ~/.claude/projects/-home-node/<id>.jsonl         │
                    └─────────────────────────┬────────────────────────┘
                                              │ submit_turn
                                              ▼
                    ┌──────────────────────────────────────────────────┐
                    │ recordTurnResult: PutSession with the id the     │
                    │ harness ran under; a killed turn reports none,   │
                    │ and the next turn resumes the id routd minted    │
                    └──────────────────────────────────────────────────┘
```

<!-- /twin -->

- **The session id is declared before the run.** routd mints the topic's id
  when it has none and persists it in `sessions` before it calls runed
  (`routd/dispatch.go` `dispatchRun`). Claude opens its session under that id,
  so a turn killed before `submit_turn` by a restart, a deadline or a crash is
  resumed by the next turn in the topic (`routd/session_continuity_test.go`).
  Codex and pi start fresh under that id and report their own, which
  `submit_turn` then stores (BUGS `L432`).
- **An idle session expires.** When the chat's cursor is older than
  `SESSION_IDLE_EXPIRY`, two days by default, routd drops the topic's session
  id before the spawn. A resume after days makes the agent blend stale state
  into the current turn.
- **An isolated task has no session.** A `timed-isolated:` turn runs with no
  resume and never writes its id back: a cron's tool-heavy run appended to the
  chat's transcript would push the humans out at compaction.

### How topics are set

1. **Platform-native threads**: channel adapters map native thread IDs
   to `Message.Topic`. Telegram's `MessageThreadID`, Discord's thread
   channel ID, etc. Outbound is symmetric: the agent's `reply` lands in a
   thread off the message it answers — Discord starts one if none exists,
   Slack roots `thread_ts` on the trigger — so the agent's own reply seeds
   the topic that subsequent in-thread messages inherit. `send` stays
   top-level. Per-group `thread_replies` toggles it (default on for group
   chats, off in DMs).

2. **Web channel**: web messages carry a topic slug directly. The JID
   format is `web:<folder>`, and topics arrive pre-set.

3. **`#topic` prefix routing**: when a message matches a `#` prefix
   route, routd runs the agent with topic `#name`.

4. **Sticky topic**: the `#topic` command sets a persistent topic
   for the chat (see Sticky Routing below).

### How topics affect processing

- **Session isolation**: `store.GetSession(folder, topic)` — each
  `(folder, topic)` pair has its own session ID. Resetting one topic's
  session leaves others untouched. `/new #support` resets only the
  `#support` session.

- **Reply chain isolation**: `store.LastReplyID(jid, topic)` — each
  topic tracks its own last-sent message ID for reply threading.

- **Web topic batching**: `groupByTopic` (on `consecutiveRuns`,
  `routd/dispatch.go:616`) splits web messages into consecutive same-topic
  runs and dispatches one turn per run, serially.

- **Lineage**: every non-main topic carries `parent_topic`, `forked_at`
  and `observed_cursor` in `sessions`. A new topic's parent is the topic of
  the message it replies to, else main `""`. The first turn's fork takes the
  parent's observed cursor; an explicit `fork_topic` takes now. Both copy the
  parent's session jsonl to the child's id (`container.CopySession`), so the
  child resumes natively from the parent's tail, with no `<inherited>`
  block. Every turn carries a `<topic name=…/>` envelope. The observed
  cursor is per topic, so two topics in one folder both see `<observed>`
  context without one consuming it for the other.

- **Two ways to see another folder**: `observe_group` subscribes this folder
  to a source folder's inbound through `group_watchers`, a one-way, read-only
  feed. `set_group_open` makes a folder's already-observed rows visible to its
  siblings. They answer two questions, so they are two tools. The single
  evaluator authorizes each one on its target folder.

### Effective topic resolution

```go
func effectiveTopic(chatJid, msgTopic string) string {
    stickyTopic := store.GetStickyTopic(chatJid)
    if stickyTopic != "" {
        return stickyTopic
    }
    return msgTopic
}
```

Sticky topic overrides the message's native topic. Clear with `#`.

## Reply Routing

When a user replies to a bot message, the reply should go to the same
group that produced the original response — even if the default route
points elsewhere.

### How routed_to works

Every outbound bot message records which group produced it:

```go
// Every agent reply goes through the same PutMessage path:
store.PutMessage(core.Message{
    ChatJID:   chatJid,
    Sender:    groupFolder,
    RoutedTo:  groupFolder,
    Topic:     topic,
    FromMe:    true,
    BotMsg:    true,
    ...
})
```

The `messages` table has a `routed_to` column. When a user replies to a
message (platform provides `reply_to_id`), routd looks it up:

```go
func resolveTarget(msg, routes, selfFolder) string {
    // 1. Reply chain: follow the reply_to_id
    if msg.ReplyToID != "" {
        routedTo := store.RoutedToByMessageID(msg.ReplyToID)
        if routedTo != "" && routedTo != selfFolder {
            return routedTo
        }
    }
    // 2. Sticky group
    // 3. Route table
    // ...
}
```

`forwarded_from` on a row is a return address, never a channel-forward
marker. `delegate` and `escalate` write a row into the target folder's queue
with `forwarded_from` set to the chat that asked, or to the escalation
worker's JID (`delegateViaMessage`, `routd/steer.go`). The turn that row
starts delivers its `reply` and `send` there.

The adapter that carries a reply is chosen in this order
(`resolveEntry`, `routd/deliver.go`): an explicit channel pin, which only
`POST /v1/outbound` carries; then the adapter that delivered the chat's latest
inbound, `messages.source`; then the first registered adapter that owns the
JID's prefix. The middle step matters when two accounts on one platform share a
prefix: the prefix lookup returns whichever it finds first.

### Example: reply routing in action

```
Routes: match='platform=telegram' → atlas
Groups: atlas, atlas/content, atlas/social

1. User sends: "@content write about cats"
   → prefix layer matches, delegates to atlas/content
   → atlas/content replies: "Here's a post about cats..." (routed_to=atlas/content)

2. User replies to that message: "make it shorter"
   → reply_to_id points to the bot message
   → store.RoutedToByMessageID → "atlas/content"
   → message routes to atlas/content again (not atlas)

3. User sends a new message (no reply): "hello"
   → no reply chain, no sticky
   → route 'platform=telegram' → atlas
```

### Reply chain for outbound threading

The routd tracks the last-sent message ID per `(jid, topic)` to build
reply chains on the platform side:

```
User: "help"                   (msg_id=100)
Bot:  "Sure, what's up?"       (reply_to=100, sent_id=201)
Bot:  "I can help with..."     (reply_to=201, sent_id=202)
```

`store.LastReplyID(jid, topic)` provides the anchor for the next
reply. Updated in three cases:

1. **Bot sends a chunk** — `SetLastReply` to the bot's sent message ID
2. **Steering message** — when a follow-up is steered into a live turn,
   routd sets `lastReplyID` to the steering message's ID
3. **New agent run** — `SetLastReply` seeds the batch's last user message
   (`routd/dispatch.go:364`), then `GetLastReplyID` is re-read before each
   chunk to pick up mid-run steering updates

Topics forked via `fork_topic` inherit the parent's last-reply context as
their starting anchor but advance the child's `lastReplyID` independently.

## Sticky Routing

Sticky commands let users lock a chat to a specific group or topic
without prefixing every message.

### Commands

A message is a sticky command when its **entire content** (trimmed) is
one of these — no additional text:

| Input            | Effect                                | Confirmation               |
| ---------------- | ------------------------------------- | -------------------------- |
| `@atlas/content` | Lock chat to `atlas/content` group    | `routing -> atlas/content` |
| `@`              | Clear sticky group, return to default | `routing reset to default` |
| `#support`       | Lock chat topic to `support`          | `topic -> support`         |
| `#`              | Clear sticky topic                    | `topic reset to default`   |

`@<unknown>` (name doesn't match any group folder) is **not** consumed —
routd passes the message through to the agent unchanged, no
confirmation, no error. Rationale: messages starting with `@` have too
many other meanings (mentions like `@everyone`, cross-instance refs like
`@sloth`, Czech/English sentences) for an unknown name to safely imply
"set sticky". The agent decides: typo, mention, or `delegate_group` to
a child.

Sticky state is stored per-chat in the `chats` table (`sticky_group`,
`sticky_topic` columns). All users in the same chat share one sticky
state.

### Interaction with other routing

Sticky group is checked in `resolveTarget` after reply-chain routing
but before the route table. This means:

- Replying to a specific bot message still routes to the group that
  produced it (reply chain takes precedence)
- Sticky overrides the default route table
- Inline `@name` in a message with content (e.g. `@content do this`)
  still fires prefix routing for that one message — sticky persists

Bot messages and scheduler-injected messages ignore sticky state.

### State model

The chat's own item carries it: `chats.sticky_group` (a group folder) and
`chats.sticky_topic`. Absent means no sticky.

## Multi-Group Chat

A single chat JID can route messages to different groups based on
content, replies, and sticky state.

```
Routes table:
  seq=0  match='platform=telegram'  target=atlas    # catchall

Prefix layer is built-in; no table rows needed.

Possible message flows in the same chat:
  "hello"                → atlas (route)
  "@content draft post"  → atlas/content (prefix @)
  [reply to content msg] → atlas/content (reply chain)
  "#support help me"     → atlas, session=#support (prefix #)
  "@atlas/social"        → sticky set to atlas/social
  "what's new"           → atlas/social (sticky override)
  "@"                    → sticky cleared, back to atlas
```

Each group runs its own agent with its own session. Reply chains and
topic sessions keep conversations isolated even within the same chat.
