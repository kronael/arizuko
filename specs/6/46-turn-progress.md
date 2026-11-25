---
status: partial
depends: [45-gdrive-comments-channel]
---

# Turn progress uses one checklist and one temporary message

A task checklist shows the remaining work and the checked results of one turn.
`routd` owns the checklist reducer, rendered text, presence and temporary message.
A supported chat shows one message where the answer will go.
`End` removes that message. Native typing and acknowledgement marks remain separate from the text.

This spec is `partial`. The shared producer, bounded rendering, checklist reminder,
message lifecycle, browser deletion and active replay have code and tests.
The one temporary message works on krons and sloth with `<status>` text.
No checklist has reached a chat: the agents had no checklist tool
(§"Measured on krons and sloth").
D1 and D2 define rendering; D3 is declined.

## Remaining

- Release an agent image that holds the Claude task-tool opt-in (`sessionEnv`, `ant/src/claude.ts`), then run `tests/lab/checklist_adoption.py` on the turns after the deploy and read the store for checklist status rows. Adoption acceptance fails on the turns measured so far.
- Give a Codex turn that ant starts the `update_plan` tool, and measure the hook event name of a plan update (BUGS `L936`; `ant/src/codex.ts`, `routd/progress_reducer.go`).
- Post the failure notice of a GitHub turn on the repository chat (BUGS `L938`; `routd/turn_status.go`).
- Probe whether Drive's `replies.update` changes a reply's `content` (§"Unverified"). The owner must first enable the Drive API in the Cloud project of the service-account key.

## Problem

At `b869bd16a5ee985a07d377b1e680666bfbf1c427`, two senders replace the same text:
the checklist sender (`todoStatus`, the job the `completion` row of
`ant/src/policy.ts` carries) sends a checklist, and `ant/src/index.ts:166`
sends a `<status>` block. The second sender can overwrite the checklist.
The `TodoWrite` matcher in `ant/src/policy.ts:90` misses Claude task tools
and Codex plans. The policy does not exclude child calls.

