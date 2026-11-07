---
status: draft
---

# specs/8/40 — groups without agents

## What it is

A group is a place where messages, routes, files and web pages live. An agent
is optional: a group can bind one, or bind none and only hold what other agents
and people write to it. One group setting, `agent`, says which.

## The problem

Today every group is an agent. Each row that resolves to a folder starts a
turn, and no group fact can stop that.

The one fact that stops a turn is per route: a `folder#observe` target stores
the row under the folder with no turn (`routd/loop.go:694-699`). But the
resolver reads the route table only when the chat is not a direct address
(`routd/loop.go:645-660`). A `web:<folder>` chat, a `hook:` token and a bare
folder JID always dispatch (`routd/loop.go:663-666`). Those are the doors that
agents and outside callers use to write to a group:

- The agent tool `send` with a group folder as `chatJid` writes an inbound
  row through `store.PostGroup` (`ipc/ipc.go`, `store/messages.go`).
- webd's `/mcp` `send` writes a row to `web:<folder>` (`webd/mcp.go:141-169`).
- a default task names the folder itself as its chat (`store/tasks.go:510`,
  seeded by `arizuko group add` at `cmd/arizuko/main.go:467`).

So a group that only carries messages between agents still boots a guest for
each post. An all-`#observe` route set does not help: no route row is read on
these paths.

Three other facts look like a marker and are not one:

- `PERSONA.md` is a file that the agent and every `/dav/` writer can change,
  and the prompt reads it as optional (`routd/prompt.go:121-128`). A file that
  switches dispatch is the magic that the "strict, not magical" rule bans.
- The core package is a required system package, so every folder holds it
  (`ARCHITECTURE.md` § "Core vs integrations").
- A runed hold refuses the run with `busy`, and routd feeds the batch again
  after the backoff (`runed/README.md` § "What runed does"). The rows never
  settle.

## Where it fits

```
 adapter   webd /mcp send   agent send       timed
    │            │                │            │
    └────────────┴───── PutMessage (routd) ────┘
                              │
                              ▼
            ┌──────────────────────────────────────┐
            │ routd resolveRows                    │
            │ direct ► routes ► engagement         │
            │ group.agent read once per folder     │
            │ (new)                                │
            └───────┬──────────────────────┬───────┘
                    │ agent bound          │ no agent (new)
                    ▼                      ▼
            ┌───────────────┐      ┌──────────────────────────────┐
            │ runTurn       │      │ MarkMessagesRouted           │
            │ POST /v1/runs │      │ observed=false (changed)     │
            └───────┬───────┘      └──────────────┬───────────────┘
                    ▼                             ▼
            ┌───────────────┐      ┌──────────────────────────────┐
            │ runed: guest  │      │ message_folders feed         │
            └───────────────┘      │ partition = the folder       │
                                   └──────────────┬───────────────┘
                                                  │ watched read
                                                  ▼
                                   ┌──────────────────────────────┐
                                   │ an observer's next turn      │
                                   │ (observe_group)              │
                                   └──────────────────────────────┘
```

routd keeps the comms half and the dispatch decision. runed keeps the agent
runtime: the guest, the busy refusal, the circuit breaker and `session_log`.
runed does not change, because routd never calls it for a group with no agent.

The binding is a fact on the group row in routd, and not in runed, for two
reasons. The decision to dispatch is made in `resolveRows`, so a binding in
runed puts an HTTP read on the busiest path. And every config fact that a
spawn carries is on that row already: `GroupConfig` reads `model`, `effort` and
`container_config` there and routd sends them in the run request
(`store/groups.go:571-591`). runed reads no routd config today, and this keeps
it so.

The change deletes nothing. No mechanism does this job today, so there is no
old one to remove.

## Module map

```yaml
- path: core/types.go (changed)
  job: the group row type
  names:
    - 'Group.Agent bool  // read-only projection of the `agent` setting'
  never: is written through PutGroup; its setter is the only writer
- path: store/groups.go (changed)
  job: reads and writes the `agent` key of the settings document
  names:
    - 'cfgAgent = "agent"  // absent = an agent is bound'
    - 'SetGroupAgent(folder string, bound bool) error'
  never: deletes routes, tasks or files when the binding changes
- path: store/message_folders.go (changed)
  job: stamps routed_to and the folder feed, with or without the observed flag
  names:
    - 'MarkMessagesRouted(ctx, folder string, msgs []core.Message, observed bool) error'
  never: decides which flag a row gets; the caller passes it
- path: routd/db.go (changed)
  job: the routd wrapper and the search index write beside it
  names:
    - 'MarkMessagesRouted(folder string, msgs []core.Message, observed bool) error'
  never: reads the group row
- path: routd/loop.go (changed)
  job: the one resolver; a row for a group with no agent resolves to no turn
  names:
    - "resolution.NoAgent bool  // Observe's folder binds no agent"
    - 'observeRows(chatJID, folder string, rows []core.Message, observed bool)'
  never: sends a notice to the chat for a group with no agent
- path: dashd/groups_admin.go (changed)
  job: the group settings form gains an "Agent" checkbox beside the model
  names:
    - 'handleGroupSettingsSave writes SetGroupAgent'
  never: offers the field to a caller without requireAdmin on the folder
```

