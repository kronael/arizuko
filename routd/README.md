# routd

routd implements the conversation plane of an arizuko instance, and channel adapters, operator tools and agents use it through REST on port 8080 and one MCP socket per turn. It stores messages, resolves routes, runs the turn loop and serves the tool socket of the agent.

## What routd owns

routd is the only code that appends messages. It verifies tokens and never signs them, because authd signs. It owns the `routd` tables, resolves routing rules, and calls runed (`POST /v1/runs`) to execute each turn. runed starts no MCP server. Its bridge carries the guest's tool calls to the per-turn socket that routd hosts in its own process (`ServeTurnMCP`, `mcp.go`). The bridge is described in `runed/README.md` under "The peer credential".

routd does these jobs:

- It ingests inbound messages from channel adapters and appends them.
- It resolves `routes` to a target folder. The route rules are topic, sticky and reply rules.
- It runs the orchestration loop: claim a turn, dispatch it to runed, record the result.
- It hosts the per-turn MCP socket. It reads the capabilities of the folder from `acl` and wires `ipc.ServeMCP` to its own DB and Deliverer. It serves the conversation tools, for example `reply`, `send` and `like`.
- It sends channel egress through the `chanreg` Deliverer. Adapters register an egress URL and the JID prefixes they own.
- It answers ranked message search, `find_messages` and `GET /v1/messages/find`.
- It owns the turn's progress, as the next section shows.

routd injects the agent's authorization gate into the socket: `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)`. It limits tool visibility with `auth.EffectiveActions`. The resreg resources that routd serves reuse the same injected gate instead of the default operator gate.

### Progress has one writer

`progress.go` is the only code that calls `Typing` or creates or edits a `verb="status"` row. The hidden method `submit_status` receives the declared task summary and the progress updates. `placement.go` gives every delivery of the turn one destination.

- A compatible channel gets an immutable `🤖` summary opener. One editable `⏳` progress message follows inside its thread.
- An existing thread wins.
- A channel without new-thread support gets one editable progress card.
- A channel without `edit` gets no progress message. gitd advertises no `edit`, so GitHub shows the `👀` mark and no progress comment.
- Persisted message rows keep the edits and the opener identity across a restart.
- `placement.go` sends progress, text, files, voice, terminal notices and retries to the same destination.
- An unresolved opener refuses an unplaced child. routd does not queue it.
- A failed summary read or thread error goes back to the caller. No child falls back to the parent.
- A real edit failure goes back to the caller. routd never posts it again as a new message.

Both progress verbs confer a reply obligation. Final delivery and last-answer tracking skip them, because a progress row must never count as the agent's reply.

`Typing(on)` fires at dispatch. `Typing(off)` fires when the turn ends in `finishTurn`, the one writer of `done`. On a channel that advertises `delete`, `finishTurn` deletes the progress message, rewrites the row to its terminal form and does nothing at a second end. A refused delete falls back to the edit. On a channel with `edit` and no `delete`, `finishTurn` changes only the leading `⏳` of the progress message to `✓` or `✗`. The task summary row stays. A run that returns without ending its turn clears `Typing` and leaves the message at `⏳`.

## The turn in routd

`ARCHITECTURE.md` section "Message Flow" shows the whole turn. These rules describe routd's half.

- **One cursor.** routd is the only appender of `messages`, so one cursor, `chats.agent_cursor`, means "what the agent has seen". A second writer would need a second cursor.
- **One process.** routd runs as one process per instance. Two routd processes on one store are not supported. The in-memory `GroupQueue` allows one turn per folder, and `agent_cursor` recovers a crash.
- **Ingress.** `POST /v1/messages` honours `X-Idempotency-Key` only when the body has no `id`. routd then mints `id=<adapter>-<key>`, so a re-sent event lands on the same row. A body with both returns `400 ambiguous_idempotency` (`server.go`). routd does not claim engagement at ingress, because the folder is unknown until resolution, and a claim with an empty folder would misroute.
- **Resolution.** `ROUTING.md` section "routd resolves a chat in two stages" gives the order. A batch resolves by its newest user row that dispatches on its own, so a mention starts a turn even when a newer row of the batch matches only `#observe`. The other user rows the turn consumes are stored observed under the batch folder before the prompt is built. Only a `message` or `mention` rides engagement, and a route target `#engaged` extends it to other verbs. If routd cannot read the route table, it logs an ERROR and treats the read as a miss, because a retry risks a poison-message loop.
- **The run contract.** `POST /v1/runs` is synchronous. routd waits `RUNED_RUN_TIMEOUT` plus two minutes, so that routd outlasts the kill deadline in runed. With equal deadlines, routd could time out first and feed the same turn to a second guest.

  | runed answers                                      | routd does                                                                              |
  | -------------------------------------------------- | --------------------------------------------------------------------------------------- |
  | `ok`                                               | advances the cursor and the observed cursor, ends the turn                              |
  | `silent`                                           | does the same and counts no output                                                      |
  | `ok` with no result and no bot row                 | tells the chat and never retries, because a clean run that delivered nothing is a fault |
  | `error`, no reply, not `terminal`                  | retries, up to `MAX_TURN_RETRY`                                                         |
  | `error` with a reply, retries spent, or `terminal` | marks the rows errored, sends one notice to every chat the run owed, advances           |
  | `busy`                                             | does not advance, does not charge the breaker, applies the busy backoff                 |
  | `steered`                                          | ends this turn, because the live run owns the batch                                     |
  | a transport failure                                | does not advance; the next poll feeds the batch again                                   |

