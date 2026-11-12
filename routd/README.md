# routd

routd implements the conversation plane of an arizuko instance, and channel adapters, operator tools and agents use it through REST on port 8080 and one MCP socket per turn. It stores messages, resolves routes, runs the turn loop and serves the tool socket of the agent.

## What routd owns

The cold-tier groups resource exposes `PATCH /v1/groups/{folder}` and the
derived `update_group` MCP tool. Its `config` string contains JSON settings:
`{"harness":{"backend":"codex"}}`. The harness replaces the whole stored
document; an omitted harness leaves it unchanged, and `null` clears it.
Other settings survive. Store writes audit the replacement atomically.
routd carries the value to runed without interpreting backend, model or effort.
The dashboard's group settings use this PATCH after checking the user's admin
authority. REST requires write scope and folder containment; MCP authorizes
`mcp:update_group` on the target folder.

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

ACL mutations, unpair and invite creation also pass `auth.CheckGrant`.
ACL mutations and unpair prove the returned policy version when they write.
An elevated turn bypasses ordinary tool gates. These mutation checks still use
the issuer's authority. `auth/defaults.go` declares the default policy under
the local `acl` rows.

### Progress has one writer

`turn_status.go` is the only code that calls `Typing` or creates or edits a `verb="status"` row. The hidden method `submit_status` receives raw harness snapshots and fallback text. `progress_reducer.go` merges parent checklists under the turn lock; an active checklist owns the text until End. `placement.go` gives every delivery of the turn one destination.

- A channel that advertises `edit` and `delete` gets one `⏳` progress line where the reply will go. An existing thread wins; without one, the reply's own rule applies.
- A channel that lacks either gets no progress line. A credentialed gitd advertises `edit` and `delete`, so GitHub can show a progress comment and the `👀` mark.
- The agent writes no opener, and no progress message starts a thread.
- Persisted message rows keep the edits across a restart.
- The first status row is persisted before Send. Its existing idempotency key lets webd classify the first frame; the retry sweep shares the turn lock with updates and End.
- `placement.go` sends progress, text, files, voice, terminal notices and retries to the same destination.
- A real edit failure goes back to the caller. routd never posts it again as a new message.
- Complete progress text stays below 2,000 UTF-8 bytes. The current task survives overflow with an omitted count. The loop ticker publishes the latest pending update at most once per five seconds; End cancels it.
- Without a checklist, eight completed parent calls or 180 seconds produce one reminder through ant's steering spool. Checklist activation and explicit clearing suppress it. Internal reminders also work on silent turns and channels without progress display.

A progress row confers a reply obligation. Final delivery and last-answer tracking skip it, because a progress row must never count as the agent's reply.

`Typing(on)` fires at dispatch. `Typing(off)` fires when the turn ends in `finishTurn`, the one writer of `done`. `finishTurn` deletes the progress line, marks the row `✓` or `✗` without a platform edit and does nothing at a second end. A line whose first send never landed is dead-lettered so the outbound sweep never posts it. A refused delete is logged and leaves the row as it was. A run that returns without ending its turn clears `Typing` and leaves the line at `⏳`.

## routd fires scheduled tasks

```text
startup / 60-second tick ──► DueTasks ──► ingress ──► run log / reschedule
```

The task ticker runs once at startup and every 60 seconds in `schedule.go`.
It calls `DueTasks`, then uses normal message ingress to enqueue each prompt
at the task's owner folder. `forwarded_from` records the task's chat JID.
It records the enqueue result and reschedules through the owning store.
The ID `sched-<taskID>@<scheduled-time>` keeps repeated fires identical.
A failed one-time enqueue retains its scheduled time for retry.

The router cancellation context stops the loop, and shutdown joins the loop
before closing the store. The router dashboard shows the last tick, firing
count and error. `TestScheduleTasks` and `TestScheduleRetryKeepsMessageIdentity`
in `schedule_test.go` cover firing and retry behavior.

## The turn in routd

`ARCHITECTURE.md` section "Message Flow" shows the whole turn. These rules describe routd's half.

- **One cursor.** routd is the only appender of `messages`, so one cursor, `chats.agent_cursor`, means "what the agent has seen". A second writer would need a second cursor.
- **One process.** routd runs as one process per instance. Two routd processes on one store are not supported. The turn controller (`turn_control.go`) allows one turn per folder and at most `MAX_CONCURRENT_AGENTS` agents, one of them a scheduled task; `agent_cursor` and the saved `turn_context` rows recover a crash.
- **Ingress.** `POST /v1/messages` honours `X-Idempotency-Key` only when the body has no `id`. routd then mints `id=<adapter>-<key>`, so a re-sent event lands on the same row. A body with both returns `400 ambiguous_idempotency` (`server.go`). routd does not claim engagement at ingress, because the folder is unknown until resolution, and a claim with an empty folder would misroute.
- **Resolution.** `ROUTING.md` section "routd resolves a chat in two stages" gives the order. A batch resolves by its newest user row that dispatches on its own, so a mention starts a turn even when a newer row of the batch matches only `?observe`. The other user rows the turn consumes are stored observed under the batch folder before the prompt is built. Only a `message` or `mention` rides engagement, and a route target `?engaged` extends it to other verbs. If routd cannot read the route table, it logs an ERROR and treats the read as a miss, because a retry risks a poison-message loop.
- **The run contract.** `POST /v1/runs` is synchronous. routd waits `RUNED_RUN_TIMEOUT` plus two minutes, so that routd outlasts the kill deadline in runed. With equal deadlines, routd could time out first and feed the same turn to a second guest.

  | Run result or callback                             | routd does                                                                              |
  | -------------------------------------------------- | --------------------------------------------------------------------------------------- |
  | `ok`                                               | advances the cursor and the observed cursor, ends the turn                              |
  | `silent`                                           | does the same and counts no output                                                      |
  | `ok` with no result and no bot row                 | tells the chat and never retries, because a clean run that delivered nothing is a fault |
  | `submit_turn.provider_failure`                     | saves the input, opens the instance breaker and retries no earlier than the reset time  |
  | `error`, no reply, not `terminal`                  | saves the input, opens the instance breaker, retries on the schedule below              |
  | `error` with a reply, retries spent, or `terminal` | edits the turn status to the final cause, advances                                      |
  | timeout                                            | edits the turn status to "Time limit reached", never retries                            |
  | `busy`                                             | does not advance, applies the busy backoff                                              |
  | `steered`                                          | ends this turn, because the live run owns the batch                                     |
  | a transport failure                                | does not advance; the next poll feeds the batch again                                   |