## The binding is one key in the settings document

The settings document of a group already holds the operator keys `model`,
`effort`, `open` and the two observe windows. `agent` is one more key with the
same encoding as `open`: absent means bound, so every group that exists keeps
its agent and no data moves.

Current, `store/groups.go:46-53`:

```go
const (
	cfgModel          = "model"
	cfgEffort         = "effort"
	cfgOpen           = "open"
	cfgObserveWinMsgs = "observe_window_messages"
	cfgObserveWinChar = "observe_window_chars"
)
```

Proposed:

```go
const (
	cfgModel          = "model"
	cfgEffort         = "effort"
	cfgOpen           = "open"
	cfgAgent          = "agent"
	cfgObserveWinMsgs = "observe_window_messages"
	cfgObserveWinChar = "observe_window_chars"
)

// in groupFrom, beside Open: absent means an agent is bound.
agent, ok := settingNum(cfg, cfgAgent)
g.Agent = !ok || agent != 0

// SetGroupAgent binds or unbinds the group's agent. The group must exist.
func (s *Store) SetGroupAgent(folder string, bound bool) error {
	return s.setSetting(folder, cfgAgent, btoi(bound), false)
}
```

A key and not a column, because `open` set the pattern: a settings key that
only its own setter writes, so an upsert of `container_config` cannot clobber
it (`core/types.go:88-100`). The caveat: `PERSONA.md`, the skills and the
default tasks stay in the folder of a group with no agent. They do nothing
there, and they apply again when the agent is bound again.

## A row for a group with no agent is routed, and no turn runs

Every path that starts a turn passes `resolveRows`. `runTurn` has one caller,
`processGroupMessages` (`routd/dispatch.go:143`), and the proactive scanner
asks `resolveGroup`, which calls the same resolver (`routd/proactive.go:184`).
So one check at the end of `resolveRows` covers the direct addresses, the
route table and engagement.

Current, `routd/loop.go:643-675`:

```go
func (l *Loop) resolveRows(chatJID string, rows []core.Message) []resolution {
	out := make([]resolution, len(rows))
	// 1. Direct address: a web:/hook: JID or a bare registered folder.
	direct := l.directFolder(chatJID)
	isDirect := direct != "" && l.db.GroupExists(direct)
	// ... the route table read ...
	reads := &chatReads{db: l.db, chatJID: chatJID}
	for i, m := range rows {
		if isDirect {
			out[i] = resolution{Folder: direct, ok: true, Addressed: core.JidPlatform(chatJID) == "web"}
		} else {
			out[i] = l.resolveFolder(reads, m, routes, routesOK)
		}
		if isSystemTrigger(m.Sender) {
			out[i].Addressed = false
		}
	}
	return out
}
```

Proposed:

```go
func (l *Loop) resolveRows(chatJID string, rows []core.Message) []resolution {
	out := make([]resolution, len(rows))
	direct := l.directFolder(chatJID)
	dg, isDirect := l.db.GroupByFolder(direct) // one read gives both facts
	// ... the route table read, unchanged ...
	bound := map[string]bool{direct: !isDirect || dg.Agent}
	agentOf := func(folder string) bool {
		if b, read := bound[folder]; read {
			return b
		}
		g, ok := l.db.GroupByFolder(folder)
		bound[folder] = !ok || g.Agent // no row stays the ghost case in runTurn
		return bound[folder]
	}
	reads := &chatReads{db: l.db, chatJID: chatJID}
	for i, m := range rows {
		// ... unchanged ...
		if out[i].ok && !agentOf(out[i].Folder) {
			out[i] = resolution{Observe: out[i].Folder, NoAgent: true}
		}
	}
	return out
}
```

`processGroupMessages` then takes the route-miss branch (`routd/dispatch.go:59-63`),
and `routeMiss` stores the rows under the folder (`routd/loop.go:528-533`).
The verdict page and the unrouted list read `Observe` already, so they show
these chats as observed and not as held (`routd/answering.go:90-98`,
`routd/unrouted.go:51`).