- **Busy backoff.** After a busy refusal, routd parks the batch for 4 s. Each repeat refusal of the same batch doubles the wait up to 60 s (`dispatch.go`). A new user message clears the wait. The running turn's own bot rows do not clear it. A retry after a failed turn returns through the same busy branch, so it waits on this backoff too. A transport failure and a dependency outage do not wait: the next poll feeds the batch again.
- **Terminal.** runed decides whether a failure is terminal. routd never parses runed's text to decide. A terminal notice names the cause: the agent's own reported failure, otherwise the error text from runed.
- **Retry count.** `MAX_TURN_RETRY` set to 0 means 3 retries, and -1 disables retries (`loop.go`). `turn_context.retry_count` and `state='pending_retry'` survive a routd restart. A retry turn starts with a note that it is a retry.
- **Turn lifecycle.** `submit_turn` is hidden from `tools/list` and recorded once. The `turn_results` key removes duplicates, so the first terminal signal wins the cursor and the delivery. A `running` turn older than the run timeout moves to `expired`, never `done`. Crash recovery looks for `running`, and the guard against a second live run looks for `done`.
- **One turn per folder.** A new inbound message for a busy folder waits. runed answers `busy`, and routd keeps the batch. After the run ends, routd feeds the batch again with its own turn context and MCP binding. The observed cursor is at-least-once. It advances on the same success path as the message cursor. So a turn that routd feeds again sees the context of the failed attempt.
- **Outbound.** The pending bot row is the outbox. `chanreg` and the adapters hold no queue. A sweep every 30 s sends `pending` rows again, and each row waits its own gap. The gap starts at 30 s and doubles per failure up to a cap of 1 h. A flat 30 s gap would spend ten attempts in five minutes, and a routine adapter restart lasts longer. A 4xx answer dead-letters the row at once with `delivery_attempts` 0. A transient failure spends one attempt, up to `MAX_OUTBOUND_RETRY`. An age rule would discard rows that the sweep never tried, so routd uses no age bound.
- **A failed send writes its row.** The MCP tools `send`, `reply` and `send_file` return the error to the agent and record the undelivered row. The row is `pending` after a transient failure and `failed` after a permanent one. A failed `send_file` always dead-letters (`recordOutboundFailure` in `ipc/ipc.go`).
- **Delivery dedup belongs to the adapter.** `chanlib/README.md` section "Delivery is claimed before the platform call" describes it.
- **A row names its source and its writer.** `messages.source` holds the adapter of record that ingest carries. An outbound reply reads it with `LatestSource(jid)`, so a reply leaves through the bot that received the message. A turn's delivery rows and its progress row take an `out-<hex>` id (`outboundRow` in `turns.go`). An MCP send takes `mcp-<unix nanos>` (`core.MsgID`). Inbound polling filters bot rows.
- **A proactive turn starts from a synthetic row.** The scanner in `proactive.go` appends an inbound row with sender `timed-proactive` (`store.ProactiveSelfSender`). That row never resets the chat's silence clock, because the trigger must not feed itself. The `timed-` prefix puts the turn on the path of every task turn: it renews no engagement, runs no slash command and carries no caller sub.

## Prompt assembly

<!-- twin:prompt -->

```
routd  buildAgentPrompt, routd/prompt.go:26 ───► RunRequest.MessageBatch
       <system-note> <system> <autocalls> <persona> <rule> <topic>
       <surface> <pane-context> <link-context> <messages>
         │
         │ POST /v1/runs
         ▼
runed  preparePayload, runed/render.go:177 ────► the guest's stdin prompt
       [pending migration]  Topic session:  <episodes>
       <knowledge layer="diary">  <knowledge layer="work">  <user>
       [solve], then routd's string unchanged
         │
         │ vsock, guest-init, stdin
         ▼
ant    buildSystemPrompt, ant/src/index.ts:49 ─► the system prompt
       SYSTEM.md, ant/CLAUDE.md for codex and pi, PERSONA.md,
       the output style
```

<!-- /twin -->

Three processes build what the agent reads, and each builds the part only it holds. routd holds the messages, the routes and the session table, so it builds the message string of the turn. runed opens the group folder at spawn, so it adds the knowledge layers that it reads from that folder. ant runs inside the guest and renders the system prompt from the files in its home. Every block has an XML shape, routd writes none of them to `messages`, and each process computes them again on the next turn.