- **Busy backoff.** After a busy refusal, routd parks the batch for 4 s. Each repeat refusal of the same batch doubles the wait up to 60 s (`turn_control.go`). A new user message clears the wait. The running turn's own bot rows do not clear it. Busy spends no retry.
- **Terminal.** runed decides whether a failure is terminal; a run timeout is terminal. routd never parses runed's text to decide. The status names a short cause from a fixed list (`turnFailureCause` in `turn_status.go`); no error prose reaches the chat.
- **Retry schedule.** A failed turn retries three times: 30 s, 2 min, 5 min for chat, 15, 45 and 180 min for a scheduled task (`retryAt`). `MAX_TURN_RETRY` set to 0 means 3, -1 disables retries, and a value above 3 is capped at 3. A waiting retry saves its input keys and due time on `turn_context` (`state='pending_retry'`) and releases the chat cursor, so new input keeps flowing; the poll replays due turns from the saved binding. A retry turn starts with a note that it is a retry.
- **Instance breaker.** A failed execution opens the `turn_control` singleton: no new agent launches until the failed turn's due retry, the one probe, runs without failure. After the probe turn exhausts its retries the breaker stays open one hour, then the oldest waiting turn probes. An intentional stop never opens it.
- **Turn lifecycle.** `submit_turn` is hidden from `tools/list`. A provider refusal records the attempt and holds input without a `turn_results` row or final delivery. For other results, the `turn_results` key removes duplicates, so the first terminal result wins the cursor and delivery. A `running` turn older than the run timeout moves to `expired`, never `done`. Crash recovery looks for `running`, and the guard against a second live run looks for `done`.
- **One turn per folder.** A new inbound message for a busy folder waits. runed answers `busy`, and routd keeps the batch. After the run ends, routd feeds the batch again with its own turn context and MCP binding. The observed cursor is at-least-once. It advances on the same success path as the message cursor. So a turn that routd feeds again sees the context of the failed attempt.
- **Outbound.** The pending bot row is the outbox. `chanreg` holds no outbound queue; the adapter stores deduplication claims. A sweep every 30 s sends `pending` rows again, and each row waits its own gap. The gap starts at 30 s and doubles per failure up to a cap of 1 h. A flat 30 s gap would spend ten attempts in five minutes, and a routine adapter restart lasts longer. A 4xx answer dead-letters the row at once with `delivery_attempts` 0. A transient failure spends one attempt, up to `MAX_OUTBOUND_RETRY`. An age rule would discard rows that the sweep never tried, so routd uses no age bound.
- **A failed send writes its row.** The MCP tools `send`, `reply` and `send_file` return the error to the agent and record the undelivered row. The row is `pending` after a transient failure and `failed` after a permanent one. A failed `send_file` always dead-letters (`recordOutboundFailure` in `ipc/ipc.go`).
- **Delivery dedup belongs to the adapter.** `chanlib/README.md` section "Delivery is claimed before the platform call" describes it.
- **A row names its source and its writer.** `messages.source` holds the adapter of record that ingest carries. An outbound reply reads it with `LatestSource(jid)`, so a reply leaves through the bot that received the message. A turn's status row takes `status-<sha256(turn id, chat)>` (`turn_status.go`), one row per turn and chat. An operator `POST /v1/outbound` row takes `out-<hex>`. An MCP send takes `mcp-<unix nanos>` (`core.MsgID`). Inbound polling filters bot rows.
- **A proactive turn starts from a synthetic row.** The scanner in `proactive.go` appends an inbound row with sender `timed-proactive` (`store.ProactiveSelfSender`). That row never resets the chat's silence clock, because the trigger must not feed itself. The `timed-` prefix puts the turn on the path of every task turn: it renews no engagement, runs no slash command and carries no caller sub.

## Provider refusals pause the instance

`ProviderFailure` comes from an attempt-ending provider error. Its `reason`
is `spend_limit`, `rate_limit`, `authentication` or `outage`. `reset_at` is
optional. Exit 78, assistant text, tool errors and unknown errors alone do
not count.

One guarded batch saves the refused turn for retry no earlier than `reset_at`,
records the reporting folder's `model_pauses` row (reason, attempts,
evidence) and opens the instance breaker with that turn as its probe. Later
callbacks from the refused attempt are closed. The status message says "Model
unavailable" with the retry time. A probe that completes without a refusal
closes the breaker and clears the episode's `model_pauses` rows. The guest
report is untrusted; it is bound to the reporting turn's attempt.

Operators list or clear the folder rows through one audited REST/MCP resource:
`GET /v1/model_pauses` returns each folder's row with its queued input count
and the breaker's probe and next attempt. `POST /v1/model_pauses/clear`
requires the listed `folder`, `opened_at` and the current `probe_attempt_id`
when a probe is bound; a changed episode or attempt returns a conflict. Clear
removes the folder row and leaves the breaker to its probe. The MCP tools are
`list_model_pauses` and `clear_model_pause`. Members and folder-scoped grants
cannot use these controls. The dashboard at `/dash/model-pauses/` and remote
`arizuko model-pauses list|clear` use the same API.