A folder with no group row keeps the ghost refusal in `runTurn`
(`routd/dispatch.go:339-346`): that is a configuration error, and a group with
no agent is not one. The caveat: no turn means no `round_done`, so a client
that waits for one, such as `arizuko talk`, waits for good (question 6).

## The rows of a group with no agent are its posts, not ambient context

`observe_group` is how one agent reads another folder: the watched folder's
rows render as `<observed>` in the observer's next turn. The watched read takes
only the rows that the source did not observe (`store/message_folders.go:304-315`).
If a group with no agent stored its rows with the observed flag, an observer
would see none of them. So these rows get `routed_to` and no flag. The flag
means "an agent saw this as context and did not answer", and here no agent
exists.

Current, `store/message_folders.go:165-185` and `routd/loop.go:528-533`:

```go
func (s *Store) MarkMessagesObserved(ctx context.Context, folder string,
	msgs []core.Message) error {
	// ...
		stamp := objstore.Write{
			Op: objstore.Update, Entity: Messages, Key: msgKey,
			Attrs: map[string]string{attrMsgRoutedTo: folder, attrMsgObserved: flagSet},
		}
```

```go
	if r.Observe != "" {
		// Observe rule: silent ingest, no turn.
		l.observeRows(chatJID, r.Observe, msgs)
	} else {
```

Proposed:

```go
// MarkMessagesRouted stamps routed_to and the folder feed. observed sets the
// flag on both: true for a `folder#observe` rule, false for a group with no
// agent, whose rows are its own posts.
func (s *Store) MarkMessagesRouted(ctx context.Context, folder string,
	msgs []core.Message, observed bool) error {
```

```go
	if r.Observe != "" {
		// An observe rule, or a group with no agent: stored, no turn.
		l.observeRows(chatJID, r.Observe, msgs, !r.NoAgent)
	} else {
```

One writer with one flag, and not a second writer, because the two writes
differ in one attribute and share the move of a row from an old folder's feed
and count (`store/message_folders.go:182-235`). The caveat: if a group gets an
agent later, its own ambient read asks for flagged rows
(`store/message_folders.go:305-308`), so the posts from before the binding do
not render in its first turn.

## Agents talk through a group with no agent using tools that exist

```
 agent in folder a                                agent in folder b
   │ send(chatJid=x, text=...)                       ▲ observe_group(source=x)
   │ gate: mcp:send on x                             │ rows render on b's next turn
   ▼                                                │
 ┌──────────────────────────────────────────────────┴─┐
 │ group x, agent=0 (new)                             │
 │ chats: x (bare folder JID), web:x                  │
 │ files: groups/x  web slot: web/pub/x, web/priv/x   │
 └──────────────────────────────────────────────────┬─┘
   ▲                                                │
   │ /mcp send(folder=x)                            ▼ /mcp get_history(folder=x)
 external agent                                   external agent
```

The agent tool `send` posts to a bare folder after checking `mcp:send` on
that target (`ipc/ipc.go`). `store.PostGroup` writes a non-bot row with the
calling folder as `ForwardedFrom`. A platform-qualified target follows the
platform delivery path and writes a bot row. The watched read skips bot rows
(`store/message_folders.go`).

A post to a group with no agent does not wake a reader. To request work now,
an agent sends to a group with an agent bound. The external read sees one of
the two chats, as question 4 describes.

## External agents use the bearer and the grant that exist

```
 external agent
   │ Authorization: Bearer <ES256, sub=user:<name>>
   ▼
 ┌──────────────────────────────────────────────┐
 │ proxyd requireAuth                           │
 │ tryAuth: verify against authd's JWKS         │
 │ groupsForSub: acl rows of <name>, read on    │
 │ every request ► X-User-Groups                │
 └──┬──────────────┬──────────────┬─────────────┘
    │ /mcp         │ /dav/<f>/    │ /priv/<f>/
    ▼              ▼              ▼
  webd           davd           vited
  MatchGroups    MatchSlot,     MatchSlot
  send,          davAllow
  get_history,   groups/<f>
  list_groups
```

This is the path `arizuko talk` takes today (`cmd/arizuko/talk.go:20-35`). The
operator gives the outside agent a name and a grant, then a bearer:

```
arizuko group -i <inst> grant <name> '<folder>'
arizuko token -i <inst> issue bearer <folder> --sub user:<name> --scope messages:read,messages:write --ttl 720h
```

The grant decides the reach, and the bearer only proves the name. proxyd
re-reads the `acl` rows on each request, so `ungrant` stops the bearer on the
next request and not at its expiry (`proxyd/hotpath_read_test.go`,
`proxyd/main.go:978-991`). The bearer's `scope` and "arz/folder" claims are not
read on this path. The same token at routd's `/v1` is read by those claims
(`routd/server.go:288-306`), but no proxyd route reaches routd's `/v1`
(`compose/compose.go:555-600`), so an outside caller never meets that reading.

A route token is the other outside credential, and it stays a capability for
one chat (`webd/mcp.go:59-78`). The turn's credential is not a token: the
guest's vsock CID at runed's bridge and the `SO_PEERCRED` check in routd
(`runed/README.md` § "The peer credential"). An outside agent has neither, so
it is a principal like a person: one `sub`, its `acl` rows, the same middleware.

This adds nothing. Its caveat is question 5: `X-User-Groups` lists scopes and
not actions, so a grant that lets a caller post to a folder also lets it write
the folder's files over `/dav/`.

## Open questions

1. **Who may set `agent`?** The form in `dashd/groups_admin.go:353-387` saves
   `model` for a caller with `requireAdmin` on the folder. Answers: (a) only
   that form; (b) also an agent tool, or a `register_group` parameter, so that
   an agent can make a group with no agent for its children. Recommend (a): an
   agent that unbinds a folder's agent changes who answers that folder's
   chats, and [paths and roles](../5/33-paths-roles.md) has no grant for that today.

2. **What does a write that asks for a run return for a group with no agent?**
   The agent tool `send(chatJid=x, text="...")` returns `{"ok": true, "id": "..."}`.
   `store.PostGroup` records the sender as `delegate` and the posting folder
   as `ForwardedFrom`. A timed task into x fires and nothing runs. Answers:
   (a) refuse with "x has no agent"; (b) store it, and the reply says
   `"agent": false`. Recommend (b) for `send` to a group, plus the posting
   folder as the sender, so a reader can tell which agent wrote.

3. **Does the folder of a group with no agent keep its default tasks?**
   `arizuko group add` seeds the default tasks before any setting exists
   (`cmd/arizuko/main.go:467`), so they fire into x each day and their rows
   land in x's feed. `ObservedSince` drops scheduled senders
   (`store/message_folders.go:339-341`), so no reader sees them. Answers:
   (a) leave them; (b) `SetGroupAgent(false)` pauses them. Recommend (a): the
   rows are harmless, and a second write in the setter couples the binding to
   the task table.

4. **Which chat does an outside agent read?** An inside post lands in the chat
   `x`; an outside post lands in `web:x`. webd's `get_history` reads `web:x`
   by topic only, and refuses an empty topic (`webd/mcp.go:180-185`). Answers:
   (a) `get_history` with no topic reads x's folder feed, where both chats'
   rows are routed; (b) leave it. Recommend (a): the feed is the one index
   that holds every post to x, whichever door wrote it.

5. **May an outside agent post to a group with an agent and not write its
   files?** `groupsForSub` lists every allow scope of the caller whatever the
   action (`auth/authorize.go:239-259`), and `/dav/` gates on that list
   (`proxyd/main.go:704-712`). So a grant to post to "corp/sales" is also a
   grant to write "corp/sales/PERSONA.md". This is BUGS `F5` and `L493`, which
   wait for sign-off. Recommend: this spec does not change it. On a group with
   no agent the files belong to the group, so the full reach is the intended
   reach; on a group with an agent, grant an outside agent only after `F5`
   decides which action `/dav/` and `/mcp` ask for.

6. **Does a client see that a group has no agent?** `list_groups` returns
   only the folder (`webd/mcp.go:120-137`), and `arizuko talk` waits for a
   `round_done` that no turn sends. Answers: (a) `list_groups` returns
   `agent`, and `talk` refuses a folder with `agent=false`; (b) nothing.
   Recommend (a): a wait that cannot end must fail at the door.

7. **Can an outside agent write a group's web pages?** davd serves
   `<data>/groups` only (`compose/compose.go:1387`), and the pages live in
   `<data>/web/pub/<folder>`, which only a turn mounts (`runed/mounts.go:225-230`).
   Answers: (a) no door; an outside agent publishes on its own host; (b) davd
   mounts `<data>/web` too, under a first segment that `reservedFolders` holds
   (`groupfolder/folder.go:61`). Recommend (a) until a case needs it: reading
   `/pub/` and `/priv/` already works with the bearer above.

8. **Does the rest of the agent half move to runed?** The topic-to-session map
   is routd's `sessions` entity and the run history is runed's `session_log`;
   `model`, `effort` and `container_config` are on the routd group row.
   Answers: (a) move them into a runed entity; (b) leave them. Recommend (b):
   no case needs it, the run request already carries them, and a move puts
   routd's dispatch behind a runed read.
