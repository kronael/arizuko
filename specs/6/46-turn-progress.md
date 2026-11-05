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

# specs/6/46 — turn progress: one declaration, two renderings, a declared degradation per sink

routd declares that a turn is running, what it is doing, and that it ended.
Every channel declares its presence, edit and delete capabilities. A turn that
reports shows ONE temporary progress line, posted where the turn's answer will
go and edited in place. When the turn ends the line is deleted. A channel that
cannot both edit and delete shows no progress line. Nothing of progress stays
behind and no progress message starts a thread.

`status: partial`: the renderer, the `edit` and `delete` sink webd,
the shared `👀` mark, gitd without `edit` and the deletion of the progress
line at `End` are built (§"Build state"). Not built: the `Typing` port
change (D3, declined), Drive `edit` (blocked on the Drive API, §"Unverified") and
the measurement on krons (§"Acceptance").

## Build state

The voluntary-reply contract (`47-voluntary-reply.md`) removes the historical
final-result status reader below. Only ant extracts `<status>` blocks;
`submit_status` remains routd's progress input.

| Step | What                                                                                         | State                                                                       | Pointer                                               |
| ---- | -------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- | ----------------------------------------------------- |
| 1    | routd renders `Begin` and `Update`; no `edit`, no message; equal text no-op                  | built, `d6572106`                                                           | `routd/progress.go` `beginProgress`, `updateProgress` |
| 2    | `finishTurn` is the one writer of `done`; `End` ends the progress line                       | built, `dd670329`                                                           | `routd/progress.go` `finishTurn`, `endProgress`       |
| 3    | gitd advertises no `edit`; GitHub renders the `👀` mark and no comment                       | built, `0c175988`                                                           | `gitd/main.go` `capsFor`                              |
| 4    | webd `edit` as a replace frame; `typing` out of its map                                      | built, `eddce204`; open: `L439`, `L440` in `BUGS.md`                        | `webd/channel.go`                                     |
| 5    | the `👀` mark once in chanlib; slakd and gitd on it                                          | built, `68fc2895`                                                           | `chanlib/mark.go`                                     |
| 6    | `Typing(req TypingRequest)` carrying the trigger id (D3)                                     | not built; declined                                                         | —                                                     |
| 7    | googd `edit` on its own reply                                                                | not built; the probe has not run, the Drive API is off in the key's project | §"Unverified"                                         |
| 8    | `End` deletes the progress message on a channel that advertises `delete`                     | built, `c0e361ac`                                                           | `routd/progress.go` `endProgress`                     |
| 9    | one temporary line: no agent-written opener; a line needs `edit` and `delete`; webd `delete` | built (this change)                                                         | `routd/progress.go`, `webd/channel.go`                |
| 10   | the nudge: first at 180 s, only where a line shows, says what comes next                     | built (this change)                                                         | §"The nudge", `ant/src/index.ts`                      |
| —    | the acceptance list measured on krons                                                        | not measured                                                                | §"Acceptance"                                         |

## Problem

At `dfa01214`, before the renderer, "the agent is working" was one fact with five
declaration sites in routd and a rendering per adapter:

| Site                                | What it declares                                                                                    |
| ----------------------------------- | --------------------------------------------------------------------------------------------------- |
| `routd/dispatch.go:436`             | `Typing(on)` when the run is dispatched                                                             |
| `routd/dispatch.go:444`             | `Typing(off)` for every chat the run owns, after the run returns                                    |
| `routd/turns.go:85`                 | `Typing(off)` after the prose reply is delivered                                                    |
| `routd/mcp.go` `mcpSubmitStatus`    | the text of a `submit_status` call: edit the last `verb=status` row, else send a fresh `⏳` message |
| `routd/turns.go` `appendAndDeliver` | every `<status>` block left in the final result, each as a fresh `⏳` message                       |

The renderings disagreed, and two of them lied:

- Three adapters render `Typing` natively (teled `sendChatAction`, discd
  `ChannelTyping`, whapd composing). slakd and gitd render it as an `👀`
  reaction on the message that started the turn, through one chanlib helper
  (`chanlib/mark.go`, §"The mark, implemented once"). Six adapters and webd
  render nothing; webd advertised `typing` and answered `ok` (`webd/channel.go:71`).