## Prompt assembly

<!-- twin:prompt -->

```
routd  buildAgentPrompt, routd/prompt.go:26 ───► RunRequest.MessageBatch
       <system-note> <system> <autocalls> <persona> <topic>
       <surface> <pane-context> <link-context> <messages>
         │
         │ POST /v1/runs
         ▼
runed  preparePayload, runed/render.go:177 ────► the guest's stdin prompt
       Topic session:  <user id>
       then routd's string unchanged
         │
         │ vsock, guest-init, stdin
         ▼
ant    buildSystemPrompt, ant/src/index.ts:49 ─► the system prompt
       SYSTEM.md, the package manual for codex and pi, PERSONA.md

ant    hooks/prompt_context.py, UserPromptSubmit ► additionalContext
       <knowledge layer="work">  <user-memory>
       <memory scope="folder"> on a new task
```

<!-- /twin -->

Three processes build what the agent reads, and each builds the part only it holds. routd holds the messages, the routes and the session table, so it builds the message string of the turn. runed adds the facts of the spawn: the topic and the sender id. ant runs inside the guest. It renders the system prompt from the files in its home, and its `prompt_context.py` hook adds `~/work.md` and the file of the sender. Every block has an XML shape, routd writes none of them to `messages`, and each process computes them again on the next turn.

| Block                                                 | Built in                                                   | When it is emitted                                                                                                                                                                                                                          |
| ----------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `<system-note>`                                       | `routd/dispatch.go` `retryNoteBlock`                       | on a retry attempt                                                                                                                                                                                                                          |
| `<system origin event>`                               | `routd/prompt_db.go` `FlushSysMsgs`                        | for each queued event: `new_day` when the cursor of the chat is from an earlier day, `new_session` with a `<previous_session>` tag when the folder has no session (`emitSystemEvents`). The flush drains exactly what it read, at most once |
| `<autocalls>`                                         | `routd/prompt.go` `autocallsBlock`                         | every turn: `now`, `instance`, `folder` and `session`, with the session id cut to 8 characters. An empty value drops its line                                                                                                               |
| `<persona name>`                                      | `routd/prompt.go` `personaBlock`                           | when the `PERSONA.md` frontmatter has a `summary`                                                                                                                                                                                           |
| `<topic name>`                                        | `routd/prompt.go` `buildAgentPrompt`                       | every turn; the main topic has the empty name                                                                                                                                                                                               |
| `<surface>`, `<pane-context>`                         | `routd/prompt.go` `paneHints`                              | when the trigger came from an open Slack assistant pane                                                                                                                                                                                     |
| `<link-context>`                                      | `routd/prompt.go` `linkContextBlock`                       | when a route token carried the context of its issuer; the newest wins                                                                                                                                                                       |
| `<messages>`, `<message>`, `<observed>`, `<reply-to>` | `router/router.go` `FormatMessages`                        | every turn: the trigger and the observed rows, ordered by time                                                                                                                                                                              |
| `<attachment>`                                        | `routd/enrich.go` `enrichAttachments`                      | inside a message body, written at ingest                                                                                                                                                                                                    |
| `Topic session:`                                      | `runed/render.go` `preparePayload`                         | on a named topic                                                                                                                                                                                                                            |
| `<user id>`                                           | `router/router.go` `UserTag`, called from `preparePayload` | when the trigger has a sender other than `system`                                                                                                                                                                                           |
| `<knowledge layer="work">`                            | `ant/hooks/prompt_context.py`                              | when `~/work.md` is not empty; the whole file                                                                                                                                                                                               |
| `<user-memory>`                                       | `ant/hooks/prompt_context.py`                              | when `~/users/<id>.md` exists for the `<user id>` of the prompt; the whole file                                                                                                                                                             |
| `<memory scope="folder">`                             | `ant/hooks/prompt_context.py`                              | on the first prompt of a session and the first after an idle gap: the newest `~/diary/` and `~/episodes/` summary of each day, week and month                                                                                               |

`<user id>` names the sender and holds no content. The hook reads `~/users/<id>.md` and adds it as `<user-memory>`. The file lives in the group folder, so what the agent knows about a person is per folder. The `acl_membership` rows bind the identities of one person across channels, which is a separate concern.

The trigger drops bot rows (`dispatch.go`). The observed window keeps the folder's own replies, from every topic, rendered `bot="true"`, so a topic sees what a sibling topic already answered; it excludes the bot rows of a watched source. Where history is returned on purpose, through `inspect_messages`, `get_thread` and `fetch_history`, a bot row carries `bot="true"`. The attribute reads `is_bot_message`, never `is_from_me`, because the from-me rows of an imported archive belong to the linked account, that is, to the operator.

`<surface>slack-pane</surface>` is the only `<surface>` tag that routd emits, and it belongs to the Slack pane. The core skill `ant/skills/self/messages.md` carries channel formatting rules.

To add a block, emit it from the builder that holds its input, keep it XML-shaped and per-turn, and add its row to the table. An autocall is the cheapest block: `EXTENDING.md` section "Adding an autocall".

## Ranked message search

The index is an in-memory BM25 index over the `messages` entity (`msgsearch/`). The store is the record, and the index is derived from it.