| Block                                                 | Built in                                                          | When it is emitted                                                                                                                                                                                                                          |
| ----------------------------------------------------- | ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `<system-note>`                                       | `routd/dispatch.go` `retryNoteBlock`                              | on a retry attempt                                                                                                                                                                                                                          |
| `<system origin event>`                               | `routd/prompt_db.go` `FlushSysMsgs`                               | for each queued event: `new_day` when the cursor of the chat is from an earlier day, `new_session` with a `<previous_session>` tag when the folder has no session (`emitSystemEvents`). The flush drains exactly what it read, at most once |
| `<autocalls>`                                         | `routd/prompt.go` `autocallsBlock`                                | every turn: `now`, `instance`, `folder` and `session`, with the session id cut to 8 characters. An empty value drops its line                                                                                                               |
| `<persona name>`                                      | `routd/prompt.go` `personaBlock`                                  | when the `PERSONA.md` frontmatter has a `summary`                                                                                                                                                                                           |
| `<rule>`                                              | `routd/prompt.go` `buildAgentPrompt`                              | when the turn carries observed rows                                                                                                                                                                                                         |
| `<topic name>`                                        | `routd/prompt.go` `buildAgentPrompt`                              | every turn; the main topic has the empty name                                                                                                                                                                                               |
| `<surface>`, `<pane-context>`                         | `routd/prompt.go` `paneHints`                                     | when the trigger came from an open Slack assistant pane                                                                                                                                                                                     |
| `<link-context>`                                      | `routd/prompt.go` `linkContextBlock`                              | when a route token carried the context of its issuer; the newest wins                                                                                                                                                                       |
| `<messages>`, `<message>`, `<observed>`, `<reply-to>` | `router/router.go` `FormatMessages`                               | every turn: the trigger and the observed rows, ordered by time                                                                                                                                                                              |
| `<attachment>`                                        | `routd/enrich.go` `enrichAttachments`                             | inside a message body, written at ingest                                                                                                                                                                                                    |
| `[pending migration]`                                 | `runed/render.go` `preparePayload`                                | when the skills version of the group is behind the version of the image                                                                                                                                                                     |
| `Topic session:`                                      | `runed/render.go` `preparePayload`                                | on a named topic                                                                                                                                                                                                                            |
| `<episodes>`                                          | `container/episodes.go` `ReadRecentEpisodes`                      | when `~/episodes/` holds files with `type:` and `summary:`; the newest 3 per type                                                                                                                                                           |
| `<knowledge layer="diary">`                           | `diary/diary.go` `Read`, called from `preparePayload`             | when `~/diary/` holds `.md` files; the newest 14 by name                                                                                                                                                                                    |
| `<knowledge layer="work">`                            | `runed/render.go` `preparePayload`                                | when `~/work.md` is not empty; the whole file                                                                                                                                                                                               |
| `<user>`                                              | `router/router.go` `UserContextXml`, called from `preparePayload` | when the trigger has a sender other than `system`                                                                                                                                                                                           |
| `[solve]`                                             | `runed/render.go` `preparePayload`                                | every turn                                                                                                                                                                                                                                  |

`<user id name memory="~/users/<id>.md"/>` is a pointer and holds no content. It names the sender and the file, and the agent decides whether to read the file. The file lives in the group folder, so what the agent knows about a person is per folder. The `acl_membership` rows bind the identities of one person across channels, which is a separate concern.

The trigger drops bot rows (`dispatch.go`). The observed window keeps the folder's own replies, from every topic, rendered `bot="true"`, so a topic sees what a sibling topic already answered; it excludes the bot rows of a watched source. Where history is returned on purpose, through `inspect_messages`, `get_thread` and `fetch_history`, a bot row carries `bot="true"`. The attribute reads `is_bot_message`, never `is_from_me`, because the from-me rows of an imported archive belong to the linked account, that is, to the operator.

`<surface>slack-pane</surface>` is the only `<surface>` tag that routd emits, and it belongs to the Slack pane. The output style sets the length and format rules per surface: `ant/output-styles/README.md`.

To add a block, emit it from the builder that holds its input, keep it XML-shaped and per-turn, and add its row to the table. An autocall is the cheapest block: `EXTENDING.md` section "Adding an autocall".

## Ranked message search

The index is an in-memory BM25 index over the `messages` entity (`msgsearch/`). The store is the record, and the index is derived from it.