- The progress message was edited in place only where the channel advertises
  `edit`. Everywhere else `mcpSubmitStatus` fell back to a fresh send, so the
  one-message rule of `specs/5/24-live-tasklist-status.md §"Design"` held on
  five adapters and failed on seven. Measured on `vumilan/morha` 2026-10-16
  through `gitd`: PR 277 carries two `⏳` comments before the review
  (`6223825595`, `6223840016`); PR 278 carries three, two of them the same
  text one second apart (`6223728182` 20:06:35Z, `6223728470` 20:06:36Z; BUGS
  `L438`, closed by `d6572106`). Each stands above the answer forever, still saying `⏳`.
- A status row kept the FIRST text it was sent with; the edits reached the
  platform and never the row, so history and the web frames showed a text the
  user no longer saw.
- webd decided a row is a status by its glyph (`webd/turn.go:28`,
  `strings.HasPrefix(m.Content, "⏳ ")`) while the row carried
  `verb="status"`: two declarations of one fact.

The Drive channel makes the gap unavoidable rather than untidy: the Drive
comments API has no reaction. Verified 2026-10-17 on Google's reference pages
— the Comment resource (`replies`, `mentionedEmailAddresses`, `id`, `kind`,
`createdTime`, `modifiedTime`, `resolved`, `anchor`, `author`, `deleted`,
`htmlContent`, `content`, `quotedFileContent`, `assigneeEmailAddress`), the
Reply resource, the Docs API `CommentThread`/`Post`/`PostAuthor` objects and
the "Manage comments and replies" guide name no reaction, emoji or like. A
reaction-shaped indicator cannot exist on `gdrive:`; the only rendering Drive
offers is a reply, edited.

## The declaration

routd owns the turn, so routd declares its progress. One renderer,
`routd/progress.go`, is the only code that calls `Typing`, or sends or edits
progress-only rows:

| Event             | Called from                                                                                                     | Carries                      |
| ----------------- | --------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| `Begin(turn)`     | the dispatch, once, before the run                                                                              | chat JID, trigger message id |
| `Update(turn, t)` | `submit_status` from `TodoWrite` and streamed `<status>` blocks                                                 | progress text                |
| `End(turn, o)`    | `finishTurn`, the one function every state→`done` site calls (result recorded, terminal failure, silent, clean) | outcome ∈ {`done`, `failed`} |

In code these are `Loop.beginProgress`, `Server.updateProgress` and
`endProgress`, called by `finishTurn`.

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

The agent does not write an opener. `submit_status` carries text only.

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

- It is created by the FIRST `Update`, never by `Begin`: a turn that reports
  nothing posts nothing.
- It is created only on a channel that advertises `edit` and `delete`: the
  line is edited while the turn runs and removed when it ends, so a channel that
  lacks either shows none. On any other channel the update is dropped and logged
  at debug with the turn id. There is no fallback to a fresh send, and no
  terminal edit (`✓`, `✗`) that would leave a line behind.
- Each later `Update` edits it. Equal text is a no-op.
- The row's content is rewritten on every edit, so the row is the message as
  the user sees it.
- `End` deletes the message (`Deliverer.Delete`) and marks the row `✓`/`✗`
  without a platform edit, so a second `End` does nothing. A refused delete is
  logged at Error and the row keeps what the user sees. A row with no platform
  id has nothing to delete. The failure notice stays its own message, because a
  notice must notify.
- A real edit failure (not `ErrUnsupported`) surfaces to the caller, as
  `specs/5/24-live-tasklist-status.md §"Design"` already requires; nothing is
  re-posted.

The live message id is the newest `verb=status` row's `platform_id`
(`store/messages.go` `LastStatusRow`), so the message survives a routd
restart: `recoverPending` re-enqueues the running turn, the turn reaches
`finishTurn`, and `End` finds the row. A `(turn, chat)` never has two checklist
rows.

`End` fires when the turn ends, not when the reply lands: an agent that
replies through the `reply` tool and then writes its diary is still working,
and the message says so until it is not.

## The sink's declaration

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

