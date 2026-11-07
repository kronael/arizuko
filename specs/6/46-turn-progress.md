---
status: partial
depends:
  [
    27-git-channel,
    45-gdrive-comments-channel,
    31-adapter-contract,
    ../5/24-live-tasklist-status,
    ../5/34-channel-protocol,
  ]
---

# Turn progress uses one checklist and one temporary message

A task checklist shows the remaining work and the checked results of one turn.
`routd` owns the checklist reducer, rendered text, presence and temporary message.
A supported chat shows one message where the answer will go.
`End` removes that message. Native typing and acknowledgement marks remain separate from the text.

This spec is `partial`. The shared producer, bounded rendering, checklist reminder,
message lifecycle, browser deletion and active replay have code and tests.
Image and krons acceptance remain unmeasured.
The skill and manual rules are present. Their presence does not prove adoption.
D1 and D2 define rendering; D3 is declined.

## Remaining

- Build the agent image containing the progress skill and native hooks (`Makefile`, `ant/Dockerfile`).
- Measure addressed Claude and Codex turns on krons through Telegram, Discord and web, including platform IDs, coalescing, deletion and reconnect; check GitHub and silent tasks separately (`anteval/cases/anteval.toml`).
- Measure checklist adoption on the declared closed-parent-turn sample, including reminder delivery and explained omissions (`ant/skills/progress/SKILL.md`).

## Problem

At `b869bd16a5ee985a07d377b1e680666bfbf1c427`, two senders replace the same text:
`ant/src/todo-status.ts:57` sends a checklist, and `ant/src/index.ts:166`
sends a `<status>` block. The second sender can overwrite the checklist.
The `TodoWrite` matcher in `ant/src/policy.ts:90` misses Claude task tools
and Codex plans. The policy does not exclude child calls.

`ant/src/index.ts:316` requests a status every 180 seconds when display is enabled.
It does not check for a checklist. The policy ignores empty and single-item
lists (`ant/src/todo-status.ts:54`). Neither sender bounds the text.
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
                                              routd/progress.go  nudgeProgress
                                                  one message   STEER_SPOOL
                                                         │       │
                                                         ▼       ▼
                                              supported chat    steering hook

ant checklist renderer and independent text suppression (removed)
```

## Module map

```yaml
- path: ant/src/todo-status.ts (changed)
  job: sends unchanged native checklist snapshots through submit_status
  names: [todoStatus]
  never: owns checklist state or renders progress text
  remove: renderTodos and the minimum-two-items filter
- path: ant/src/claude.ts (changed)
  job: identifies Claude snapshots and delivers the current reminder while waiting on background agents
  names: [claudeHarness, claudeHooks]
  never: treats Agent, TaskStop, or scheduler jobs as checklist entries
- path: ant/src/policy.ts (changed)
  job: observes parent tool completions and preserves the existing steering delivery
  names: [ToolEvent, bindings]
  never: owns progress state or counts child calls
  remove: the Claude-only TodoWrite matcher
- path: ant/src/index.ts (changed)
  job: sends fallback statuses and the one-shot time trigger through submit_status
  names: [runTurn, statusBlocks]
  never: owns checklist or reminder state
  remove: whole-turn text-set deduplication and repeating status timer
- path: ant/src/spool.ts (changed)
  job: carries the single checklist reminder through the existing steering spool
  names: [NUDGE_TEXT, STEER_SPOOL, drainSteering, nudgeProgress]
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
- path: routd/progress.go (changed)
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

| Contract                                                              | State        | Evidence or remaining work                                                  |
| --------------------------------------------------------------------- | ------------ | --------------------------------------------------------------------------- |
| Presence, persisted ID, equal-text suppression, edit and End deletion | built        | `routd/progress.go`                                                         |
| Shared acknowledgement mark and GitHub exception                      | built        | `chanlib/mark.go`, `gitd/main.go`                                           |
| Web edit/delete server and widget SDK callback                        | built        | `webd/channel.go`, `webd/assets/arizuko-client.js`                          |
| Built-in browser deletion and active reconnect replay                 | built        | `webd/progress_test.go`, `store/turns_containment_test.go`                  |
| One parent-scoped producer across Claude and Codex                    | built        | `routd/progress_reducer_test.go` merges raw snapshots through submit_status |
| Bounded rendering, list clearing, coalescing, retry state             | built        | `routd/progress_coalesce_test.go`                                           |
| Addressed publication gate at receiver                                | built        | `routd/mcp.go` checks addressed, closure, mute and capabilities             |
| One reminder at eight calls or 180 seconds                            | built        | `routd/progress_reminder_test.go`, `ant/src/claude.test.ts`                 |
| Progress skill, manual sentence and solve routing                     | present      | adoption requires measurement                                               |
| D3 Typing port change                                                 | declined     | no port change                                                              |
| Drive edit/delete                                                     | not built    | §"Unverified"                                                               |
| pi checklist                                                          | unavailable  | backend selection rejects pi until its MCP bridge ships                     |
| Cross-harness acceptance and krons adoption                           | not measured | §"Acceptance"                                                               |

## The declaration

`routd` is the single producer of progress text, per BUGS L677 option (a).
Claude's checklist runs in `ant`; Codex's plan hooks run in separate processes
through `ant/src/policy-cli.ts`. Both send unchanged native snapshots through
the existing `submit_status` method on routd's MCP socket.
The parent-scoped reducer runs under `lockTurn` in `routd/turns.go`.
It normalizes native snapshots, owns checklist state, and renders one snapshot for every sink.