- **Built at boot.** The process that answers searches, which is `main` of routd, calls `StartSearch`. It walks every chat into the index in the background. A search before the walk ends fails with `index_warming`. It never returns an empty hit list, because an agent would repeat "no message says that" as fact.
- **Fed by four writes.** routd is the only appender, so the feed is one line beside each write that touches an indexed attribute. The four writes are the append, the media rewrite, the observe stamp and the prune by the breaker. Each runs after the store write commits. A write by another process appears at the next boot walk. Such writes are `arizuko send`, an archive import and a group delete.
- **Tokens.** A term is a run of Unicode letters and digits in lower case, with diacritics dropped after NFD. So `uroven` finds `úroveň` from a plain keyboard. Each token keeps its byte offsets, so the snippet marks the hit in the bytes that the writer wrote.
- **Rank and snippet.** BM25 uses the constants of SQLite FTS5, `k1` 1.2 and `b` 0.75, negated as FTS5 negates them. Ascending rank therefore lists the best hit first (`parity_test.go`). A hit returns a window of at most 32 tokens around the first match, with each hit inside `«` and `»`. The default is 20 hits and the maximum is 200.
- **Grammar.** Terms are all required. `"phrases"` match adjacent terms in order. A `-` before a term or phrase excludes it. The grammar has no OR, no prefix and no NEAR. A malformed query is an error and never an empty list.
- **Scope.** A `scope` that holds `:` names a chat. Any other scope names a folder subtree over `routed_to`, tested with `groupfolder.Contains` and never with `LIKE`. The caller filters the hits to the folders it may read.

## The model gateway

When `MODEL_GATEWAY_URL` is set, the gateway holds the model credential and keeps the spend ledger. routd stores no spend. The `litellm` package provides the gateway. `gateway.go` and `budget.go` map the arizuko nouns onto the gateway nouns:

| arizuko                  | gateway                                              | why                                                                                                                               |
| ------------------------ | ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| folder                   | `team_id`, verbatim                                  | the id is the folder, so no folder-to-team row exists that could drift from a wiped gateway                                       |
| `cost_cap_cents_per_day` | team `max_budget`, `budget_duration "1d"`            | the meter enforces the cap on the request                                                                                         |
| person                   | internal `user_id`, set by routd at mint             | the guest writes the body `user` field and the `x-litellm-end-user-id` header, so the spender would choose whose budget it spends |
| turn                     | one key: TTL = the run ceiling, no budget of its own | deleting the key revokes it; `turn_context.gateway_key_hash` answers `GET /v1/cost?turn_id=`                                      |

- **One `GET /team/info` per turn.** The gate before spawn creates the team when the gateway has none. A key that names an unknown team is accepted, and then every model call answers 404. The same call sets the team budget to the cap of the operator and reads the spend that the refusal quotes.
- **The gate runs when `COST_CAPS_ENABLED` is off.** The gate then reports both caps as uncapped and clears them at the gateway. Otherwise the gateway would keep refusing turns that arizuko does not cap.
- **A spent cap refuses the next turn.** The refusal goes to the chat and consumes the batch. There is no budget notice during a turn. An over-cap turn runs to its end, and the gate of the next turn refuses. A call outside the gateway is a log line and never a row.
- **A gateway failure is a dependency outage.** When a gate read or a key mint fails, routd returns `queue.ErrDependencyDown` (`dispatch.go`). No guest starts, routd feeds the batch again, and the breaker streak of the folder stays as it was. A turn never runs without a meter. A failed spend read is an error and never an empty spend.
- **One limit.** A gateway drops the model key of a user, with a warning in the runed log, because nothing translates it into a gateway key. BUGS `L404` tracks this limit.
- **No gateway means no meter.** The dashboard says so, and shows no number that the agent chose. A cap on such an instance is enforced by nothing, and the gate logs that fact on every turn.

The package ships its own Postgres (`litellm_db`) for keys and spend. It holds the bookkeeping of the gateway and not the store of the instance. Every operator surface reads spend through `/v1/cost/summary`.

## Tables routd owns

routd is the `routd` owner. Each table below is an objstore entity. routd creates all of them at boot, because `Open` calls `store.PrepareOwner`.

- Messages: `messages`, the four read-shaped indexes that an append fans out into (`message_folders`, `message_folder_counts`, `message_activity`, `message_delivery`), `chats`, `chat_reply_state` and `chat_proactive`. Ranked search keeps no table, because the index lives in memory (`msgsearch/`).
- Routing: `routes`, `web_routes` and `route_tokens`. A `route_tokens` row has `kind='route'` for a delivery bearer or `kind='pair'` for an identity-pairing link.
- Orchestration: `turn_context`, `turn_results`, `sessions`, `system_messages`, `group_watchers` and `groups`.
- Authorization and secrets: `acl`, `acl_membership`, `secrets` and `secret_use_log`.
- Scheduler: `scheduled_tasks` and `task_run_logs`.
- Slack pane: `pane_sessions`.
- Held tool calls: `pending_actions`.
- Webhook and file sources: `github_sources` and `gdrive_files`.
- The route table of proxyd: `proxyd_routes`. proxyd makes every write to it and serves it on its own `/v1`.
- Counters: `counters` holds one server-minted number, `routes.id`.
- People: `user_profiles` holds the username that names the folder of a person, and the per-day spend cap of that person. authd owns identity. The model gateway owns spend.
- Network: `network_rules`.
- Packages: `installed_packages`, keyed by folder and name, where `''` means instance-wide. The CLI writes optional receipts; startup and group creation complete the required core receipt. Both faces only read it, and both require the scope `**` for the read (`installedPackagesScope` in `packages_resource.go`). The folder column does not narrow the read, because one record names identities in several folders. A token bound to its own subtree gets 403.
- Audit: `audit_log`, created through `audit.EnsureEntities`.