| Sink   | Native presence | Mark | `edit`                                           | Begin renders   | Update renders             | End renders                |
| ------ | --------------- | ---- | ------------------------------------------------ | --------------- | -------------------------- | -------------------------- |
| teled  | yes             | —    | yes, and `delete`                                | typing          | one `⏳` line, edited      | typing off, line deleted   |
| discd  | yes             | —    | yes, and `delete`                                | typing          | same                       | same                       |
| whapd  | yes             | —    | yes, and `delete`                                | typing          | same                       | same                       |
| slakd  | no              | `👀` | yes, and `delete`                                | `👀` on trigger | same                       | `👀` removed, line deleted |
| gitd   | no              | `👀` | no                                               | `👀` on trigger | dropped                    | `👀` removed               |
| googd  | no              | none | no; step 7 needs `edit` and `delete`             | nothing         | dropped                    | nothing                    |
| reditd | no              | none | yes, no `delete`                                 | nothing         | dropped                    | nothing                    |
| webd   | no              | none | yes (replace frame), and `delete` (remove frame) | nothing         | one status frame, replaced | frame removed              |
| bskyd  | no              | none | no                                               | nothing         | dropped                    | nothing                    |
| linkd  | no              | none | no                                               | nothing         | dropped                    | nothing                    |
| twitd  | no              | none | no                                               | nothing         | dropped                    | nothing                    |
| emaid  | no              | none | no                                               | nothing         | dropped                    | nothing                    |

`typing` is not in webd's map: webd renders no presence, and a declared
capability with a no-op behind it is the drift `CapImplReport` exists to catch,
`typing` being the one verb it does not probe. googd advertises `send_text` alone
today, so routd drops its updates and Drive shows nothing between the question
and the answer; step 7 gives it a line only with both `edit` and `delete`.

## The nudge

ant asks a running agent for a `<status>` line when the turn has run long and
no line has been reported. The text asks what the user gets next, not which tool,
model or file. The first nudge comes 180 s into a run and repeats every 180 s.

A nudge is useful only where a line shows. routd resolves that once at dispatch
and hands ant the fact as `ARIZUKO_PROGRESS` (`runed.RunRequest.Progress`): `1`
when the turn is addressed (a person is owed an answer; cron, scheduled and
migrate turns are not) and the reply chat advertises `edit` and `delete`;
empty otherwise. Without `1`, ant starts no nudge timer.
`runed/env_contract_test.go` ties the key's producer to ant's read.

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
one line, and it is the same line (`6/27` carries it; `6/45` takes it with step 7):

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
- `6/45`, with step 7 (not built): `capsFor` advertises `send_text` and, when the probe below passes,
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

The routd items are pinned by `routd/progress_test.go`, the mark items by
`chanlib/mark_test.go`, `gitd/typing_test.go` and `slakd/integration_test.go`, the
edit items by `webd/edit_test.go`, and the gitd capability map by `gitd/caps_test.go`. None has been measured
on krons; that run is what holds this spec at `partial`. The googd clause waits
on step 7.

- One turn on a channel with `edit` and `delete` that reports three statuses,
  two of them equal, leaves exactly one `verb=status` row, one platform message,
  two edits and a delete; the row's content equals the last text.
- The same turn on a channel without `edit` or without `delete` leaves no status
  row and no platform message, and a debug log line per dropped update.
- Duplicate progress text posts once
  (`TestProgress_EqualTextIsNoOp`).
- No `submit_status` call posts an opener, and a progress line in a topic joins
  that topic (`routd/progress_test.go`).
- A failed turn's line is deleted like any other; the failure notice is a
  separate message.
- `End` deletes the message and a second `End` deletes nothing; a refused delete
  is logged and leaves the row as the user sees it.
- webd `POST /delete` publishes a `delete` frame with the target id on the same
  hub key as the original frame (`webd/edit_test.go`); replay of a turn never
  returns a status row.
- The nudge starts at 180 s only when `ARIZUKO_PROGRESS` is `1`, and its text
  asks what the user gets next (`ant/src/index.test.ts`).
- A GitHub turn that reports a status posts no progress comment; the `👀` stands
  while the turn runs and is removed at `End`.
- gitd and slakd set and remove the `👀` through the shared helper; a
  persisted mark older than `DefaultTypingMaxTTL` at boot is removed; no
  `eyes.json` is written.
- `CapImplReport` is clean for gitd (`send_text`, `send_file`, `typing`), googd
  (`send_text`, `edit` when credentialed and the probe passed) and webd
  (`send_text`, `edit`).
- Every existing `routd/typing_test.go` and `routd/fixes_test.go` case passes
  or is rewritten against the renderer, never deleted.

## Unverified

| Claim                                                                                                                             | Probe                                              | If false                                                                    |
| --------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------- | --------------------------------------------------------------------------- |
| `replies.update` changes a reply's `content` (the guide says the creator "can … edit … a reply"; the method page lists no fields) | PATCH one reply googd posted, then list the thread | googd keeps `send_text` alone and renders nothing, which is its state today |

The probe has not run: it waits on the Drive API being enabled in the Cloud
project of the service-account key. Until it runs and passes, googd renders
nothing for progress.