`ant/src/index.ts:316` requests a status every 180 seconds when display is enabled.
It does not check for a checklist. The policy ignores empty and single-item
lists (`todoStatus`'s filter). Neither sender bounds the text.
`chanlib.Chunk` counts UTF-8 bytes; teled and discd split sends at 4,096 and
2,000 bytes (`teled/bot.go:439`, `discd/bot.go:399`). Only the first ID is retained.

The web server emits deletion events, but the built-in browser has no deletion
subscription (`webd/route_token.go:660`). Turn replay excludes all status rows
(`store/messages.go:789`). The route-token stream uses a separate topic replay
(`webd/route_token.go:442`). History-on-open returns retained ended statuses
through `MessagesByThread` (`webd/route_token.go:135`). Reconnect keeps existing
nodes (`webd/route_token.go:660`). Active-only replay must cover all three paths
and reconcile displayed progress.

## Where it fits

```text
Claude checklist in ant (changed) ─┐
Codex plan hook process (changed) ─┼─► submit_status ─► routd reducer (new)
Parent status and calls (changed) ─┘                   under lockTurn
                                                         │       │
                                              bounded text       reminder decision
                                                         │       │ RPC response
                                                         ▼       ▼
                                           routd/turn_status.go  nudgeProgress
                                                  one message   STEER_SPOOL
                                                         │       │
                                                         ▼       ▼
                                              supported chat    steering hook

ant checklist renderer and independent text suppression (removed)
```

## Module map

```yaml
- path: ant/src/claude.ts (changed)
  job: identifies Claude snapshots and delivers the current reminder while waiting on background agents
  names: [claudeHarness, claudeHooks]
  never: treats Agent, TaskStop, or scheduler jobs as checklist entries
- path: ant/src/policy.ts (changed)
  job: the `completion` row sends unchanged native checklist snapshots through submit_status, observes parent tool completions and preserves the existing steering delivery
  names: [ToolEvent, bindings]
  never: owns progress state, renders progress text or counts child calls
  remove: the Claude-only TodoWrite matcher, `renderTodos` and the minimum-two-items filter
- path: ant/src/index.ts (changed)
  job: sends fallback statuses and the one-shot time trigger through submit_status
  names: [runTurn, statusBlocks]
  never: owns checklist or reminder state
  remove: whole-turn text-set deduplication and repeating status timer
- path: ant/src/spool.ts (changed)
  job: carries the single checklist reminder through the existing steering spool
  names: [NUDGE_TEXT, STEER_SPOOL, nudgeProgress]
  never: starts a turn from a stale reminder
  remove: the request for a standalone status
- path: ant/src/codex.ts (changed)
  job: binds Codex plan and tool-completion hooks to the existing policy command
  names: [codexHooksConfig]
  never: requires the hook process to share ant memory
- path: ant/src/policy-cli.ts (changed)
  job: sends unchanged Codex snapshots through submit_status from each hook process
  never: keeps progress state between calls
- path: ant/src/mcp.ts (changed)
  job: carries reducer inputs and reminder decisions on the existing status RPC
  names: [submitStatus, SubmitStatusPayload, TurnStatusAck, parseStatusAck]
  never: adds another status transport
- path: ipc/ipc.go (changed)
  job: declares the submit_status contract shared with ant
  names: [TurnStatus, TurnStatusAck]
  never: adds a second status method
- path: routd/mcp.go (changed)
  job: reduces submit_status inputs under lockTurn and gates public snapshots
  names: [submitStatus]
  never: trusts checklist labels as authorization
- path: routd/turn_status.go (changed)
  job: owns parent checklist state, bounded rendering, reminder decisions and message lifecycle
  names:
    [
      renderChecklist,
      publishProgress,
      flushProgress,
      updateProgress,
      endProgress,
      finishTurn,
      retryProgress,
    ]
  never: renders a second checklist or fails an answer because deletion failed
- path: routd/progress_reducer.go (new)
  job: normalizes native snapshots and merges each source into the turn's parent checklist
  names: [turnProgress, progressItem, progressEvent, reduceProgress, reminder]
  never: publishes directly or accepts child checklist changes
- path: routd/loop.go (changed)
  job: flushes due progress and serializes its retry sweep with End
  names: [Run, maybeRetryOutbound, retryOutbound]
  never: starts a second progress scheduler
- path: store/messages.go (changed)
  job: reads active progress independently of normal history and replay cursors
  names: [ActiveProgress, MessagesSinceTopic, MessagesByThread, LastStatusRow]
  never: uses the message timestamp alone to decide whether progress changed
- path: store/turn_context.go (changed)
  job: declares the active turn states for progress publication and replay
  names: [IsActiveTurnState]
  never: treats an expired turn as active
- path: webd/turn.go (changed)
  job: replays the latest active progress snapshot on turn reconnect
  names: [frameKind]
  never: restores progress for an ended turn
- path: webd/channel.go (changed)
  job: classifies the first progress frame from its persisted idempotency key
  names: [handleSend]
  never: infers progress kind from message text
- path: webd/hub.go (changed)
  job: filters stale progress through the existing SSE loop
  names: [serveSSE]
  never: starts a second event loop
- path: webd/route_token.go (changed)
  job: filters history to active progress and reconciles browser nodes on reconnect and delete
  names: [handleChatTokenHistory, handleRouteTokenStream]
  never: treats a reconnect cursor as proof that a status snapshot is current
- path: ant/skills/progress/SKILL.md (changed)
  job: requires native checklists for meaningful longer work
  names: [progress]
  never: asks the agent to publish a checklist with reply or send
- path: ant/CLAUDE.md (changed)
  job: routes longer work through progress and treats labels as public output
  never: makes the manual a publication gate
- path: ant/skills/solve/SKILL.md (changed)
  job: applies progress when several meaningful steps remain
  names: [solve]
  never: duplicates checklist counting in solve_nudge.py
```

Tests belong beside the changed modules. The acceptance cases below define their results.
`ant/hooks/solve_nudge.py`, the hidden RPC, native presence, placement, and the
widget SDK's `onDelete` interface remain in place.

## Build state

The following state is checked against `b869bd16a5ee985a07d377b1e680666bfbf1c427`.
The skill and manual rows describe this spec amendment.
The receiver gate is checked at `dda72103a266faaff0a2ae19a1b192907bf164b3`.

| Contract                                                              | State       | Evidence or remaining work                                                                                                                                                                                                                      |
| --------------------------------------------------------------------- | ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Presence, persisted ID, equal-text suppression, edit and End deletion | built       | `routd/turn_status.go`                                                                                                                                                                                                                          |
| Shared acknowledgement mark                                           | built       | `chanlib/mark.go`, `gitd/main.go`                                                                                                                                                                                                               |
| Web edit/delete server and widget SDK callback                        | built       | `webd/channel.go`, `webd/assets/arizuko-client.js`                                                                                                                                                                                              |
| Built-in browser deletion and active reconnect replay                 | built       | `webd/progress_test.go`, `store/turns_containment_test.go`; in Chrome, `webd/browser_test.go` (build tag `browser`)                                                                                                                             |
| One parent-scoped producer across Claude and Codex                    | built       | `routd/progress_reducer_test.go` merges raw snapshots through submit_status                                                                                                                                                                     |
| Bounded rendering, list clearing, coalescing, retry state             | built       | `routd/progress_coalesce_test.go`                                                                                                                                                                                                               |
| Addressed publication gate at receiver                                | built       | `routd/mcp.go` checks addressed, closure, mute and capabilities                                                                                                                                                                                 |
| One reminder at eight calls or 180 seconds                            | built       | `routd/progress_reminder_test.go`, `ant/src/claude.test.ts`                                                                                                                                                                                     |
| Progress skill, manual sentence and solve routing                     | present     | in the live image and in each instance's core package; loaded in 23 Claude turns after 2026-11-11, with no checklist tool to use                                                                                                                |
| Claude checklist tools in the session                                 | built       | `sessionEnv` sets `CLAUDE_CODE_ENABLE_TODO_TOOLS`; `ant/src/claude.test.ts` reads the tools the installed CLI announces; not in an image yet                                                                                                    |
| Codex `update_plan` in a turn that ant starts                         | absent      | BUGS `L936`: 0 of 135 stored sessions list the tool                                                                                                                                                                                             |
| D3 Typing port change                                                 | declined    | no port change                                                                                                                                                                                                                                  |
| GitHub and Drive edit/delete                                          | built       | `gitd/bot.go`, `googd/main.go`, `routd/turn_status_test.go`                                                                                                                                                                                     |
| pi checklist                                                          | unavailable | pi is selectable (`selectHarness`, `ant/src/backend.ts`) and has no native checklist tool: `registerWith` binds the table with an empty turn id, so its `completion` row sends no snapshot and no reminder; pi shows `<status>` fallback blocks |
| Cross-harness acceptance and krons adoption                           | failed      | §"Measured on krons and sloth": 0 of 39 long Claude turns and 0 of 5 long Codex turns made a checklist                                                                                                                                          |

## The declaration

`routd` is the single producer of progress text, per BUGS L677 option (a).
Claude's checklist runs in `ant`; Codex's plan hooks run in separate processes
through `ant/src/policy-cli.ts`. Both send unchanged native snapshots through
the existing `submit_status` method on routd's MCP socket.
The parent-scoped reducer runs under `lockTurn` in `routd/turns.go`.
It normalizes native snapshots, owns checklist state, and renders one snapshot for every sink.

The `completion` row in `ant/src/policy.ts` is a plain event sender: it sends
the unchanged snapshot and renders nothing. `renderTodos` lives in routd. `streamStatuses` in `ant/src/index.ts`
becomes a plain fallback sender without its whole-turn text set.
The existing RPC carries checklist operations, native call IDs and parent scope,
fallback text, and reminder triggers. It returns any reminder decision through
the same call. No status file, second method or shared harness memory is needed.

Contracts 2–4 build on this reducer: bounded rendering and coalescing,
the one-shot reminder, and browser deletion with active replay.
They share its turn state and feed the existing message lifecycle.

| Event                | Source                        | Carries                      |
| -------------------- | ----------------------------- | ---------------------------- |
| `Begin(turn)`        | dispatch, before the run      | reply chat and presence      |
| `Update(turn, text)` | routd's submit_status reducer | the latest rendered snapshot |
| `End(turn, outcome)` | `finishTurn`                  | `done` or `failed`           |

Any nonempty native list activates the checklist.
Successful parent checklist changes are authoritative:

- `TodoWrite` and Codex `update_plan` replace the list.
- Claude `TaskCreate` and `TaskUpdate` change entries by native task ID.
  An update preserves fields it does not change.
  A successful native deletion removes the entry.
- Successful `TaskList` and `TaskGet` results hydrate resumed tasks by ID.
  Task descriptions and metadata never become labels.
- Failed calls and malformed fields change nothing. Validate label, status,
  and optional `activeForm` types before changing state.
- Child plans never replace the parent plan. The parent records a delegated
  job as one milestone. `Agent`, `TaskStop`, and scheduler tasks are not checklist entries.

The reducer retains state across attempts of the same turn and discards it at
turn end. Duplicate events are idempotent. Each native checklist event enters
this reducer once, even when both a hook and a stream observe the call.

Before a checklist exists, parent `<status>` blocks provide a planless fallback.
Once a checklist exists, standalone status blocks cannot replace it.
An explicit clear keeps checklist ownership for the rest of the turn.
It cannot reactivate fallback statuses or leave the old list visible.

Delete the Claude-only matcher.
Compare the latest rendered snapshot for equal-text suppression.
A sequence `A,B,A` must restore `A`.

`routd/placement.go` places progress, text, files, voice, terminal notices and
outbound retries. The progress line goes where the reply will go: an existing
topic wins and the line joins it, never a nested thread. Without a topic, the
reply's own rule applies, so a chat with `thread_replies` roots the line and the
reply in one thread on the trigger message. The progress line never creates a
thread of its own and never lands in the channel's main flow when the turn has
a topic. The chat owns `thread_replies`; an unset preference uses the chat's
group or direct-message default.

`reply` follows the same placement. An explicit text `send` posts in the named
chat's main channel. Retries keep the recorded thread placement and use the
turn's return address for delegated replies.

The agent does not write an opener.

**Presence** is `Typing(on)` from the moment an addressed message waits for its
turn until `End`, on the chat that receives the reply and, at `End`, on every chat
the run owns (`chatsOwnedBy`). A message waits when the queue parks its chat behind
another run (its folder is held, or the run cap is full) and its newest user row is
addressed, by the same test the turn uses; `Typing(on)` then starts at once, and
`Begin` repeats it. The wire is `POST /typing` unchanged. Presence ends with the
turn, not with the first reply: a `reply` tool call mid-turn leaves it on. A run
that is refused Busy keeps it on: the batch re-feeds and the next `End` clears it.
A waiting chat whose cursor advances with no run (the batch is dropped) gets
`Typing(off)` through `chatsOwnedBy`. A run that returns without ending its turn
for another reason (a retry is scheduled, the batch is re-fed, runed is
unreachable) sends `Typing(off)`, because nothing is working then; its progress
message stays `⏳`. What the sink does with it is the sink's declaration
(§"The sink's declaration").

**The progress message** is one `verb=status` row per `(turn, chat)`:

- The first `Update` creates the message. `Begin` creates presence only.
  A turn that reports nothing posts nothing.
- Public updates require an addressed turn and a reply channel with both
  `edit` and `delete`. `ARIZUKO_PROGRESS` controls display, not event submission.
  The bound callback validates the turn ID and drops closed turns before reduction.
  Publication then checks `tc.Addressed`, mute state and the resolved reply capabilities.
  Other updates create no row or message.
- The renderer uses `☑` for completed, `⏳` for current, and `☐` for pending.
  It uses `activeForm` for the current label when present.
  It omits descriptions and metadata and neutralizes mentions and formatting controls.
  Labels are public output; deletion cannot retract a notification or screenshot.
- The complete wire text, including the routd `⏳ ` prefix, stays below
  2,000 UTF-8 bytes. This also stays below the teled chunk limit.
  Preserve the current item and show an omitted-item count when space is short.
  Shorten labels at Unicode boundaries. No snapshot produces a second platform message.
- The first checklist snapshot sends immediately. Later updates retain only
  the latest snapshot and publish at most once per five seconds per turn.
  This interval limits chat noise; it is not a provider quota.
  Equal text does nothing. The row tracks the last successful send or edit.
- After activation, a one-item list replaces the list.
  An empty list replaces the text with `No remaining tasks.`
  The renderer never sends empty wire text or leaves stale tasks visible.
- `End` cancels pending updates and waits for an in-flight update before deletion.
  It deletes the platform message and marks the retained row terminal without a platform edit.
  A repeated `End` after successful deletion does nothing.
  An unsent row is dead-lettered, so a later sweep cannot post ended progress.
- A failed delete logs an error and leaves the row text unchanged.
  `finishTurn` still marks the turn done. Cleanup failure cannot fail the answer.
  Respect transient provider backoff and report exhausted cleanup failures.
- A real edit failure surfaces to the caller. Never repost the snapshot as a new message.

A reconnect returns the latest active status snapshot with its original ID.
This snapshot is independent of the normal message cursor: edits keep the same
ID and timestamp. Ended turns return no status, including rows whose deletion
failed. Apply this rule to turn replay, route-token topic replay, and
history-on-open through `handleChatTokenHistory` and `MessagesByThread`.
On reconnect, the browser reconciles displayed progress against the active
snapshot and removes nodes absent from it, even if the browser missed `delete`.
An empty snapshot clears displayed progress. Reconciliation must not restore
progress that ends during reconnect.
The built-in browser replaces the matching node on update and removes it on `delete`.
The widget SDK exposes `onDelete`; its consumer must remove the node.

The live message id is the newest `verb=status` row's `platform_id`
(`store/messages.go` `LastStatusRow`), so the message survives a routd
restart: `recoverPending` re-enqueues the running turn, the turn reaches
`finishTurn`, and `End` finds the row. A `(turn, chat)` never has two checklist
rows.

`End` fires when the turn ends, not when the reply lands: an agent that
replies through the `reply` tool and then writes its diary is still working,
and the message says so until it is not.

## The sink's declaration

All supported sinks receive the same bounded plain text. Telegram converts the
first send through its Markdown-to-HTML helper; edits use plain text.
The rendering tests must check that labels stay plain and safe on both paths.
The web server, built-in browser and widget SDK support deletion.
Active replay uses the turn lifecycle independently of message cursors.
GitHub and Drive show one status comment or reply. pi shows only `<status>` fallback blocks, because it has no native checklist.

An adapter's capability map says what it renders. routd reads it; it never
probes a platform error to find out.

| Capability    | Meaning under this spec                                                                          |
| ------------- | ------------------------------------------------------------------------------------------------ |
| `typing`      | the sink renders presence: a native indicator, or an acknowledgement mark on the trigger message |
| `edit`        | the progress line is edited in place; the sink edits its own posts                               |
| `delete`      | `End` removes the progress line                                                                  |
| `thread_id`   | the sink places sends in an existing thread                                                      |
| `thread_root` | the sink creates or reuses a thread from a message before sending content                        |
| not both      | the sink renders nothing for progress; routd sends it nothing for progress                       |

**The acknowledgement mark** is a reaction that carries no social meaning —
`👀` on Slack and GitHub. A like, an upvote or a heart is a public statement
and is never used as a mark: bskyd, linkd and reditd advertise `like` and
render no presence. A sink with a native indicator does not add a mark.

| Sink   | Native presence | Mark | `edit`                                       | Begin renders   | Update renders             | End renders                                                |
| ------ | --------------- | ---- | -------------------------------------------- | --------------- | -------------------------- | ---------------------------------------------------------- |
| teled  | yes             | —    | yes, and `delete`                            | typing          | one `⏳` line, edited      | typing off, line deleted                                   |
| discd  | yes             | —    | yes, and `delete`                            | typing          | same                       | same                                                       |
| whapd  | yes             | —    | yes, and `delete`                            | typing          | same                       | same                                                       |
| slakd  | no              | `👀` | yes, and `delete`                            | `👀` on trigger | same                       | `👀` removed, line deleted                                 |
| gitd   | no              | `👀` | yes, and `delete`                            | `👀` on trigger | one `⏳` comment, edited   | `👀` removed, comment deleted                              |
| googd  | no              | none | yes, and `delete`                            | nothing         | one `⏳` reply, edited     | reply deleted                                              |
| reditd | no              | none | yes, no `delete`                             | nothing         | dropped                    | nothing                                                    |
| webd   | no              | none | server edit/delete; browser removal required | nothing         | one status frame, replaced | server emits delete; built-in browser must remove the node |
| bskyd  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |
| linkd  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |
| twitd  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |
| emaid  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |

`typing` is not in webd's map: webd renders no presence, and a declared
capability with a no-op behind it is the drift `CapImplReport` exists to catch,
`typing` being the one verb it does not probe. googd and gitd edit and delete
only the reply or comment they posted (`specs/6/45-gdrive-comments-channel.md`
§"Decision 3", `gitd/README.md` § "Reply").

## Checkpoint and continuation

`RUNED_RUN_TIMEOUT` (`core.RunTimeout`) is the single run ceiling, with a
40-minute default. At half that ceiling, ant sends one checkpoint note.
`ant/src/spool.ts` owns its text. The note directs the agent to use `reply`
to post a short summary of completed and remaining work to the source chat,
then end the turn. The platform continues the work in the same session.
An agent that ignores the note stops at the ceiling and also continues.

After a checkpoint or ceiling stop, routd appends one continuation through
the existing inbound message queue for the same folder, chat and topic.
Pending chats run first. The continuation resumes the topic's session and
directs the agent to continue from its last summary and work notes.
A normal turn that finishes before the checkpoint keeps its usual behavior.

After eight consecutive continuations without a new non-bot message in the
topic, routd stops and posts one line explaining that a new message resumes
the work. This is turn lifecycle plumbing, not a managed resource.

`routd/checkpoint.go` creates a deterministic successor through `SavePendingTurn`
and the existing deferred-turn path. The synthetic input is a bot row; replay
renders it as the continuation instruction with the original caller's authority.
Its saved input cursor does not consume newer user messages. The loop guard
counts stored continuation inputs back to the newest non-bot message in the topic.
It needs no new counter or storage schema.

`ant/src/index.ts` reports native session IDs through `submit_status.session_id`
before terminal output. `routd/mcp.go` persists them under the turn lock.
Continuation replay skips idle expiry. `submit_turn.checkpoint` marks a
cooperative completion; `RunOutcome.timed_out` and routd's deadline error
identify a ceiling stop. `turnControl.complete` schedules continuation before
failure classification, so checkpoint work does not open the failure circuit.
An accepted checkpoint also determines continuation when the run request loses
its response.
Checkpoint parents remain live until the successor is saved, so recovery can
finish scheduling after a restart or failed write. Folder cancellation and
successor creation share a lock; a recorded stop prevents a stale completion
from starting more work.

Isolated scheduled work stores its native session ID on its turn context and
copies it to its continuation. It never reads or replaces the chat's session.
This per-turn `session_id` is the only added storage attribute.

## The nudge

Use one reminder for a turn without a checklist. Trigger it after eight
completed parent tool calls or 180 seconds, whichever comes first.
Seven calls do not trigger it. A checklist suppresses it, including a checklist
that the agent explicitly clears. Recent fallback statuses do not count as a checklist.
Count each completed call once by native call ID; exclude child calls.
The call count and reminder state survive attempts of the same turn.

Reuse `nudgeProgress`, `STEER_SPOOL`, and the existing steering hook.
Use the existing timer as the one-shot time limit. Do not add another timer,
Python hook, blocking guard, or reminder path. A reminder left in the spool
when the turn ends is discarded with the guest. During live background-agent waiting,
the existing poll delivers the current reminder through the same spool and
`renderSteering`, even without another parent tool call.
The live waiting poll reads `drainLines(STEER_SPOOL)`.
`solve_nudge.py` remains the solve entry nudge.

At turn start, `runTurn` checks `submit_status` for a reminder or its remaining
delay. One timeout spans all attempts. routd measures the delay from the turn's
stored start time.

The routd reducer accepts call-count and time triggers through `submit_status`.
The sender delivers its reminder decision through `nudgeProgress` and the spool.
It queues at most one reminder while the turn has no checklist.
It does not depend on public display. GitHub and silent tasks can still use
native checklists internally. Unavailable checklist tools do not cause repeated reminders.

`NUDGE_TEXT` in `ant/src/spool.ts`:

```text
This turn has no checklist. If meaningful work remains, invoke /progress
and track the remaining milestones with the exposed native checklist.
If the answer is ready, deliver it.
```

`ARIZUKO_PROGRESS` remains the public-display fact. routd sets `1` for an
addressed turn whose reply chat supports edit and delete; otherwise it is empty.
`runed/env_contract_test.go` ties the producer to ant's read.
A silent task can plan internally but emits no public progress.

The core package owns `ant/skills/progress/SKILL.md`, `ant/CLAUDE.md`, and
`ant/skills/solve/SKILL.md`. These files require meaningful milestones before
longer work, checked completion, scope updates, and public-safe labels.
The solve rule applies whenever several meaningful steps remain, including a continuation.

## The mark, implemented once

The `👀` rendering is one chanlib helper, `AckMark` (`chanlib/mark.go`), that
slakd and gitd call from their `Typing`. The helper owns the mark's state:

- the message the mark stands on, and the handle the platform needs to remove
  it (GitHub's reaction id; Slack removes by `(channel, ts)` and has none), per
  chat JID;
- persisted in the adapter store (`chanlib.OpenAdapterStore`) as the
  `ack_marks` entity, so an adapter restart finds its marks. No existing entity
  fits: `outbound_dedup` is keyed by idempotency key and never deleted,
  `inbound_pending` is a queue of payloads, `file_urls` is an id-to-URL map
  nothing sweeps, while a mark is keyed by chat, deleted when its turn ends and
  read as a whole at boot;
- a boot sweep (`AckMark.Sweep`) that removes every persisted mark older than
  `chanlib.DefaultTypingMaxTTL`, which clears the residue a crash leaves
  (`gitd/README.md` § "Typing"), and re-arms the backstop of
  a younger one, because its turn may still be running and routd's
  `Typing(off)` will come;
- the same TTL backstop per mark while the process lives.

The platform calls (add, remove) are the adapter's; the helper decides when. A
record is dropped only after the platform confirmed the removal, and a graceful
shutdown removes no mark.

Which message the mark lands on is the one fact the helper cannot read from
`Typing(jid, on)`, which carries no message id (D3 is declined). The helper
therefore records, per chat, the newest inbound with verb `message` or
`mention` at the one site every inbound passes on its way to routd,
`RouterClient.postInbound`: the live delivery and the retention sweep both call
it, so a message replayed from retention is recorded too. `Typing(on)` pins
that message and the adapter's `add` refuses one that cannot carry the mark
(`ErrNoMarkTarget`); `Typing(off)` removes the mark from the pinned message.
Under two inbound messages in one chat the newest wins, while routd holds the
batch's `last.ID`; that guess is what D3 would remove.

## Decisions

### D1 — a progress message is visible content on a pull request and in a document

Three shapes, costed. `N` is the number of distinct statuses a turn reports;
measured 1–2 per GitHub turn on 2026-10-16.

| Shape                                                        | Posts per turn beyond the reply | Residue after the turn        | Code                                                                       |
| ------------------------------------------------------------ | ------------------------------- | ----------------------------- | -------------------------------------------------------------------------- |
| A — before the renderer: every status a fresh post           | `N`, plus duplicates            | `N` comments, each still `⏳` | none                                                                       |
| B — one message, edited, deleted at end                      | 1 when the turn reports, else 0 | none                          | renderer (~200 lines, deletes the five sites); `edit` and `delete` on webd |
| C — artefact channels render the mark only; statuses dropped | 0                               | none                          | B's renderer; gitd and googd do not advertise `edit`                       |

Decided 2026-11-09: **B** on every channel that can edit and delete, gitd and
googd included; only email keeps its two-post exception. B is strictly quieter
than A on every channel. C hid retry and failure status on a pull request or a
document, where a person waits for the answer as on any chat.

What this does not build: a per-folder switch (`progress_messages`, the shape
of `thread_replies`). The capability map decides per channel; a folder that
wants a different answer per channel is a case that has not occurred.

### D2 — terminal form: the line is deleted, never marked

Decided 2026-10-24 (BUGS `L546`), made uniform 2026-11-03 (BUGS `L643`): a
progress message is scaffolding, so `End` deletes it, and a channel that cannot
both edit and delete shows no line. A `⏳` → `✓` edit leaves the scaffolding in
the chat above the answer; a channel that can only edit shows nothing instead.
webd gained `delete` (an SSE `delete` frame the widget SDK turns into
`onDelete`), so every channel that edits also deletes. The cost of deletion is
that the line recording what the agent reported is gone from the chat; the row
in the store keeps it. webd does not replay a status row, so a reload after the
turn does not bring the line back.

### D3 — `Typing` carries the trigger message id

Declined by the owner. The proposal was `Typing(req TypingRequest)` with
`ChatJID`, `On`, `TriggerID`; wire body `{chat_jid, on, trigger_id}`, a port
change to `BotHandler` (root `CLAUDE.md`, "A port is two methods and stays
two"). The case for it, should it come back:

- It carries a fact routd holds to the sink that renders it, and replaces the
  helper's per-chat guess of the newest inbound (`AckMark.note`).
  Reconstructing an identity the system already knows is the bug class `identity_derived_not_read` names.
- Eleven of the fourteen verbs already take a request struct; `Typing`,
  `SendFile` and `SendVoice` are positional. The change makes `Typing` the
  shape its neighbours have, not a new convenience.
- Cost: one signature in `chanlib`, `chanreg`, routd's `Deliverer`, nine Go
  `BotHandler` implementations and the two TypeScript adapters' `/typing`
  handlers, each a one-line edit; the compiler names every missed sink.

The port stays `Typing(jid, on)` and the helper records the trigger candidate at the one chanlib site every inbound
passes (`RouterClient.postInbound`), which covers a message replayed from
retention and keeps the two-messages-in-one-chat guess.

## What this changes in gitd and 6/45

`gitd/README.md` § "Reply" and
`specs/6/45-gdrive-comments-channel.md §"Decision 3 — the thread reply, and
changes to the agent's own replies"` carry the same line:

> The channel creates, edits and reacts to ITS OWN posts in the thread it was
> addressed in. Every change to another party's state — resolve, reopen,
> merge, label, a new thread, the document body — is the connector's, under
> its own grant and hold.

Under that line a progress message, its edits and deletion, and the `👀` mark are channel
plumbing with the authority the reply already has; `comments.update` on a
human's comment and `action=resolve` are not. Concretely:

- gitd: `capsFor` advertises `send_text`, `send_file`, `typing`, `edit` and
  `delete` with the token, so GitHub shows the `👀` mark and one status
  comment. §"Typing indicator" there is the rendering (`👀` through the shared
  helper, the two reaction endpoints) and points here for the mechanism.
- `6/45`: `capsFor` advertises `send_text`, `edit` and `delete` while the
  credential works; `Edit` is `replies.update` on a reply whose id googd
  minted (`gd-r<fileId>.<commentId>.<replyId>`), `{"content"}` only, and
  `Delete` removes that reply. `Typing` stays a no-op and `typing` stays out
  of the map: there is nothing to render. The §"Not built" row "a second
  channel verb" names resolve, reopen, a new thread and the body, not a change
  to the agent's own reply.

## Tier

Hot-tier per-turn output, as `routd/README.md`: no
REST twin, no resreg resource. The mark's persisted state is adapter-local
plumbing in the adapter store, not a managed entity.

## Acceptance

The tests cover the shared producer, rendering, reminders, lifecycle and replay.
Keep the spec `partial` until image build and krons measurements pass.
A lint result does not satisfy these requirements.

| Area                 | Required result                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Native contracts     | Successful fixtures for TodoWrite, Claude TaskCreate/Update/deletion, resumed TaskList/Get, and Codex plans produce the same normalized state. Use the installed native event shapes.                                                                                                                                                                                                                                                                                        |
| Scope and validation | Failed calls, malformed fields, and child calls cannot change public progress. Native call IDs prevent double ingestion.                                                                                                                                                                                                                                                                                                                                                     |
| Producer ownership   | Checklist → status → checklist retains the checklist. Duplicate events are idempotent. Attempts, fresh-session retries, and process retries of the same turn retain state; separate turns do not share state. `A,B,A` restores A.                                                                                                                                                                                                                                            |
| Rendering            | One-item and empty updates replace stale text. Overflow keeps the current item and an omitted count. Unicode, mentions and formatting remain safe. Complete wire text stays below 2,000 UTF-8 bytes.                                                                                                                                                                                                                                                                         |
| Reminder             | Seven completed parent calls do nothing. Eight calls without a checklist queue one reminder. The 180-second trigger uses the same state and delivery. A reminder queued before or during background waiting reaches the agent once through the waiting poll without another parent tool call. Only between turns are stale reminders discarded. A checklist suppresses both triggers. GitHub and silent tasks can plan internally. Missing tools cause no repeated reminder. |
| Message lifecycle    | `A,A,B`, with B outside the coalescing interval, gives one send, one edit and one delete. Rapid changes publish the latest snapshot. End cancels pending edits and waits for in-flight updates.                                                                                                                                                                                                                                                                              |
| Publication gate     | Unaddressed, muted, closed and unsupported turns publish no progress. A forged or mismatched turn ID fails the bound callback.                                                                                                                                                                                                                                                                                                                                               |
| Web clients          | The built-in browser removes the node on delete. Turn replay, topic replay, and history-on-open return only active progress, including a snapshot edited before the cursor. Reload after End shows no progress, including after failed deletion. A disconnect spanning End deletion leaves no stale node after reconnect.                                                                                                                                                    |
| Cleanup              | End deletes once after success. Failed deletion preserves row text, logs an error, and still ends the turn. An outbound send racing End either finishes before deletion or is cancelled; no status appears after End. An unsent status never posts after End.                                                                                                                                                                                                                |
| Sink exceptions      | GitHub and Drive keep one status comment or reply, edited and deleted at End; GitHub removes its eyes mark at End. Email posts first and final status only. pi has no checklist and shows fallback status blocks.                                                                                                                                                                                                                                                            |

Retain the lifecycle checks in `routd/progress_test.go`, `routd/typing_test.go`
and `routd/fixes_test.go`. Retain the mark checks in `chanlib/mark_test.go`,
`gitd/typing_test.go` and `slakd/integration_test.go`.
Check the web server in `webd/edit_test.go` and the built-in browser in
`webd/browser_test.go`. The Go producer and TypeScript reader share a contract test for
`ARIZUKO_PROGRESS` and `submit_status`. Do not add a second wire format.

On krons, measure addressed longer turns on Telegram, Discord and web.
Record the commit, native tools, calls, elapsed time, snapshots, platform IDs,
sends, edits, deletion results, and reconnect results.
Exercise both Claude and Codex. Check GitHub and silent-task internal planning separately.

Measure adoption on a declared sample of closed parent turns.
For comparison, define a long turn as at least 20 parent tool calls and
120 elapsed seconds. Deduplicate copied or forked histories by prompt and tool-use IDs.
Exclude child transcripts and active segments. Report malformed input counts.
Record checklist use, milestone quality, unnecessary checklist use, progress
skill use, time to the first checklist, reminder delivery, and any eligible
turn without a checklist.
Compare the same sampling rules before and after the change.
A missing checklist requires an explained one-shot answer or missing capability;
unexplained omissions fail adoption acceptance. Do not claim fleet-wide adoption from this sample.

`specs/CLAUDE.md` defines shipping completion. Build and lint must pass,
and the packaged skill changes require an agent image build before release.

## Measured on krons and sloth

Read on 2026-11-25 at v0.75.28 from the store, the unit journals and the
stored transcripts. No turn was started for the measurement.

**The image.** The live root file system `rootfs-2de9fa2d6b19` holds
`ant/skills/progress/SKILL.md`, `ant/CLAUDE.md`, `ant/skills/solve/SKILL.md`,
`ant/src` and `ant/hooks` byte for byte as at
`0df2b45a6d85ef3cb7424610651747d86c96d652`. The installed core package of each
instance (`packages/core/skills/progress/SKILL.md`) holds the same skill file,
and the turns load it from `/home/node/.claude/skills/progress`.

**The one message.** Status rows of the turns started since 2026-11-10:

| Instance | Turns | Addressed | Status rows | Chats                                   |
| -------- | ----- | --------- | ----------- | --------------------------------------- |
| krons    | 362   | 135       | 80          | Telegram 79, web 1                      |
| sloth    | 751   | 385       | 258         | Discord 100, GitHub 152, web 5, other 1 |

- No turn has two status rows in one chat.
- 251 rows are `<status>` text that the turn end deleted: the row has phase
  `cleared` and its platform id is removed. No row of an ended turn is left
  in phase `progress`.
- The longest status text is 148 bytes. The most edited row has revision 19.
- No row holds a checklist.
- 52 final notices on the GitHub repository chat were refused by gitd
  (BUGS `L938`); the 10 on a pull-request chat were posted.
- The store keeps the final state of a row, so it does not show the
  five-second interval between edits. `routd/progress_coalesce_test.go`
  covers that. Reconnect is covered by `webd/browser_test.go`.

**Adoption.** `tests/lab/checklist_adoption.py` at
`a5ea66cfb` counts a long turn as 20 parent tool calls and 120 seconds. It
drops child transcripts, copied turns and open turns.

| Instance | Window                   | Harness | Turns | Long | With a checklist | Reminded | Progress skill used |
| -------- | ------------------------ | ------- | ----- | ---- | ---------------- | -------- | ------------------- |
| krons    | 2026-10-20 to 2026-11-10 | Claude  | 881   | 44   | 0                | 0        | 0                   |
| krons    | 2026-11-11 to 2026-11-25 | Claude  | 307   | 17   | 0                | 17       | 10                  |
| krons    | 2026-11-11 to 2026-11-25 | Codex   | 30    | 5    | 0                | 5        | 5                   |
| sloth    | 2026-10-20 to 2026-11-10 | Claude  | 976   | 78   | 0                | 0        | 2                   |
| sloth    | 2026-10-20 to 2026-11-10 | Codex   | 139   | 14   | 0                | 0        | 3                   |
| sloth    | 2026-11-11 to 2026-11-25 | Claude  | 251   | 22   | 0                | 22       | 13                  |
| sloth    | 2026-11-11 to 2026-11-25 | pi      | 471   | 83   | 0                | 0        | 0                   |

The reminder reaches every long Claude and Codex turn, and the agents load
the skill. No turn makes a checklist, because the tool is missing:

- Claude Code 2.1.331 announces 22 tools for `claude-opus-5-5` and
  `claude-fable-5-2`, and none of `TaskCreate`, `TaskUpdate`, `TaskList`,
  `TaskGet` or `TodoWrite`. With `CLAUDE_CODE_ENABLE_TODO_TOOLS=1` it
  announces the four task tools. `sessionEnv` now sets that key; the fix
  reaches an instance with the next agent image.
- A Codex session that ant starts lists no `update_plan` (BUGS `L936`).
- pi has no checklist tool and gets no reminder, as §"Build state" says.

The adoption rule of §"Acceptance" therefore fails for Claude and Codex until
a new image is live and the sample is taken again.

## Unverified

| Claim                                                                                                                             | Probe                                              | If false                                                               |
| --------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------- | ---------------------------------------------------------------------- |
| `replies.update` changes a reply's `content` (the guide says the creator "can … edit … a reply"; the method page lists no fields) | PATCH one reply googd posted, then list the thread | googd withdraws `edit` and `delete`, and Drive renders no status reply |

googd's `Edit` and `Delete` are tested against an HTTP fake
(`googd` `TestEditReply`, `TestDeleteReply`). The live probe has not run: it
waits on the Drive API being enabled in the Cloud project of the
service-account key, which only the owner can do. No instance runs googd.
