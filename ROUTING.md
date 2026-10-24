# Routing

This file explains how a message goes from a chat to the right agent group,
and how the reply goes back. A web request routes by path and not by chat.
[`proxyd/README.md`](proxyd/README.md#a-request-through-proxyd) describes it.

## Each adapter owns the JID prefixes it registers

Each adapter declares the `platform:` prefixes that it owns when it registers
with `routd`. The part of a JID after the first `:` belongs to the platform.
Routing predicates treat it as an opaque string and match it with `path.Match`
globs over `/`.

| Adapter | Prefix      | Chat JID                                                             |
| ------- | ----------- | -------------------------------------------------------------------- |
| discd   | `discord:`  | `discord:<guild_id>/<channel_id>`, `discord:dm/<channel_id>`         |
| slakd   | `slack:`    | `slack:<workspace>/<kind>/<id>` with kind `channel`, `dm` or `group` |
| teled   | `telegram:` | `telegram:user/<id>`, `telegram:group/<id>`                          |
| whapd   | `whatsapp:` | `whatsapp:1234567@s.whatsapp.net`                                    |
| emaid   | `email:`    | `email:thread/<id>`                                                  |
| webd    | `web:`      | `web:<folder>`, for example `web:atlas`; see the next section        |

A Telegram group has a negative chat id. The JID drops the minus sign, so group
`-100123` becomes `telegram:group/100123`. A Discord guild channel always has
both parts, `<guild_id>/<channel_id>`. The sender of an email is
`email:address/<address>`, and the sender of a Slack message is
`slack:user/<id>`.

`core.JidPlatform("discord:dm/abc")` returns `discord`, and `core.JidRoom`
returns `dm/abc`. Use `platform=` predicates to match a whole platform. Use
`room=` globs to match the kind or a segment.

### A web JID names a folder and skips the route table

`web:<folder>` addresses the group `<folder>` directly. The route table does
not apply to web JIDs. `routd` resolves them with `GroupByFolder` and does not
read `routes`. To split a chat surface into flows, such as intake threads or
separate report channels, create sub-groups named `<parent>/<sub>`. Each
sub-group gets its own JID `web:<parent>/<sub>`. `store.AddRoute` rejects a
predicate that begins with `web:` when you insert the rule, so the mistake
fails at once.

### A route token feeds a chat from a public URL

`/chat/<token>/` and `/hook/<token>` are public URLs that feed a chat.
`/chat/<token>/` serves the widget for people on GET and accepts messages on
POST. `/hook/<token>` accepts a POST from a machine and returns at once. Each
URL combines three independent parts:

1. **Routing, set by the token.** The token decides which JID and folder the
   URL feeds. It is a random secret that `webd` looks up in the database. The
   database stores only its sha256 hash in `route_tokens`. A revoke deletes
   the row.
2. **Identity, set by an optional JWT.** The identity says who posts. When the
   caller is logged in, the sender is the real `sub` and name from the headers
   that `proxyd` verified. An anonymous caller gets `anon:<ip-hash>` and the
   name "Anonymous". The link is the same in both cases
   , see `webd/route_token.go`.
3. **Context, set by optional instructions.** The context says how to process
   the messages that arrive through this link. The issuer sets it when it
   mints the token, with `issue_chat_link(context=…)`,
   `issue_webhook(context=…)`, REST, dashd or `arizuko token <instance> issue
… --context`. The ingest copies it to each message in
   `messages.link_context`, and the turn envelope shows it to the agent as
   `<link-context>`.

### A route token is either a delivery token or a pairing token

The column `route_tokens.kind` says what a row is. Kind `route` is a delivery
token, the two URLs above. Kind `pair` is an identity-pairing link that `webd`
serves at `/pair/<token>`. Redeeming it writes the `acl_membership` edge that
makes a channel identity, such as `telegram:user/123`, resolve to the account
of the person who confirms. This is the only way that an anonymous channel user
gets any authority.

Each lookup checks the kind. Delivery accepts only `route`, and redemption
accepts only `pair`, so neither token works as the other. The agent mints a
pairing link in the chat with `issue_pairing_link`, which exists only as an MCP
tool. The JID must route to the folder of the agent. The link lives ten
minutes, works once and grants nothing by itself. `GET /pair/<token>` has no
side effects, so an unfurl bot cannot spend the link. The `POST` is the consent
step. [SECURITY.md](SECURITY.md#pairing-consent-is-the-boundary) explains the
threat model behind it.

The inverse tool is `unpair`. It deletes one `acl_membership` edge with
`added_by='pairing'`, and either end of the edge can call it. Nobody can list
or revoke a pairing link that is not yet redeemed. `list_tokens` and
`revoke_token` read only rows with `kind='route'`, so the link lives until
someone redeems it or its ten minutes pass.

## The route table sends a message to the first matching rule

`routes` is a flat list of rules. `routd` checks every inbound message against
it. Each rule has the address `(seq, target, match)`. The store pads `seq`, so
a read returns the rules in order of precedence.

| Field                     | Type | Purpose                                                              |
| ------------------------- | ---- | -------------------------------------------------------------------- |
| `id`                      | int  | The handle that `route rm` takes; minted through the `counters` item |
| `seq`                     | int  | Evaluation order; a lower value goes first                           |
| `match`                   | text | Space-separated `key=glob` predicates, all of which must match       |
| `target`                  | text | `<folder>[#<mode>]`: a group folder and an optional mode             |
| `observe_window_messages` | int  | Optional. Overrides the message cap of the observed window           |
| `observe_window_chars`    | int  | Optional. Overrides the character cap of the observed window         |

A rule has no `jid` or `type` field. The `match` expression is the only
filter. `routd` evaluates the rules in `seq` order, and the first rule whose
predicates all match wins.

Registering a group writes exactly one rule. The rule has `seq` 0, `match`
`room=<the JID after the colon>` and the new folder as `target`, see `registerGroup` in `routd/mcp.go`. The route write of `onbod` has the same
shape.

```bash
# All Telegram chats go to atlas/content by default
arizuko route krons add 'platform=telegram' atlas/content --seq 0

# One Telegram chat overrides the default
arizuko route krons add 'chat_jid=telegram:group/12345' atlas/legal --seq -10

# All Discord chats fall back to atlas/social
arizuko route krons add 'platform=discord' atlas/social --seq 9999
```

### A match expression is a list of key=glob predicates

`router.RouteMatches` reads `match` as a list of `key=glob` predicates
separated by white space. Every predicate must match. An empty `match` matches
every message.

| Key        | Source                                                    |
| ---------- | --------------------------------------------------------- |
| `platform` | `core.JidPlatform(msg.ChatJID)`                           |
| `room`     | `core.JidRoom(msg.ChatJID)`, the part after the first `:` |
| `chat_jid` | `msg.ChatJID`, the full JID                               |
| `sender`   | `msg.Sender`                                              |
| `verb`     | `msg.Verb`; see the verb table below                      |

A glob uses `path.Match` over segments that `/` separates. `*` never matches
`/`.

| Pattern      | Meaning                                         |
| ------------ | ----------------------------------------------- |
| `key=exact`  | the value equals `exact`                        |
| `key=<glob>` | the value matches the glob (`*`, `?` and `[…]`) |
| `key=*`      | the value is not empty and contains no `/`      |
| `key=`       | the value is empty                              |
| no key       | no filter on this field                         |

The `verb` field has no closed list of values. Each adapter emits its own
verbs, and `routd` rewrites some of them at ingest:

| Source              | Verbs it emits                                                                     |
| ------------------- | ---------------------------------------------------------------------------------- |
| teled, discd, slakd | `mention` on an explicit `@bot` in a group chat, else no verb, stored as `message` |
| slakd               | also `join` and `pane_open`                                                        |
| a reaction          | `like` or `dislike`, from `chanlib.ClassifyEmoji`                                  |
| emaid               | `untrusted` on mail that fails DMARC and the allowlist                             |
| routd, at ingest    | `mention` (see the verb promotion below); an empty verb becomes `message`          |

```bash
# Slack mentions only, from any workspace
arizuko route krons add 'platform=slack verb=mention' atlas/mentions

# All Slack direct messages
arizuko route krons add 'platform=slack room=*/dm/*' atlas/dm
```

### A target can name the sender

A target can contain `{sender}`. The router replaces it with a short form of
the sender id, and each sender gets a child group:

```bash
# Each Discord user gets a group under atlas/
arizuko route krons add 'platform=discord' 'atlas/{sender}'
# discord:user/alice becomes atlas/dc-alice, discord:user/bob becomes atlas/dc-bob
```

### A target fragment sets the mode

The `target` column is a URI. It has a folder and an optional fragment
`#<mode>`. The fragment selects what happens to the matched messages. A target
without a fragment means trigger. Trigger is the default and the only mode that
always fires a turn.

| Fragment    | Effect                                                                                                   |
| ----------- | -------------------------------------------------------------------------------------------------------- |
| none        | trigger: store under `target` and fire a turn                                                            |
| `#observe`  | store under `target` and fire no turn; the agent sees it in the `<observed>` block and in the history    |
| `#engaged`  | fire a turn while the chat and topic are engaged, else store under `target` as observed; never addressed |
| `#announce` | trigger, and mark this channel as the release-note target of the group for `migrate`                     |
| `#<topic>`  | trigger, and pin the topic `<topic>` on every matched message                                            |

`observe`, `engaged` and `announce` are reserved words, so no topic can have any
of these names. `folder#announce` always means the announce fragment and never a
topic called `announce`; the same holds for `#engaged`. A route row has no creation time, so nothing can tell a topic of
that name apart from the fragment. Pick another topic name.

An explicit `verb=` predicate and `seq` decide whether a message fires a turn.
A weighted score never decides. With a score, nobody can answer "will this
fire?" without a simulation. A channel that answers only on mention therefore
needs two rules: a `verb=mention` trigger above a catch-all `#observe`. See
the Discord example below.

**Engagement follows a person's message.** The agent can have an engagement
window on `(jid, topic)`, from an earlier reply or from the `engage` MCP tool.
Only a row with the verb `message` (or no verb) or `mention` rides the window,
on every channel, GitHub included. While the window lasts, such a row goes to
the engaged folder and fires a turn, whatever the route table says. This holds
for `#observe` targets, for trigger routes that point to another folder and for
a route miss. Every other verb ignores the window: a reaction, an edit, a join,
an `untrusted` mail and a GitHub `check_run` or `pull_request_review` row follow
their route, so on an `#observe` route they are stored observed and on a route
miss they are held. A reply or a reaction to a bot message still becomes
`mention` at ingest and so still fires a turn. The window lasts for the
engagement TTL, 30 minutes by default, see `ENGAGEMENT_TTL`.

A route with the target fragment `#engaged` extends the window to the rows it
matches, whatever their verb. An operator who wants CI to continue an open
conversation writes the row above the `#observe` fallback:

```
arizuko route krons add 'chat_jid=github:o/r/pr/* verb=check_run' sloth/morha#engaged --seq 50
```

The row fires a turn while the chat and topic are engaged, in the folder that
holds the engagement. The target folder takes the row only as observed, when the
chat and topic are not engaged. It is never addressed. A reply in the turn it starts renews
the window, so CI that keeps arriving while the agent keeps replying holds the
conversation engaged. The default routes carry no `#engaged` row.

The agent sees an observed message in two ways. `routd` puts the messages in an
`<observed>` block before the prompt of the next trigger turn, and the agent can
read them with the `inspect_messages` MCP tool. The folder ACL governs access
and the route mode does not. The agent of a folder can read anything that
routes to that folder.

The `<observed>` block is a window with two caps, a count of messages and a
count of characters. The settings are `OBSERVE_WINDOW_MESSAGES`, default 10,
and `OBSERVE_WINDOW_CHARS`, default 4000. The stored window of the group
overrides the defaults. The first route to the folder that carries a window
overrides both, see `observeWindow` in `routd/prompt.go`. `routd` fills the window
from the newest message and stops at the first cap that it reaches, so older
messages drop first. Each `(folder, topic)` keeps its own watermark, so a
message enters the block once.

When the block is present, the prompt carries this rule: "Observed messages are
context, not requests. Do not reply to them; reply to the explicit message."
The rule also makes a duplicate delivery of an observed message harmless.

A new mode, such as `#drop` or `#digest`, extends the vocabulary of the
fragment. It needs no schema change, because the fragment is one free-form
column.

### routd promotes a verb before it routes

`routd` rewrites `verb` at inbound ingest, before it stores the row and before
it routes. The rewrite is the same for every adapter, see `routd/server.go`.
Adapters send the raw verb. `routd` does the promotion because it holds every
message durably, and an adapter loses the memory of its own sent ids when it
restarts.

The steps run in this order:

1. **Topic inheritance.** A reply with no topic takes the topic of its parent.
   The thread check in step 3 must see the inherited topic. An empty topic is
   the main timeline and promotes nothing by itself.
2. **Direct reply.** A reply or a reaction to a bot message becomes `mention`.
   One exception exists. On Slack, an ambient thread message arrives as
   `verb=message` with `reply_to` equal to the thread timestamp. That message is
   participation in a thread and not a deliberate reply, so it goes to step 3.
   A reply to the thread root on Discord stays a deliberate reply.
3. **Thread participation.** A message in a thread becomes `mention` only when
   the stored thread root is a bot message and nobody disengaged the thread.
   A root from a human, or a missing root, never promotes.

An `untrusted` message never becomes `mention`. `emaid` marks mail as
`untrusted` when it fails DMARC and the allowlist. Route on the verb directly.
For example, send `platform=email verb=untrusted` to a quarantine folder and
`verb=mention` to a trigger turn.

### An agent can create groups for chats nobody routed

An agent that holds `mcp:register_group`, scoped to the subtree where it may
create groups, can call it when an unrouted JID arrives. The call creates a
child folder and a high-priority route in one step. The grant is always
delegated and never follows from the place of the agent in the tree. No core
mechanism exists for this. Routes and MCP tools compose to do it. See
[concepts/autoviv.html](template/web/pub/arizuko/concepts/autoviv.html).

<a id="unrouted-chats-held"></a>

### Unrouted chats are held, not delivered

`routd` holds a chat that no rule matches. It stores the messages under no
folder, advances the cursor and fires no turn. It delivers the messages
nowhere. A folder confers files, secrets, grants and egress, and a platform
lets anyone message a bot, so no group is a safe default.

`GET /v1/routing/unrouted` lists the held chats. Only a bearer without a folder
scope can call it, and the verdict comes from the own resolve step of the
loop. The page `/dash/routes/` shows the held chats to the operator with a
one-click claim that writes a `chat_jid=` rule at seq 0. A `platform=<name>`
rule claims a whole platform. History does not move. Only messages that arrive
after the claim reach the group.

With `ONBOARDING_ENABLED`, `routd` also hands a held chat to `onbod`, which
greets it with a pairing link. A Discord guild channel starts onboarding only
on an explicit `@mention`, so a busy server does not queue every poster.
[`onbod/README.md`](onbod/README.md#from-an-unrouted-chat-to-a-routed-one)
draws the flow.

No default catch-all rule exists, and `arizuko create` seeds none:

- A trigger catch-all would let anyone who finds the bot drive the agent of the
  operator, with its files, secrets and egress.
- An observe catch-all fires no turn, but it puts the text of strangers into
  the `<observed>` window of the target folder. It also ends onboarding on that
  platform. `routeMiss` takes the observe branch first, and only the other
  branch hands the chat to `onbod`, see `routd/loop.go`. A rule
  `platform=X → staging#observe` therefore means "capture and never greet" for
  platform X. Greeting and capturing exclude each other on one platform.

The same table answers who may make the agent fire. A `sender=` rule above an
observe rule for the same room fires for that sender, and everyone else matches
only the observe row. A person's message on an engaged chat and topic fires in
the engaged folder. A firing never consults the ACL, so a second permission
check in the loop has no place.

A held chat looks like a broken chat from the outside, because nobody answers
either. `GET /v1/routing/answering` tells them apart. It lists every chat whose
newest human message is newer than its newest agent reply, and it tags each
chat with the verdict of the loop. The tag `route` means a turn was due and
none answered. The tags `observe` and `held` mean that the configuration above
keeps the chat silent. The endpoint also counts the turn outcomes of the
window, so you can read `expired` turns without a hand-written query. The
operator sees both lists on `/dash/status/`.

### A Discord guild can answer only on mention

The agent reads everything in the guild and replies only when someone mentions
it. A guild channel has the JID `discord:<guild_id>/<channel_id>`, so the room
is `<guild_id>/<channel_id>` and the glob `<guild_id>/*` matches every channel
of one guild.

```
seq  match                                           target
5    platform=discord room=<sloth_guild_id>/*        main
10   platform=discord room=dm/*                      main
20   platform=discord verb=mention                   main
30   platform=discord                                main#observe
```

The rule at seq 20 fires a turn on every `@bot` message in any guild. The
catch-all at seq 30 stores all other messages under `main` in observe mode,
and fires no turn. While the agent holds an engagement window on a channel, a
person's plain message there also fires a turn. The rule at seq 5 opts one
guild out of the mention gate, and every message there is a trigger. The rule at seq 10 makes every direct
message a trigger. Without it, a direct message has no `mention` verb and
falls into the observe rule.

### A Slack workspace can answer only on mention

The agent reads all channels and replies only when someone mentions it
directly:

```
seq  match                                             target
10   chat_jid=slack:*/dm/*                             atlas
40   chat_jid=slack:*/channel/* verb=mention           atlas
40   chat_jid=slack:*/group/*   verb=mention           atlas
100  chat_jid=slack:*/channel/*                        atlas#observe
100  chat_jid=slack:*/group/*                          atlas#observe
```

A message in a Slack channel or private group without `@atlas` matches seq 100.
`routd` stores it as observed context, no turn fires, and atlas stays silent.
An `@atlas` mention becomes `verb=mention` in the verb promotion. It matches
seq 40 first and fires a turn. While atlas holds an engagement window on a
channel, a person's plain message there also fires a turn. A direct message
matches seq 10, a plain trigger, so the agent always replies in direct
messages.

To make the agent reply to every channel message, change the targets at seq 100
from `atlas#observe` to `atlas`.

### An @name or #topic prefix is code, not a route

The `@` and `#` prefix layer is not in the routes table. It lives in
`handlePrefixLayer` in `routd/steer.go`. An anchored regular expression,
`^\s*@(\w[\w-]*)` or `^\s*#(\w[\w-]*)`, matches a sigil at the very start of
`msg.Content`. A `@handle` or `#hashtag` inside the content, for example in a
forwarded tweet, does not trigger the layer. The layer ignores messages from
bots and from `timed-` senders.

- **`@name` at the start of a message.** `routd` looks for the child group
  `<current-group>/<name>`. If the child exists, `routd` delegates the message,
  with `@name` removed, to the agent of that child.

  ```
  User in group atlas sends: "@content write a blog post about cats"
  parsePrefix: name="content", rest="write a blog post about cats"
  Child lookup: atlas/content exists
  Delegation: "write a blog post about cats" goes to the atlas/content agent
  ```

- **`#topic` at the start of a message.** `routd` stores the message, with
  `#topic` removed, under the topic `#topic` and runs it in a session for that
  topic inside the current group.

  ```
  User in atlas sends: "#support my account is locked"
  Agent runs: "my account is locked" in session (atlas, #support)
  ```

If the child group named by `@name` does not exist, the prefix stays in the
message, and the message goes to the agent unchanged.

The prefix layer is code and not `routes` rows, because `@name` is relative to
the folder that owns the chat. A row would repeat for every group, and each
folder move would force a rewrite.

The layer reads one row for each poll batch. The row is the oldest slash
command when the batch holds one. Otherwise it is the newest user message, see `steerRowIdx` in `routd/dispatch.go`. The layer does not apply an `@name` or
`#topic` on an older row of the same batch.

## routd resolves a chat in two stages

`routd` resolves a batch of user rows of a chat in two stages, see `resolveFolder` in `routd/loop.go`, then `steer` in `routd/steer.go`. The
first stage picks the folder. The second stage runs inside that folder. It can
consume the message before any turn starts.

**A batch resolves by its newest row that dispatches on its own.** `routd`
reads the route table once per batch and scans the user rows from the newest.
The first row whose own resolution dispatches picks the folder and the topic
pin for the batch. The addressed flag is per turn: a turn is addressed when one
of its own rows is addressed, so a CI row under `#engaged` that runs beside a
mention in its own turn is not owed an answer. A row dispatches on its own when a
trigger rule matches it, or when it is a `message` or `mention` on an engaged
chat, or when an `#engaged` rule matches it on an engaged chat. When no row dispatches, the newest user row decides: a route miss or an
`#observe` match. Bot rows never resolve a batch. A mention in a batch therefore
starts a turn even when a newer row of the same batch matches only `#observe`.
Every other user row that the turn consumes is stored observed under the batch
folder before the prompt is built, and it reaches the turn in the `<observed>`
block. The cursor passes the mention and the observed rows together. Three
questions keep three rows: the oldest slash command (or else the newest user
row) steers, the newest user row keys the busy backoff, and the row that
dispatches resolves the folder.

A batch can run as more than one turn, one per sender and topic. When runed
refuses a turn with Busy, or the dispatch fails, after earlier turns of the
batch finished, the cursor moves to the row before the refused turn. The
re-feed then starts at that turn, so a finished turn never runs again, even
when the engagement it opened makes more rows dispatch.

A row that arrives while a turn runs waits above the cursor. When the turn ends,
the queue drains it. A person's message rides the window that the reply renewed
and starts the next turn. Any other row becomes observed context of the next
turn, through the same rule.

```
inbound row
   │
   ▼
1. direct address: a web:/hook: JID or a bare folder ──────────────┐
   │ none                                                          │
   ▼                                                               │
2. route table, the first match by seq                             │
   │ a trigger rule, or a person on an engaged chat ► a folder ────┤
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
   record, and a JID without `:` names a registered folder. The route table
   does not apply, and rows that agents write never get content routing. A
   prefix that only meant "not a platform" would be noise.
2. **Route table.** `routd` checks the rules in ascending `seq`, and the first
   match wins. The match expression is described above. A `message` or `mention`
   on an engaged chat overrides the folder of a matched rule. A matched `observe`
   rule on an engaged chat counts as engagement alone. A matched `#engaged` rule
   fires only on an engaged chat.
3. **Engagement.** Engagement rescues a miss for a `message` or `mention`. The lookup uses the effective
   topic. It then falls back to the root topic `""`, unless someone disengaged
   that thread. A reply inside a thread therefore keeps the window that its
   root started. `routd` claims engagement at dispatch and never at ingress,
   because the folder is unknown until this step. Resolution runs before the
   miss handler, so an engaged chat never reaches onboarding. The grants of the
   engaged folder are not checked again on this path, because the folder held
   them when it engaged.

`routd` logs a read failure of the route table at ERROR and treats it as a
miss. A retry would loop on a poison message.

The second stage runs these steps in order:

1. A bare `@group` or `#topic` sets sticky navigation.
2. A slash command runs.
3. An `@child` or `#topic` prefix delegates the message, or posts it again
   under that topic.
4. `routd` looks up a delegation target. It tries the folder that a replied-to
   bot row was routed to, then the sticky group of the chat, then the route
   table.

`routd` uses a target only when it differs from the current folder and passes
the authorization check below. Each consumed row advances the cursor and starts
no turn. [ARCHITECTURE.md](ARCHITECTURE.md#message-flow) describes what
`runTurn` does, up to the send by the adapter.

### A delegation target must be the direct child of the source

`router.IsAuthorizedRoutingTarget(source, target)` enforces two rules:

- A source in the `root` world can route to any target.
- Any other source can route only to a direct child, one level deeper.

```
source="atlas"       target="atlas/content"    → allowed (direct child)
source="atlas"       target="atlas/content/x"  → denied (grandchild)
source="atlas"       target="other/content"    → denied (different world)
source="root"        target="atlas/content/x"  → allowed (root)
```

## Topics isolate threads inside one group

A topic gives a thread its own session inside one group. Each topic has its
own Claude session, reply chain and message history.

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

- **The session id is declared before the run.** `routd` mints the id of the
  topic when it has none, and it stores the id in `sessions` before it calls
  `runed`, see `dispatchRun` in `routd/dispatch.go`. Claude opens its session
  under that id. A restart, a deadline or a crash can kill a turn before
  `submit_turn`. The next turn in the topic then resumes the session
  , see `routd/session_continuity_test.go`. Codex and pi start fresh under that id
  and report their own, which `submit_turn` then stores, see BUGS `L432`.
- **An idle session expires.** When the cursor of the chat is older than
  `SESSION_IDLE_EXPIRY`, two days by default, `routd` drops the session id of
  the topic before the spawn. A resume after days makes the agent mix stale
  state into the current turn.
- **An isolated task has no session.** A `timed-isolated:` turn runs with no
  resume and never writes its id back. A tool-heavy cron run appended to the
  transcript of the chat would push the humans out at compaction.

### Four sources set a topic

1. **A native thread of the platform.** The channel adapters map native thread
   ids to `Message.Topic`. Examples are `MessageThreadID` on Telegram and the
   thread channel id on Discord. Outbound is symmetric. The `reply` of the
   agent lands in a thread off the message it answers. Discord starts a thread
   if none exists, and Slack roots `thread_ts` on the trigger. The reply of the
   agent therefore seeds the topic that later in-thread messages inherit.
   `send` stays top-level. The group setting `thread_replies` toggles this
   behavior. It is on by default for group chats and off in direct messages.
2. **The web channel.** Web messages carry a topic slug. The JID format is
   `web:<folder>`, and the topics arrive already set.
3. **A `#topic` prefix.** When a message starts with `#name`, `routd` runs the
   agent with the topic `#name`.
4. **A sticky topic.** The `#topic` command sets a topic that stays for the
   chat. See "Sticky routing" below.

### A topic affects five parts of processing

- **Session isolation.** Each `(folder, topic)` pair has its own session id,
  from `store.GetSession(folder, topic)`. Resetting one topic leaves the others
  untouched. `/new #support` resets only the `#support` session.
- **Reply chain isolation.** `store.LastReplyID(jid, topic)` stores the last
  sent message id of each topic, for reply threading.
- **Batching by topic.** A web chat dispatches one turn for each run of
  consecutive messages with the same topic. Other chats dispatch one turn for
  each run of consecutive messages with the same sender and topic
  , see `consecutiveRuns` in `routd/dispatch.go`. The turns run one after the
  other.
- **Lineage.** Every topic except main carries `parent_topic`, `forked_at` and
  `observed_cursor` in `sessions`. The parent of a new topic is the topic of the
  message it replies to, or main `""`. The first turn takes the observed cursor
  of the parent. An explicit `fork_topic` takes the current time. Both copy the
  session transcript of the parent to the id of the child
  , see `container.CopySession`. The child therefore resumes natively from the tail
  of the parent, with no `<inherited>` block. Every turn carries a
  `<topic name=…/>` envelope. The observed cursor belongs to one topic, so two
  topics in one folder both see `<observed>` context, and neither consumes it
  for the other.
- **Two ways to see another folder.** `observe_group` subscribes this folder to
  the inbound messages of a source folder through `group_watchers`. The feed is
  one-way and read-only. `set_group_open` makes the already observed rows of a
  folder visible to its siblings. The two tools answer two questions. The
  single evaluator authorizes each tool on its target folder.

### A sticky topic overrides the native topic

`effectiveTopic` returns the sticky topic of the chat when one is set. Otherwise
it returns the topic of the message. A sticky topic therefore overrides the
native topic of the message. Send `#` to clear it.

<a id="reply-routing"></a>

## Reply routing sends a reply to the group that wrote the answer

When a user replies to a bot message, the reply goes to the group that wrote
the answer. This holds even when the default route points elsewhere.

### The messages table records which group answered

`PutMessage` stores every outbound bot message with `RoutedTo` set to the
group that produced it, and with `Sender`, `Topic`, `FromMe` and `BotMsg`. When
a user replies to a message, the platform supplies `reply_to_id`. `routd` looks
up `routed_to` for that message.

`resolveTarget` in `routd/steer.go` picks a delegation target in this order:

1. **The reply chain.** If `reply_to_id` points to a bot row with a `routed_to`
   value, that group is the target. If it is the current folder, no delegation
   happens.
2. **The sticky group** of the chat.
3. **The route table.**

`forwarded_from` on a row is a return address and never a marker of a channel
forward. `delegate` and `escalate` write a row into the queue of the target
folder, and they set `forwarded_from` to the chat that asked, or to the JID of
the escalation worker, see `delegateViaMessage` in `routd/steer.go`. The turn that
the row starts delivers its `reply` and `send` there.

`routd` chooses the adapter that carries a reply in this order, see `resolveEntry` in `routd/deliver.go`:

1. An explicit channel pin. Only `POST /v1/outbound` carries one.
2. The adapter that delivered the latest inbound message of the chat,
   `messages.source`.
3. The first registered adapter that owns the prefix of the JID.

The middle step matters when two accounts on one platform share a prefix,
because the prefix lookup returns whichever adapter it finds first.

### A reply returns to the group that wrote the answer

```
Routes: match='platform=telegram' → atlas
Groups: atlas, atlas/content, atlas/social

1. User sends: "@content write about cats"
   → the prefix layer matches and delegates to atlas/content
   → atlas/content replies: "Here's a post about cats..." (routed_to=atlas/content)

2. User replies to that message: "make it shorter"
   → reply_to_id points to the bot message
   → the lookup of routed_to returns "atlas/content"
   → the message routes to atlas/content again, not to atlas

3. User sends a new message, not a reply: "hello"
   → no reply chain, no sticky
   → route 'platform=telegram' → atlas
```

### The last reply id builds the reply chain on the platform

`routd` stores the last sent message id for each `(jid, topic)`. The platform
uses it to build reply chains:

```
User: "help"                   (msg_id=100)
Bot:  "Sure, what's up?"       (reply_to=100, sent_id=201)
Bot:  "I can help with..."     (reply_to=201, sent_id=202)
```

`store.LastReplyID(jid, topic)` gives the anchor for the next reply. Three
events update it:

1. **The bot sends a chunk.** `SetLastReply` stores the sent message id.
2. **A steering message arrives.** When `routd` steers a follow-up into a live
   turn, it sets `lastReplyID` to the id of the steering message.
3. **A new agent run starts.** `SetLastReply` stores the last user message of
   the batch. `routd` reads `GetLastReplyID` again before each chunk, to pick
   up steering updates during the run.

A topic that `fork_topic` creates inherits the last-reply context of its parent
as the first anchor. After that it advances its own `lastReplyID`
independently.

## Sticky routing locks a chat to a group or topic

A sticky command locks a chat to a group or a topic. Users then do not repeat
a prefix on every message.

### Four commands set or clear the lock

A message is a sticky command when its entire content, after trimming, is one
of these forms with no other text:

| Input            | Effect                                           | Confirmation               |
| ---------------- | ------------------------------------------------ | -------------------------- |
| `@atlas/content` | Lock the chat to the group `atlas/content`       | `routing → atlas/content`  |
| `@`              | Clear the sticky group and return to the default | `routing reset to default` |
| `#support`       | Lock the topic of the chat to `support`          | `topic → support`          |
| `#`              | Clear the sticky topic                           | `topic reset to default`   |

`routd` does not consume `@<unknown>`, a name that matches no group folder. It
passes the message to the agent unchanged, with no confirmation and no error.
Messages that start with `@` have many other meanings. Examples are mentions
such as `@everyone`, references to another instance such as `@sloth`, and
ordinary sentences. An unknown name cannot safely mean "set sticky". The agent
decides whether the text is a typo, a mention or a `delegate_group` call to a
child.

`routd` stores the sticky state for each chat in the `chats` table, in the
columns `sticky_group` and `sticky_topic`. An absent value means no sticky. All
users in the same chat share one sticky state. Bot messages and messages from
`timed-` senders never act as sticky commands.

### Sticky state sits between the reply chain and the route table

`resolveTarget` checks the sticky group after the reply chain and before the
route table. This has three effects:

- A reply to a specific bot message still routes to the group that produced
  it. The reply chain takes precedence.
- Sticky overrides the default route table.
- An inline `@name` in a message with content, such as `@content do this`,
  still fires the prefix layer for that one message. Sticky persists.

## One chat can reach several groups

A single chat JID can route messages to different groups, based on content,
replies and sticky state.

```
Routes table:
  seq=0  match='platform=telegram'  target=atlas    # catch-all

The prefix layer is built in and needs no table rows.

Possible message flows in the same chat:
  "hello"                → atlas (route)
  "@content draft post"  → atlas/content (prefix @)
  [reply to content msg] → atlas/content (reply chain)
  "#support help me"     → atlas, session=#support (prefix #)
  "@atlas/social"        → sticky set to atlas/social
  "what's new"           → atlas/social (sticky override)
  "@"                    → sticky cleared, back to atlas
```

Each group runs its own agent with its own session. Reply chains and topic
sessions keep the conversations separate, even inside one chat.