routd reads no table of another owner. Data of other daemons arrives over HTTP: identity from authd and `session_log` from runed.

## What the REST face serves

`/openapi.json` comes from the mux that routd mounts (`resreg.OpenAPIHandler` in `cmd/routd/main.go`), so the document lists what routd serves and nothing else. `resreg/README.md` explains the emission.

- `secrets` has write-only endpoints, create and key-delete, and no read. So `enc_value` cannot leak through a `GET`.
- `installed_packages` serves only its two `GET` endpoints. Install, upgrade and remove write host files and restart sidecars, so they stay in the `arizuko packages` CLI.
- `scheduled_tasks` is the one resource whose REST path is not `/v1/<name>`. It serves `/v1/tasks`, because timed calls that prefix across the container boundary.
- An agent-only action is declared `MCPOnly` on the single `Endpoints` slice of its resource. Mounting and emission read the same list, so the document cannot advertise an endpoint that routd does not serve. The `MCPOnly` actions are `schedule`, `pause` and `resume` of `scheduled_tasks`, `issue_pair` of `route_tokens`, `list` of `acl` with the tool `list_acl`, and `register` of `groups` with the tool `register_group`. Group creation stays in `SetupGroup` of dashd.
- `network_rules` has no REST face, because all three of its actions are `MCPOnly`.
- `acl_membership` has one pair of faces, `unpair` over MCP and `DELETE /v1/acl_membership` over REST. Both limit the change to rows with `added_by='pairing'`. No endpoint writes the edge. It appears only when a human confirms a pairing link in a browser.

## Endpoints

The binary is `routd/cmd/routd/main.go`. It listens on `LISTEN_ADDR`, default `:8080`. The endpoints are in `server.go` and `channels.go`, and each resource file mounts its own. The scope in brackets is the one that the endpoint requires.

Inbound and outbound:

- `POST /v1/messages` accepts an inbound message from an adapter [`messages:write`].
- `POST /v1/outbound` is the passthrough for adapters [`messages:write`].
- `POST /v1/channels/register`, `POST /v1/channels/deregister` and `GET /v1/channels` manage the adapter registry.
- `POST /v1/pane` sets the Slack pane context [`messages:write`].
- `GET /v1/messages/{inspect,thread,find}` read messages.
- `GET /v1/messages/volume` and `GET /v1/messages/activity` serve the operator cockpit. They are REST-only on purpose, because a resreg resource would become an agent tool.

Routing:

- `GET /v1/routes` lists routes, `GET /v1/routes/{id}` reads one, `POST /v1/routes` adds one, `PUT /v1/routes` replaces the set, and `DELETE /v1/routes/{id}` removes one.
- `GET|PUT|DELETE /v1/web_routes` manage web routes, `GET /v1/web_routes/owner` names the owner of a path, and `GET /v1/web_presence` returns the vhost information of a folder. It is the REST twin of `get_web_presence`.
- `GET /v1/routing/{resolve,errored}` return routing information.
- `GET /v1/routing/answering?since=` tells whether agents answer people over a window. The default window is 7 days. It returns the turn outcomes by state. It also returns every chat whose newest human message is newer than its newest agent reply. Each waiting chat carries the verdict of the loop.
  - The verdict `route` means a turn was due, so the wait is a fault.
  - The verdicts `observe` and `held` mean that the configuration of the operator makes the chat answer nothing. An example is a `verb=mention` rule and a plain message.
  - The response is an aggregate and holds no message content. A folder-scoped bearer sees its own subtree and no unclaimed chat.
- `GET /v1/routing/unrouted` lists the held chats, newest activity first. routd derives the set and never stores it. It passes the latest inbound message of each chat through the `resolve` of the loop. So the list cannot disagree with the drop it describes, and no marker column exists. A failed read of the route table returns 500 and never "everything unrouted". The endpoint is a plain read beside `errored` and not a resreg resource, so it can never become an agent tool. A held chat belongs to no folder, so only an unscoped bearer may call it. A folder-scoped token gets 403, because an empty list would read as "nothing is arriving". The code is in `unrouted.go`.
- `GET|POST /v1/engagement` manages engagement state. `GET` with `jid` reads one pair: `folder`, `engaged_until` and `last_reply_id`. `GET` without `jid` lists the live windows in the subtree of the caller, newest deadline first. Only an empty folder claim, from root or from a service, widens the list to the whole fleet. `POST` engages, and `ttl_seconds <= 0` disengages. The write is contained on the claiming folder of the live window. The `folder` in the body comes from the caller and does not widen the write. So a write can never reach a window that the list would not show. The audit row `engagement.set` or `engagement.clear` lands in the transaction of the write, in `DB.SetEngagementAudited`. A disengagement survives the final answer of the current turn and implicit thread mentions. A fresh mention or an explicit engagement can start another task.
- `GET /v1/topic_sessions` returns the Claude session id to resume for a folder and topic.
- `GET /v1/sessions/counts` and `GET /v1/principals` serve the operator cockpit.