- **Built at boot.** The process that answers searches, which is `main` of routd, calls `StartSearch`. It walks every chat into the index in the background. A search before the walk ends fails with `index_warming`. It never returns an empty hit list, because an agent would repeat "no message says that" as fact.
- **Fed by message writes.** routd is the only appender, so the feed is one line beside each write that touches an indexed attribute. The writes are append, media rewrite, observe stamp and explicit message cleanup. Each runs after the store write commits. A write by another process appears at the next boot walk. Such writes are `arizuko send`, an archive import and a group delete.
- **Tokens.** A term is a run of Unicode letters and digits in lower case, with diacritics dropped after NFD. So `uroven` finds `úroveň` from a plain keyboard. Each token keeps its byte offsets, so the snippet marks the hit in the bytes that the writer wrote.
- **Rank and snippet.** BM25 uses the constants of SQLite FTS5, `k1` 1.2 and `b` 0.75, negated as FTS5 negates them. Ascending rank therefore lists the best hit first (`parity_test.go`). A hit returns a window of at most 32 tokens around the first match, with each hit inside `«` and `»`. The default is 20 hits and the maximum is 200.
- **Grammar.** Terms are all required. `"phrases"` match adjacent terms in order. A `-` before a term or phrase excludes it. The grammar has no OR, no prefix and no NEAR. A malformed query is an error and never an empty list.
- **Scope.** A `scope` that holds `:` names a chat. Any other scope names a folder subtree over `routed_to`, tested with `groupfolder.Contains` and never with `LIKE`. The caller filters the hits to the folders it may read.

## The model gateway

When `MODEL_GATEWAY_URL` is set, the gateway holds the model credential and keeps the spend ledger. routd keeps no spend of its own: a turn's self-reported usage is stored, and no cap reads it. The `litellm` package provides the gateway. `gateway.go` and `budget.go` map the arizuko nouns onto the gateway nouns:

| arizuko                  | gateway                                              | why                                                                                                                               |
| ------------------------ | ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| folder                   | `team_id`, verbatim                                  | the id is the folder, so no folder-to-team row exists that could drift from a wiped gateway                                       |
| `cost_cap_cents_per_day` | team `max_budget`, `budget_duration "1d"`            | the meter enforces the cap on the request                                                                                         |
| person                   | internal `user_id`, set by routd at mint             | the guest writes the body `user` field and the `x-litellm-end-user-id` header, so the spender would choose whose budget it spends |
| turn                     | one key: TTL = the run ceiling, no budget of its own | deleting the key revokes it; `turn_context.gateway_key_hash` answers `GET /v1/cost?turn_id=`                                      |

- **One `GET /team/info` per turn.** The gate before spawn creates the team when the gateway has none. A key that names an unknown team is accepted, and then every model call answers 404. The same call sets the team budget to the cap of the operator and reads the spend that the refusal quotes.
- **The gate runs when `COST_CAPS_ENABLED` is off.** The gate then reports both caps as uncapped and clears them at the gateway. Otherwise the gateway would keep refusing turns that arizuko does not cap.
- **A spent cap refuses the next turn.** The refusal goes to the chat and consumes the batch. There is no budget notice during a turn. An over-cap turn runs to its end, and the gate of the next turn refuses. A call outside the gateway shows only in the turn's self-reported usage.
- **A gateway failure is a dependency outage.** When a gate read or a key mint fails, routd returns `ErrDependencyDown` (`dispatch.go`). No guest starts, and routd feeds the batch again. A turn never runs without a meter. A failed spend read is an error and never an empty spend.
- **One limit.** A gateway drops the model key of a user, with a warning in the runed log, because nothing translates it into a gateway key. BUGS `L404` tracks this limit.
- **No gateway means no meter.** The dashboard says so, and shows no number that the agent chose. A cap on such an instance is enforced by nothing, and the gate logs that fact on every turn.

The package ships its own Postgres (`litellm_db`) for keys and spend. It holds the bookkeeping of the gateway and not the store of the instance. Every operator surface reads spend through `/v1/cost/summary`.

## State ownership

routd opens only its own owner. The complete entity map and maintenance policy appear below under Persistent state and maintenance.
Other daemon data arrives over HTTP: identity from authd, session history from runed, onboarding operations from onbod.
proxyd serves runtime proxyd_routes CRUD; compose generation and package installation also write those routd-owned rows.

## What the REST face serves

`/openapi.json` comes from the mux that routd mounts (`resreg.OpenAPIHandler` in `cmd/routd/main.go`), so the document lists what routd serves and nothing else. `resreg/README.md` explains the emission.

- `secrets` has write-only endpoints, create and key-delete, and no read. So `enc_value` cannot leak through a `GET`.
- `model_pauses` is an operator-only runtime resource. Its list and guarded clear share one handler and atomic audit.
- `turn_usage` is an operator-only read of each turn's self-reported usage, one row per model, held on the turn's `turn_results` item. `arizuko usage` sums it per folder or day.
- `installed_packages` serves only its two `GET` endpoints. `arizuko package add`, `rm` and `sync` write the rows.
- `scheduled_tasks` is the one resource whose REST path is not `/v1/<name>`. It serves `/v1/tasks`.
- An agent-only action is declared `MCPOnly` on the single `Endpoints` slice of its resource. Mounting and emission read the same list, so the document cannot advertise an endpoint that routd does not serve. The `MCPOnly` actions are `schedule`, `pause` and `resume` of `scheduled_tasks`, `issue_pair` of `route_tokens`, `list` of `acl` with the tool `list_acl`, and `register` of `groups` with the tool `register_group`. Group creation stays in `SetupGroup` of dashd.
- `network_rules` has no REST face, because all three of its actions are `MCPOnly`.
- `acl_membership` has one pair of faces, `unpair` over MCP and `DELETE /v1/acl_membership` over REST. Both limit the change to rows with `added_by='pairing'`. No endpoint writes the edge. It appears only when a human confirms a pairing link in a browser.

## Other HTTP endpoints

The binary is `routd/cmd/routd/main.go`; LISTEN_ADDR defaults to :8080.
The Resource names and agent tools table lists the resreg endpoints.
These additional mux patterns serve ingress, runtime state, and operator pages:

| Method and path                  | Handler owner |
| -------------------------------- | ------------- |
| `GET /health`                    | routd         |
| `POST /v1/messages`              | routd         |
| `POST /v1/outbound`              | routd         |
| `GET /v1/messages/inspect`       | routd         |
| `GET /v1/messages/thread`        | routd         |
| `GET /v1/messages/find`          | routd         |
| `GET /v1/routing/resolve`        | routd         |
| `GET /v1/routing/errored`        | routd         |
| `GET /v1/routing/unrouted`       | routd         |
| `GET /v1/routing/answering`      | routd         |
| `GET /v1/engagement`             | routd         |
| `POST /v1/engagement`            | routd         |
| `GET /v1/topic_sessions`         | routd         |
| `GET /v1/sessions/counts`        | routd         |
| `GET /v1/messages/volume`        | routd         |
| `GET /v1/messages/activity`      | routd         |
| `GET /v1/principals`             | routd         |
| `GET /v1/users/{sub}/scopes`     | routd         |
| `POST /v1/pane`                  | routd         |
| `GET /v1/tasks/runs`             | routd         |
| `POST /v1/tasks/runlog`          | routd         |
| `GET /v1/tasks/{id}/runs`        | routd         |
| `POST /v1/tasks/{id}/reschedule` | routd         |
| `GET /v1/cost`                   | routd         |
| `GET /v1/cost/summary`           | routd         |
| `GET /dash/routd/`               | routd         |
| `POST /dash/routd/retry`         | routd         |
| `POST /dash/routd/replay`        | routd         |
| `POST /v1/channels/register`     | routd         |
| `POST /v1/channels/deregister`   | routd         |
| `GET /v1/channels`               | routd         |
| `GET /v1/web_routes/owner`       | routd         |
| `GET /v1/web_presence`           | routd         |

Bearer gates bind the required operation scope and target folder. Channel registration admits seeded service principals.
The operator dashboard requires the proxyd-attested operator identity.
`POST /v1/messages` requires messages:write. If the body omits id, X-Idempotency-Key supplies a stable adapter-prefixed id.
Providing both id and the header returns 400 ambiguous_idempotency.
`GET /v1/users/{sub}/scopes` requires grants:read; authd uses this for login and refresh ceilings.
`GET /v1/routing/unrouted` requires an unscoped caller because unclaimed chats belong to no folder.
`GET /v1/routing/answering` defaults to a seven-day window and returns metadata without message bodies.
`POST /v1/engagement` binds changes to the current window's folder; a body folder cannot widen access.
`POST /v1/pane` stores Slack pane context.
Task runlog and reschedule writes require tasks:write; runlog reads require tasks:read.
`GET /v1/cost/summary` requires a since window and reads the model gateway's meter.
Pending-action approval stays off the held agent's MCP socket.

## Dependencies

- `auth` verifies tokens offline against the authd JWKS. `chanreg` is the channel registry and Deliverer. `ipc` provides `ServeMCP`. The others are `grants`, `groupfolder`, `obs`, `resreg` and `types`.
- At run time routd calls `authd` for the JWKS, `runed` for `POST /v1/runs`, and the channel adapters for ingress and egress.

## Configuration