`todoStatus` in `ant/src/todo-status.ts` becomes a plain event sender.
Its `renderTodos` moves to routd. `streamStatuses` in `ant/src/index.ts`
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
GitHub keeps the D1 exception. pi remains disabled until its MCP bridge ships.

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
| gitd   | no              | `👀` | no                                           | `👀` on trigger | dropped                    | `👀` removed                                               |
| googd  | no              | none | no; Drive needs `edit` and `delete`          | nothing         | dropped                    | nothing                                                    |
| reditd | no              | none | yes, no `delete`                             | nothing         | dropped                    | nothing                                                    |
| webd   | no              | none | server edit/delete; browser removal required | nothing         | one status frame, replaced | server emits delete; built-in browser must remove the node |
| bskyd  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |
| linkd  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |
| twitd  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |
| emaid  | no              | none | no                                           | nothing         | dropped                    | nothing                                                    |

`typing` is not in webd's map: webd renders no presence, and a declared
capability with a no-op behind it is the drift `CapImplReport` exists to catch,
`typing` being the one verb it does not probe. googd advertises `send_text` alone
today, so routd drops its updates and Drive shows nothing between the question
and the answer; Drive requires both `edit` and `delete` to show a line.

## The nudge

Use one reminder for a turn without a checklist. Trigger it after eight
completed parent tool calls or 180 seconds, whichever comes first.
Seven calls do not trigger it. A checklist suppresses it, including a checklist
that the agent explicitly clears. Recent fallback statuses do not count as a checklist.
Count each completed call once by native call ID; exclude child calls.
The call count and reminder state survive attempts of the same turn.

Reuse `nudgeProgress`, `STEER_SPOOL`, and the existing steering hook.
Use the existing timer as the one-shot time limit. Do not add another timer,
Python hook, blocking guard, or reminder path. Only between turns does
`drainSteering` discard stale reminders. During live background-agent waiting,
the existing poll delivers the current reminder through the same spool and
`renderSteering`, even without another parent tool call.
The live waiting poll reads `drainLines(STEER_SPOOL)` without the stale filter.
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
  (`specs/6/27-git-channel.md §"Typing indicator"`), and re-arms the backstop of
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

Decided: **B** on a channel with `edit` and **C** on gitd and googd, which
leave `edit` out of their capability maps. B is strictly quieter than A on
every channel. C is the answer for a channel where any extra post is unwanted;
the owner ruled on 2026-10-24 that GitHub gets no progress comment. Its cost
is that the agent's own `edit` tool, which ipc registers on every turn, returns
unsupported there (`chanlib.NoSocial`). `specs/6/27-git-channel.md` and
`specs/6/45-gdrive-comments-channel.md` accept this.

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

## What this changes in 6/27 and 6/45

The two channel specs disagree about what the channel may do to a thread.
`specs/6/27-git-channel.md §"Channel messages and repository tools"` has gitd
post AND react; `specs/6/45-gdrive-comments-channel.md §"Decision 3 — one
verb, the thread reply"` has googd post and nothing else, with `Typing` a
no-op and a test that the capability map holds exactly one entry. Both get
one line, and it is the same line (`6/27` carries it; `6/45` takes it when the Drive probe passes):

> The channel creates, edits and reacts to ITS OWN posts in the thread it was
> addressed in. Every change to another party's state — resolve, reopen,
> merge, label, a new thread, the document body — is the connector's, under
> its own grant and hold.

Under that line a progress message, its edits and deletion, and the `👀` mark are channel
plumbing with the authority the reply already has; `comments.update` on a
human's comment and `action=resolve` are not. Concretely:

- `6/27`: `capsFor` advertises `send_text`, `send_file` and `typing` with the
  token, and no `edit`, so GitHub shows the `👀` mark and no progress comment.
  §"Typing indicator" there is the rendering (`👀` through the shared helper,
  the two reaction endpoints) and points here for the mechanism.
- `6/45`, with Drive editing (not built): `capsFor` advertises `send_text` and, when the probe below passes,
  `edit`; `Edit` is `replies.update` on a reply whose id googd minted
  (`gd-r<fileId>.<commentId>.<replyId>`), `{"content"}` only, and the
  one-verb test becomes "the serialized body of every call has no `action`
  key". `Typing` stays a no-op and `typing` stays out of the map: there is
  nothing to render. The §"Not built" row "a second channel verb" names
  resolve, reopen, a new thread and the body, not an edit of the agent's own
  reply.

## Tier

Hot-tier per-turn output, as `specs/5/24-live-tasklist-status.md §"Tier"`: no
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
| Sink exceptions      | GitHub posts no progress comment and removes its eyes mark at End. pi remains rejected. Drive remains blocked on §"Unverified".                                                                                                                                                                                                                                                                                                                                              |

Retain the lifecycle checks in `routd/progress_test.go`, `routd/typing_test.go`
and `routd/fixes_test.go`. Retain the mark checks in `chanlib/mark_test.go`,
`gitd/typing_test.go` and `slakd/integration_test.go`.
Check the web server in `webd/edit_test.go` and the built-in browser with a
browser test. The Go producer and TypeScript reader share a contract test for
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
No live acceptance or image build is claimed by this spec amendment.

## Unverified

| Claim                                                                                                                             | Probe                                              | If false                                                                    |
| --------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------- | --------------------------------------------------------------------------- |
| `replies.update` changes a reply's `content` (the guide says the creator "can … edit … a reply"; the method page lists no fields) | PATCH one reply googd posted, then list the thread | googd keeps `send_text` alone and renders nothing, which is its state today |

The probe has not run: it waits on the Drive API being enabled in the Cloud
project of the service-account key. Until it runs and passes, googd renders
nothing for progress.