Tokens and access:

- `POST /v1/route_tokens/{chat,hook}` mints a chat token or a hook token. The optional `context` holds the processing instructions for that link.
- `GET /v1/route_tokens` lists tokens and `DELETE /v1/route_tokens/{jid}` revokes one. Token delivery has no REST face: webd and proxyd read the tables of routd directly and resolve the URL token in their own process.
- The MCP tool `issue_pairing_link(jid, sender)` takes a chat JID that routes within the caller's folder and an explicit sender. One bounded read must find an inbound message from that sender in that chat. Its `kind='pair'` token binds only that sender. It has no REST twin.
- `DELETE /v1/acl_membership` unpairs the supplied `child` and `parent`. The caller sub must be the `parent`. The agent twin `unpair(jid, sender, parent)` uses the mint's chat containment and inbound sender check. Missing senders and senders without an inbound message in the named chat return 400 naming the chat and sender on both tools.
- `POST /v1/acl` and `DELETE /v1/acl` grant and revoke [`acl:write`].
- `POST /v1/secrets` and `DELETE /v1/secrets/{key}` write and delete secrets [`secrets:write`].
- `GET /v1/users/{sub}/scopes` returns the scope snapshot at login [`grants:read`].

Held calls:

- The chat verdicts `/approve`, `/reject` and `/always` resolve at ingress, including the native approval buttons. They reach a held call while its agent turn waits. The sender needs admin on the folder of the held call.
- `pending_actions` has a REST face: `GET /v1/pending_actions`, `POST /v1/pending_actions/{id}/approve` and `POST /v1/pending_actions/{id}/reject`. It has no face on the agent socket, because an agent that could approve would approve its own call. Both faces resolve through `resolveHoldInto`. The REST gate requires `pending_actions:read` or `pending_actions:write`, and only `service:dashd` holds them.

Tasks:

- `GET /v1/tasks` lists scheduled tasks. `GET`, `PATCH` and `DELETE` on `/v1/tasks/{taskId}` read, change and cancel one. The agent task tools share the handler [`tasks:read`, `tasks:write`]. `schedule_task(targetJid=...)` accepts a registered folder for internal work or a routed external chat. Silent memory reducers target their owner folder.
- `GET /v1/tasks/due` claims due tasks [`tasks:read`].
- `POST /v1/tasks/runlog` and `POST /v1/tasks/{id}/reschedule` record logs and reschedule [`tasks:write`].
- `GET /v1/tasks/runs` and `GET /v1/tasks/{id}/runs` read run logs [`tasks:read`].

Dead letters and cost:

- `GET /v1/dead_letters` lists outbound messages that routd gave up on, with the lost content [`dead_letters:read`]. `POST /v1/dead_letters/{id}/replay` returns one to `pending` for the existing outbound sweep [`dead_letters:write`]. The scope pair is separate from `messages:*`. `messages:write` composes any inbound message, and a replay only moves one composed and failed row back to `pending`. `delivery_attempts` tells the two causes apart. The value 0 means the platform refused the message, and the cap means the retries ran out. The agent twins are `list_dead_letters` and `replay_dead_letter`.
- `GET /v1/cost?turn_id=` returns what the gateway charged to the key of one turn. `GET /v1/cost/summary?folder=&since=` returns what the gateway metered per folder over a window [`cost:read`, `cost:read:own_group`]. Both read the gateway and answer 404 or an explicit "no meter" when the instance has none. Every operator surface uses the summary, dashd's `/dash/usage/` included. The parameter `since` is required, because a summary without a window would report a lifetime total as current. The folder claim of the token narrows the rows.

Other resources and public paths:

- The resources `audit`, `github_sources`, `gdrive_files` and `groups` serve `/v1/<name>` through resreg. `groups` is read-only over REST. `GET /v1/installed_packages` and `GET /v1/installed_packages/{name}` read the package record.
- `GET /dash/routd/` shows the operator overview. `POST /dash/routd/retry` and `POST /dash/routd/replay` are its forms. The proxyd operator identity gates them and not the `/v1` bearer.
- `GET /openapi.json` serves the OpenAPI 3.1 document. It is public and mounted before auth.
- `GET /health` is the healthcheck. It is public.

## Dependencies