| Variable                                                             | Default                                   | Meaning                                                                                                           |
| -------------------------------------------------------------------- | ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `DATA_DIR`                                                           | required                                  | Instance root; boot refuses an empty value.                                                                       |
| `LISTEN_ADDR`                                                        | `:8080`                                   | HTTP listener.                                                                                                    |
| `AUTHD_URL`                                                          | unset                                     | JWKS source; unset opens local development verifier gates.                                                        |
| `AUTHD_SERVICE_KEY`                                                  | unset                                     | Bootstrap for service:routd; preferred over a static token.                                                       |
| `ROUTD_SERVICE_TOKEN`                                                | unset                                     | Static token used for runed if no exchange source is configured.                                                  |
| `RUNED_URL`                                                          | `http://runed:8080`                       | Execution API.                                                                                                    |
| `RUNED_RUN_TIMEOUT`                                                  | `60m`                                     | Run ceiling; router HTTP wait adds 2 minutes.                                                                     |
| `ONBOD_URL`                                                          | unset                                     | Onboarding, invite, and gate federation endpoint.                                                                 |
| `WEB_HOST`                                                           | unset                                     | Public instance origin.                                                                                           |
| `SECRETS_KEY`                                                        | required                                  | Comma-separated encryption keyring; first seals, all decrypt. Missing key aborts boot.                            |
| `APP_SRC_DIR`                                                        | `HOST_APP_DIR`, then executable directory | Release ant assets; read by core.AppSrcDir.                                                                       |
| `HOST_APP_DIR`                                                       | executable directory                      | Fallback release asset directory.                                                                                 |
| `MODEL_GATEWAY_URL, MODEL_GATEWAY_KEY`                               | unset                                     | Model gateway and master key. A URL without a key aborts boot.                                                    |
| `ASSISTANT_NAME`                                                     | `arizuko`                                 | Instance name in prompts.                                                                                         |
| `MAX_CONCURRENT_AGENTS`                                              | `5`                                       | Live agent admission limit, loaded through core config.                                                           |
| `ENGAGEMENT_TTL`                                                     | `30m`                                     | Conversation engagement duration; zero disables automatic engagement.                                             |
| `SESSION_IDLE_EXPIRY`                                                | `0` → 48 hours                            | Session idle threshold; zero selects loop default.                                                                |
| `ARIZUKO_DEFAULT_PRODUCT`                                            | unset                                     | Default-group product recorded when seeding an empty store.                                                       |
| `COST_CAPS_ENABLED`                                                  | `true`                                    | Literal true enables pre-run cost gate.                                                                           |
| `MAX_TURN_RETRY`                                                     | `3`                                       | Turn retries; zero means 3, negative disables, maximum 3.                                                         |
| `MAX_OUTBOUND_RETRY`                                                 | `10`                                      | Transient delivery attempt cap; negative fails on first transient error.                                          |
| `OBSERVE_WINDOW_MESSAGES`                                            | `10`                                      | Default number of observed messages in a prompt.                                                                  |
| `OBSERVE_WINDOW_CHARS`                                               | `4000`                                    | Default observed-text character ceiling.                                                                          |
| `SEND_DISABLED_GROUPS, SEND_DISABLED_CHANNELS`                       | unset                                     | Comma-separated folder or JID-prefix outbound suppression.                                                        |
| `PROACTIVE_ENABLED`                                                  | false                                     | Enables synthetic unprompted turns.                                                                               |
| `PROACTIVE_SCAN_INTERVAL`                                            | `30s`                                     | Proactive scan interval.                                                                                          |
| `PROACTIVE_SILENCE_MIN`                                              | `90s`                                     | Minimum user silence.                                                                                             |
| `PROACTIVE_SILENCE_MAX`                                              | `12h`                                     | Maximum user silence.                                                                                             |
| `PROACTIVE_COOLDOWN`                                                 | `24h`                                     | Minimum time between proactive turns.                                                                             |
| `PROACTIVE_BOT_QUIET`                                                | `15m`                                     | Required bot silence.                                                                                             |
| `PROACTIVE_RECENT_ACTIVITY_MIN`                                      | `3`                                       | Minimum inbound count in the last hour.                                                                           |
| `MEDIA_ENABLED`                                                      | `false`                                   | Download and enrich inbound media.                                                                                |
| `MEDIA_MAX_FILE_BYTES`                                               | `20971520`                                | Maximum inbound file bytes.                                                                                       |
| `WHISPER_BASE_URL`                                                   | `http://localhost:8080`                   | Whisper API base.                                                                                                 |
| `WHISPER_MODEL`                                                      | `turbo`                                   | Requested transcription model.                                                                                    |
| `VOICE_TRANSCRIPTION_ENABLED, VIDEO_TRANSCRIPTION_ENABLED`           | `false`                                   | Voice and video transcription switches.                                                                           |
| `TTS_ENABLED`                                                        | `false`                                   | Enable send_voice synthesis.                                                                                      |
| `TTS_BASE_URL`                                                       | `http://ttsd:8880`                        | Speech synthesis endpoint.                                                                                        |
| `TTS_VOICE`                                                          | `af_bella`                                | Requested voice.                                                                                                  |
| `TTS_MODEL`                                                          | `kokoro`                                  | Requested speech model.                                                                                           |
| `TTS_TIMEOUT`                                                        | `15s`                                     | Speech synthesis deadline.                                                                                        |
| `HOSTING_DOMAIN, WEB_VHOST_ALIASES`                                  | unset                                     | World-host suffix and comma-separated host=folder aliases.                                                        |
| `ONBOARDING_ENABLED`                                                 | `false`                                   | Enable chat onboarding.                                                                                           |
| `ONBOARDING_PLATFORMS`                                               | unset                                     | Comma-separated allowed platform prefixes.                                                                        |
| `CONNECTORS_TOML`                                                    | `DATA_DIR/connectors.toml`                | Connector definitions loaded at boot.                                                                             |
| `CONNECTOR_CALL_TIMEOUT_MS`                                          | `30000`                                   | Connector timeout in milliseconds.                                                                                |
| `HITL_WAIT`                                                          | `10m`                                     | Maximum wait for a held tool verdict.                                                                             |
| `GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY`                              | unset                                     | App identity and base64 PEM key for turn-scoped installation tokens.                                              |
| `GITHUB_API_URL`                                                     | `https://api.github.com`                  | GitHub App API base.                                                                                              |
| `DYNAMODB_URL`                                                       | required                                  | DynamoDB-protocol store endpoint.                                                                                 |
| `AWS_REGION`                                                         | `us-east-1`                               | Store signing region.                                                                                             |
| `AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY`                           | required                                  | Daemon store identity; both values must be set.                                                                   |
| `STORE_CA`                                                           | unset                                     | Base64 PEM endpoint certificate; unset uses system trust.                                                         |
| `ARIZUKO_INSTANCE`                                                   | unset                                     | Audit and observability instance label.                                                                           |
| `LOG_LEVEL`                                                          | `info`                                    | JSON stderr verbosity: debug, info, warn, error.                                                                  |
| `OTEL_EXPORTER_OTLP_ENDPOINT`                                        | unset                                     | Optional OTLP log export.                                                                                         |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`                                 | unset                                     | Optional OTLP trace export; independent of the log endpoint.                                                      |
| `MAX_CONCURRENT_CONTAINERS`                                          | absent                                    | Shared config rejects this variable whenever it is present; configure MAX_CONCURRENT_AGENTS for execution limits. |
| `METRICS_ENABLED`                                                    | `false`                                   | Literal `true` mounts public GET /metrics.                                                                        |
| `AUDIT_ENABLED`                                                      | `false`                                   | Enable auxiliary audit-system.jl and webhooks; audit_log stays active.                                            |
| `AUDIT_MAX_BYTES`                                                    | `104857600`                               | Auxiliary audit rotation size.                                                                                    |
| `AUDIT_ROTATE_HOURS`                                                 | `24`                                      | Auxiliary audit rotation age.                                                                                     |
| `AUDIT_WEBHOOK_URL, AUDIT_WEBHOOK_URL_SYSTEM, AUDIT_WEBHOOK_URL_WEB` | unset                                     | Auxiliary audit webhook destinations.                                                                             |
| `AUDIT_WEBHOOK_SECRET`                                               | unset                                     | Webhook signing secret.                                                                                           |

`core.LoadConfig` parses shared instance defaults; [core/config.go](../core/config.go) declares them.
Surrogate credentials use dynamic `SURROGATE_<PROVIDER>_CLIENT_ID` and `SURROGATE_<PROVIDER>_CLIENT_SECRET` reads through the provider library.
Compose does not forward every process variable ([BUGS L526 and L490](../BUGS.md)); verify the generated env file.

## Health signal

`GET /health` returns 200 once the process is up. A red flag is a turn that stays unclaimed in the loop, or a runed that is unreachable on dispatch.

## Observability

routd logs JSON with slog to stderr and journald. It exports OTLP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set (`obs/README.md`). Context-aware slog events carry the deterministic turn TraceID; a plain turn_id log attribute alone does not propagate trace context.

The audit source of truth is the `audit_log` entity in the tables of routd. Every state-changing MCP tool and REST handler adds one row (`audit.WriteFor`) to the batch of its own mutation. The row therefore is the mutation: a refused batch records nothing, and a recorded row happened. `audit-system.jl`, gated by `AUDIT_ENABLED`, is a separate optional observability webhook and is not the source of truth.

## Files

- `cmd/routd/main.go` wires the daemon: config, verifier, loop and channel registry.
- `server.go` holds the HTTP mux and the auth gates (`Handler`, `authz`).
- `channels.go` registers and deregisters adapters and runs the channel health loop.
- `loop.go` is the orchestration loop: poll, claim and dispatch.
- `dispatch.go` dispatches the turn to runed, enriches it and applies the budget gate.
- `turns.go` handles the end of a turn: the check for an explicit delivery, the one shared nudge decision and `submit_turn` recording.
- `turn_control.go` owns admission, agent limits, retry deferral, the instance breaker and provider refusals. `model_pauses.go` holds oldest-input selection and restart recovery. `model_pauses_resource.go` serves the operator REST/MCP resource.
- `turn_status.go` owns the turn's summary, progress message and `finishTurn`. `placement.go` places each delivery of the turn.
- `deliver.go` is the channel Deliverer, the egress fan-out through `chanreg`.
- `mcp.go` is the in-process per-turn MCP socket (`ServeTurnMCP`, `buildRoutdFns`).
- `prompt.go` and `prompt_db.go` build the message string of the turn, as "Prompt assembly" describes.
- `proactive.go` runs the proactive interjection, and `steer.go` dispatches slash commands.
- `routes_resource.go`, `tokens_http.go`, `web_routes_resource.go`, `tasks_http.go`, and `reads_http.go` serve REST.
- `*_resource.go` define one resreg resource each: `acl`, `audit`, `chats`, `dead_letters`, `gdrive_files`, `github_sources`, `groups`, `membership`, `network_rules`, `packages`, `route_tokens`, `routes`, `scheduled_tasks`, `secrets` and `web_routes`. One handler serves each. Agent MCP and operator REST use the same handler with a different injected Gate.
- `packages_store.go` implements `installed_packages` CRUD. It is the record that `arizuko package add|rm|sync` reads and writes.
- `db.go` holds the writes of the owner, such as `PutMessage`, turn claims and routing, and the boot that dials the endpoint.
- `search.go` and `msgsearch/` implement ranked message search: the index, its boot walk and the message writes that feed it.
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

## Persistent state and maintenance

`StoreReach["routd"]` reaches only `routd`. routd prepares these entities at boot:

| Entities                                                                                       | What they hold                                                                          |
| ---------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `messages`, `message_folders`, `message_folder_counts`, `message_activity`, `message_delivery` | Message bodies, folder indexes/counts, activity, and outbound delivery state.           |
| `chats`, `sessions`, `chat_reply_state`, `chat_proactive`                                      | Dispatch and observed cursors, topic continuity, engagement, and proactive cooldown.    |
| `groups`, `routes`, `route_tokens`                                                             | Folder configuration, routing precedence, and hashed capability or pairing credentials. |
| `acl`, `acl_membership`                                                                        | Grants and membership edges.                                                            |
| `scheduled_tasks`, `task_run_logs`                                                             | Scheduled prompts, next runs, and execution records.                                    |
| `secrets`, `secret_use_log`, `network_rules`                                                   | Sealed credentials, credential use, and egress rules.                                   |
| `pending_actions`, `turn_context`, `turn_results`, `turn_control`, `model_pauses`              | Held calls, attempts, completion claims, instance breaker, and provider refusals.       |
| `proxyd_routes`, `web_routes`                                                                  | Proxy dispatch and public-tree access rules.                                            |
| `installed_packages`, `github_sources`, `gdrive_files`                                         | Package selection, webhook sources, and Drive bindings.                                 |
| `pane_sessions`, `system_messages`, `user_profiles`, `group_watchers`                          | Slack pane context, queued system events, names/caps, and observation subscriptions.    |
| `counters`, `resreg_scopes`, `audit_log`                                                       | ID allocation, optimistic version markers, and mutation audit.                          |

On disk routd reads `DATA_DIR/groups`, package assets, and connectors; it writes sockets under `DATA_DIR/ipc` and speech files under `DATA_DIR/tts`.
Optional audit files use `audit-system.jl`. There is no routd SQLite database.
The message search index and channel registry are process-local and rebuild at startup.

| Loop                   | Exact cadence and effect                                                                                                                                     |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Loop ticker            | Every 2 s; reads no chat. Flushes due progress; recovers turns every 30 s. Writers wake their own chat, and a startup sweep enqueues chats with unread rows. |
| Scheduled tasks        | Once at startup, then every 60 s; normal ingress and stable fire IDs.                                                                                        |
| Pending outbound       | Every 30 s; reads up to 200 rows, with per-row 30 s exponential backoff capped at 1 hour.                                                                    |
| Stale-turn maintenance | Every hour; expires abandoned running turns and prunes the 30-day activity index.                                                                            |
| Adapter health         | Every 30 s, 10 s HTTP timeout; three consecutive failures deregister the channel. `stale` counts as alive.                                                   |
| Proactive scans        | `PROACTIVE_SCAN_INTERVAL`, default 30 s, evaluated on the main poll.                                                                                         |
| Surrogate refresh      | Runs at connector call time; refreshes credentials within 60 s of expiry, or reactively after 401.                                                           |

Boot recovers running contexts, pending status rows, stranded `firing` tasks, and orphan sockets.
A failed store poll logs and leaves later ticks to retry. A route-table read error acts as a miss and can mark input unrouted.
A runed transport failure leaves input for refeed; a busy response backs off 4 s, doubling to 60 s.
A schedule reschedule failure can leave a task claimed until startup recovery; grep `task not restored to active`.
Grep `claim due tasks`, `retry outbound: list pending`, `execution plane unreachable`, `channel auto-deregistered`, and `recover firing tasks`.
HTTP 200 from `/health` proves process liveness, not store, model, adapter, or execution readiness.

Known limits: one routd process per instance; stop/pending-input semantics ([BUGS L317](../BUGS.md));
upstream thread identity propagation ([BUGS L421](../BUGS.md)); provider-specific env forwarding ([BUGS L490](../BUGS.md)).
Local documentation checks do not run live platform, model-provider, or KVM turns.

Sources: [boot](cmd/routd/main.go), [poll and recovery](loop.go), [scheduler](schedule.go),
[entity declarations](../store/entities.go), [owner reach](../store/owner_reach.go), [channel health](../chanreg/health.go).

## Resource names and agent tools

| Resource             | REST endpoints on routd                                                                                                | Agent MCP tools                                                                                            |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `groups`             | `GET /v1/groups`, `GET /v1/groups/{folder}`, `PATCH /v1/groups/{folder}`                                               | `refresh_groups`, `get_group`, `update_group`; `register_group` is MCP-only.                               |
| `routes`             | `GET /v1/routes`, `POST /v1/routes`, `PUT /v1/routes`, `DELETE /v1/routes/{id}`                                        | `list_routes`, `add_route`, `set_routes`, `delete_route`.                                                  |
| `route_tokens`       | `POST /v1/route_tokens/chat`, `POST /v1/route_tokens/hook`, `GET /v1/route_tokens`, `DELETE /v1/route_tokens/{jid...}` | `issue_chat_link`, `issue_webhook`, `list_tokens`, `revoke_token`; `issue_pairing_link` is MCP-only.       |
| `acl`                | `POST /v1/acl`, `DELETE /v1/acl`                                                                                       | `add_acl`, `remove_acl`, `list_acl`; list is MCP-only.                                                     |
| `acl_membership`     | `DELETE /v1/acl_membership`                                                                                            | `unpair`.                                                                                                  |
| `chats`              | `GET /v1/chats/{jid}`, `PUT /v1/chats/{jid}`                                                                           | `get_chat`, `set_chat`.                                                                                    |
| `scheduled_tasks`    | `GET /v1/tasks`, `GET /v1/tasks/{taskId}`, `PATCH /v1/tasks/{taskId}`, `DELETE /v1/tasks/{taskId}`                     | `list_tasks`, `reschedule_task`, `cancel_task`; `schedule_task`, `pause_task`, `resume_task` are MCP-only. |
| `network_rules`      | No REST mount.                                                                                                         | `network_allow`, `network_deny`, `network_list`.                                                           |
| `secrets`            | `POST /v1/secrets`, `DELETE /v1/secrets/{key}`                                                                         | No derived tools.                                                                                          |
| `web_routes`         | `PUT /v1/web_routes`, `GET /v1/web_routes`, `DELETE /v1/web_routes`                                                    | `set_web_route`, `list_web_routes`, `del_web_route`.                                                       |
| `github_sources`     | `POST /v1/github_sources`, `GET /v1/github_sources`, `DELETE /v1/github_sources/{source...}`                           | `register_github_source`, `list_github_sources`, `revoke_github_source`.                                   |
| `gdrive_files`       | `POST /v1/gdrive_files`, `GET /v1/gdrive_files`, `DELETE /v1/gdrive_files/{file_id}`                                   | `register_gdrive_file`, `list_gdrive_files`, `revoke_gdrive_file`.                                         |
| `installed_packages` | `GET /v1/installed_packages`, `GET /v1/installed_packages/{name}`                                                      | `list_packages`, `get_package`.                                                                            |
| `audit`              | `GET /v1/audit`                                                                                                        | `query_audit`.                                                                                             |
| `dead_letters`       | `GET /v1/dead_letters`, `POST /v1/dead_letters/{id}/replay`                                                            | `list_dead_letters`, `replay_dead_letter`.                                                                 |
| `model_pauses`       | `GET /v1/model_pauses`, `POST /v1/model_pauses/clear`                                                                  | `list_model_pauses`, `clear_model_pause`; operator-only.                                                   |
| `pending_actions`    | `GET /v1/pending_actions`, `POST /v1/pending_actions/{id}/approve`, `POST /v1/pending_actions/{id}/reject`             | Not mounted on the held agent's socket.                                                                    |

The table follows [resreg resource declarations](../resreg/resources/names.go) and the mounts in [server.go](server.go).
Agent tools use the injected turn gate; operator REST uses the handler's registered scope and folder resolver.
A declared catalog tool is not automatically exposed on a turn socket: grants and the mounted surface both apply.

Held-call waiters poll their pending_actions row every 2 s until HITL_WAIT (default 10 minutes).

JWKS keys load lazily during verification through auth.FetchKeys; there is no fixed background JWKS ticker.
Service tokens refresh on demand within 60 s of expiry, with a 10 s exchange timeout ([auth/service.go](../auth/service.go)).