- `auth` verifies tokens offline against the authd JWKS. `chanreg` is the channel registry and Deliverer. `ipc` provides `ServeMCP`. The others are `grants`, `groupfolder`, `obs`, `resreg` and `types`.
- At run time routd calls `authd` for the JWKS, `runed` for `POST /v1/runs`, and the channel adapters for ingress and egress.

## Configuration

Core variables:

- `DATA_DIR` is required. routd exits at boot without it. It holds the `groups`, `web`, `tts` and `ipc` directories, `connectors.toml` and `audit-system.jl`.
- `DYNAMODB_URL` names the store endpoint, and `routd.Open` fails without it. The store variables `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `STORE_CA` complete the connection (`objstore/dynstore/env.go`).
- `LISTEN_ADDR` is the HTTP listener. The default is `:8080`.
- `AUTHD_URL` is the JWKS source. When it is unset, routd runs in open mode for local development, without a verifier.
- `AUTHD_SERVICE_KEY` is the exchange key for the `service:routd` ES256 token. When it is unset, routd falls back to `ROUTD_SERVICE_TOKEN`.
- `ROUTD_SERVICE_TOKEN` is a static service token. It is deprecated, and the ES256 exchange is preferred.
- `RUNED_URL` is the runs endpoint of runed. The default is `http://runed:8080`.
- `RUNED_RUN_TIMEOUT` is the kill deadline for a run. The default is 20m. routd waits 2m longer.
- `ONBOD_URL` is the onbod endpoint for the `/invite` and `/gate` commands.
- `WEB_HOST` is the canonical web origin.
- `SECRETS_KEY` is the keyring for secret reads. Without it routd returns the ciphertext.
- `MODEL_GATEWAY_URL` and `MODEL_GATEWAY_KEY` configure the model gateway. routd refuses to start when the URL is set and the key is not.

Behaviour:

- `ASSISTANT_NAME` is the instance identity. The default is `arizuko`.
- `ENGAGEMENT_TTL` is the engagement window of the reply state. The default is 30m, and `0` disables automatic engagement. Engagement is the same on every channel, GitHub included: only a person's `message` or `mention` rides it, and [the trigger policy of gitd](../gitd/README.md#observe-vs-trigger) shows how a route extends it to CI.
- `SESSION_IDLE_EXPIRY` is the session reset threshold. The default is 2d.
- `ARIZUKO_DEFAULT_MODEL` is the fallback model of a group that has none. The default is empty, and the run request then names no model.
- `ARIZUKO_DEFAULT_EFFORT` is the fallback effort. The default is empty.
- `ARIZUKO_DEFAULT_PRODUCT` selects the product that seeds the default group in an empty store.
- `COST_CAPS_ENABLED` turns on the budget gate before spawn. The default is `true`.
- `MAX_CONCURRENT_CONTAINERS` limits concurrent runs in the queue. The default is 5.
- `MAX_TURN_RETRY` is the retry ceiling for a turn that dies without a reply, for example after SIGKILL, OOM or a timeout. The default is 3, `0` means 3, and `-1` disables retries.
- `MAX_OUTBOUND_RETRY` is the number of transient delivery failures that a `pending` bot row absorbs before the sweep dead-letters it to `failed`. The default is 10, and `-1` dead-letters on the first transient failure. A permanent refusal (4xx) dead-letters at once and spends no attempt.
- `OBSERVE_WINDOW_MESSAGES` is the message count of the prompt context. The default is 10.
- `OBSERVE_WINDOW_CHARS` is the character ceiling of the prompt context. The default is 4000.
- `SEND_DISABLED_GROUPS` is a CSV list of folders. routd stores their outbound messages and does not deliver them.
- `SEND_DISABLED_CHANNELS` is a CSV list of JID prefixes. It mutes the egress of those channels.
- `PROACTIVE_ENABLED` is the kill switch of the proactive scanner. The tuning variables are `PROACTIVE_SCAN_INTERVAL` (30s), `PROACTIVE_SILENCE_MIN` (90s), `PROACTIVE_SILENCE_MAX` (12h), `PROACTIVE_COOLDOWN` (24h), `PROACTIVE_BOT_QUIET` (15m) and `PROACTIVE_RECENT_ACTIVITY_MIN` (3).

Media enrichment, download and Whisper:

- `MEDIA_ENABLED` turns on the download and transcription of inbound attachments. The default is `false`.
- `MEDIA_MAX_FILE_BYTES` is the file size cap. The default is 20 MB.
- `WHISPER_BASE_URL` is the Whisper service URL. The default is `http://localhost:8080`.
- `WHISPER_MODEL` is the Whisper model name. The default is `turbo`.
- `VOICE_TRANSCRIPTION_ENABLED` turns voice to text on. The default is `false`.
- `VIDEO_TRANSCRIPTION_ENABLED` turns video to text on. The default is `false`.

Voice synthesis, the `send_voice` MCP tool:

- `TTS_ENABLED` turns `send_voice` on. The default is `false`.
- `TTS_BASE_URL` is the TTS service URL. The default is `http://ttsd:8880`.
- `TTS_VOICE` is the voice id. The default is `af_bella`.
- `TTS_MODEL` is the TTS model. The default is `kokoro`.
- `TTS_TIMEOUT` is the synthesis deadline. The default is 15s.

Vhosts, the `get_web_presence` MCP tool:

- `HOSTING_DOMAIN` is the root domain for derived vhosts.
- `WEB_VHOST_ALIASES` is a CSV list of `host=folder` aliases.

Integrations:

- `ONBOARDING_ENABLED` turns on chat-initiated onboarding. The default is `false`.
- `ONBOARDING_PLATFORMS` is a CSV list of prefixes that allow automatic onboarding.
- `CONNECTORS_TOML` is the path of the MCP connector catalog. The default is `DATA_DIR/connectors.toml`.
- `CONNECTOR_CALL_TIMEOUT_MS` is the call timeout of a connector. The default is 30s.
- `AUDIT_ENABLED` turns on the optional `audit-system.jl` webhook. The default is `false`. The `audit_log` table is always on.

## Health signal

`GET /health` returns 200 once the process is up. A red flag is a turn that stays unclaimed in the loop, or a runed that is unreachable on dispatch.

## Observability

routd logs JSON with slog to stderr and journald. It exports OTLP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set (`obs/README.md`). Every slog event that carries `turn_id` gets a deterministic TraceID, so the collector groups the spans of one turn.

The audit source of truth is the `audit_log` entity in the tables of routd. Every state-changing MCP tool and REST handler adds one row (`audit.WriteFor`) to the batch of its own mutation. The row therefore is the mutation: a refused batch records nothing, and a recorded row happened. `audit-system.jl`, gated by `AUDIT_ENABLED`, is a separate optional observability webhook and is not the source of truth.

## Files

- `cmd/routd/main.go` wires the daemon: config, verifier, loop and channel registry.
- `server.go` holds the HTTP mux and the auth gates (`Handler`, `authz`).
- `channels.go` registers and deregisters adapters and runs the channel health loop.
- `loop.go` is the orchestration loop: poll, claim and dispatch.
- `dispatch.go` dispatches the turn to runed, enriches it and applies the budget gate.
- `turns.go` handles the end of a turn: the check for an explicit delivery, the one shared nudge decision and `submit_turn` recording.
- `progress.go` owns the turn's summary, progress message and `finishTurn`. `placement.go` places each delivery of the turn.
- `deliver.go` is the channel Deliverer, the egress fan-out through `chanreg`.
- `mcp.go` is the in-process per-turn MCP socket (`ServeTurnMCP`, `buildRoutdFns`).
- `prompt.go` and `prompt_db.go` build the message string of the turn, as "Prompt assembly" describes.
- `proactive.go` runs the proactive interjection, and `steer.go` dispatches slash commands.
- `routes_http.go`, `tokens_http.go`, `web_routes_http.go`, `tasks_http.go` and `reads_http.go` serve REST.
- `*_resource.go` define one resreg resource each: `acl`, `audit`, `dead_letters`, `gdrive_files`, `github_sources`, `groups`, `membership`, `network_rules`, `packages`, `route_tokens`, `routes`, `scheduled_tasks`, `secrets` and `web_routes`. One handler serves each. Agent MCP and operator REST use the same handler with a different injected Gate.
- `packages_store.go` delegates `installed_packages` CRUD to `store/packages.go`, which owns the receipt encoding.
- `db.go` holds the writes of the owner, such as `PutMessage`, turn claims and routing, and the boot that dials the endpoint.
- `search.go` and `msgsearch/` implement ranked message search: the index, its boot walk and the four writes that feed it.
- `reads.go` holds the reads of the owner for messages. The reads of chats, sessions and reply state go through `store`.
- `session.go`, `identity.go` and `onbod.go` federate to runed, authd and onbod.
- `connectors.go` loads the MCP connector catalog.
- `enrich.go` downloads inbound media and transcribes it with Whisper.
- `tts.go` synthesizes `send_voice` audio through the TTS service.
- `dash.go` and `dash_reads_http.go` serve `/dash/routd/`: the overview, the retry and dead-letter replay forms, and the reads behind them. They cut message bodies to 80 characters before render, and the same cut applies to the `/v1/messages/activity` preview that dashd reads. The cut is privacy and not layout: an operator traces routing without reading the conversation. The owner makes the cut, so no consumer can widen it.
- `pending_actions.go` and `pending_actions_http.go` hold the queue of held calls.
- `unrouted.go` and `dead_letters_db.go` serve the two holds that an operator must see: a chat that no rule claims, and a message that delivery gave up on.
- `gateway.go` and `budget.go` mint the per-turn virtual key of the model gateway and apply the folder spend gate.
- `surrogate_refresh.go` refreshes surrogate OAuth credentials near expiry.
- `sibling_db.go`, `ext.go`, `network.go` and `tokens.go` are helpers.
