---
status: draft
depends:
  [
    34-resident-prompt,
    ../1/R-prompt-format,
    ../5/9-agent-capability-eval,
    ../5/G-engagement,
  ]
relates-to: [5/Y-output-styles-per-surface, 5/24-live-tasklist-status]
---

# specs/6/44 — one delivery door, and the nudge that teaches it

Decision (maintainer, unsigned): **a turn reaches a chat through a delivery
tool call, or it reaches nothing.** The agent's prose result stops being a
delivery path. A turn that leaves prose behind and calls no delivery tool gets
a nudge and one more round on the same session. A turn that still delivers
nothing ends in the notice `routd/dispatch.go:745` already carries.

`status: draft` because this is a redesign — a changed contract plus changed
order inside `recordTurnResult` — and root `CLAUDE.md` holds redesigns for
sign-off. It is also gated on a number nobody has measured (§"The gate").

## Problem: one contract, three declarations

The question "what does the agent do to make text reach the user" has three
answers in the tree, and they disagree.

| Site                                 | What it declares                                                               |
| ------------------------------------ | ------------------------------------------------------------------------------ |
| `ipc/ipc.go:1194` — the `reply` tool | "THE DEFAULT way to respond — use this for virtually every answer to the user" |
| `ant/CLAUDE.md:85`                   | "Only your FINAL text block is delivered… write the answer once, at the end"   |
| `routd/turns.go:252`                 | delivers `req.Result` as the reply whenever the turn produced no bot row       |

That is the "one declaration, many readers" rule of root `CLAUDE.md` broken in
the one place where both readers are the same agent. The two paths do not even
render alike: the tool path (`routd/mcp.go:252` `mcpDeliver`) sends the argument
verbatim, while the prose path (`routd/turns.go:55` `appendAndDeliver`) strips
`<think>`, lifts `<status>` blocks and runs `router.FormatOutbound`. Two
renderers for one sink.

### What it costs — measured 2026-09-20

A desk agent woke on a message that did not address it, decided correctly, and
delivered the decision:

```
Not addressed to me — <user> is continuing the geopolitics discussion
with <other>, no @mention or tag. Desk norm is reply only when tagged.
... Staying silent.
```

`router.FormatOutbound` (`router/router.go:178`) carries a guard for exactly
this, `silentRefusalRe` (`router/router.go:163`), and the guard is anchored
whole-string. Run on the four shapes:

| final block                                 | delivered      |
| ------------------------------------------- | -------------- |
| `Staying silent.`                           | ""             |
| `<think>reasoning</think>\nStaying silent.` | ""             |
| `Not addressed to me. Staying silent.`      | the whole line |
| the desk agent's sentence above             | the whole line |

The guard loses because deliberation is unbounded text and the guard is a
vocabulary list. Widening it is the same bet at longer odds.

The agent is told the rule three times inside one section
(`ant/CLAUDE.md:73`, `:77`, `:85`), one of them a bolded ALWAYS, and wrote the
prose anyway. Repetition is not the lever. Filed as BUGS `L329`.

### Why the prose path is load-bearing today

`routd/turns.go:245-252` states its own reason: the daemon split dropped the
prose path, a normal question produced output that never left routd, and the
fallback closed the krons no-reply outage of 2026-06-08. Any inversion answers
that outage or it is not shipped.

### The prose path is already half-broken

`ant` runs an inner loop: a turn past the first submits under
`<seed-turn-id>:<n>` (`ant/src/index.ts:459`). `store.turnKey` is an exact
lookup, and no code opens a turn context under a suffixed id, so
`recordTurnResult` answers "no turn context for turn_id" and the prose is lost.
The `reply` tool is unaffected — it stamps routd's own active turn
(`routd/mcp.go:520` `CurrentTurnID`), not ant's. So on every steered or nudged
round the tool path is the only one that works. The contract this spec picks is
the one that already holds everywhere.

## The contract

A turn **delivers** when it produces a bot row that is not a status row —
`reply`, `send`, `send_file`, `send_voice`, `like`, `dislike`, `delete`. That
predicate exists: `TurnHasBotReply` (`store/messages.go:930`), and
`deliverStatus` stamps `verb="status"` (`routd/turns.go:135`) precisely so an
interim ⏳ never counts as an answer.

Three outcomes at `submit_turn`, and no fourth:

1. **Delivered.** A bot row exists. Nothing else happens.
2. **Deliberately silent.** No bot row, and `FormatOutbound(result)` is empty —
   the agent wrapped its reasoning in `<think>` and wrote nothing outside it.
   Nothing is delivered and nothing is said. This is the correct shape of the
   L329 turn.
3. **Prose left behind.** No bot row, and `FormatOutbound(result)` is not
   empty. The prose is not delivered. The turn gets one nudge round
   (§"The nudge").

Isolated cron turns (`trigger` prefix `timed-isolated:`) keep their existing
exemption: their output is the file write, and they are excluded before the
branch is reached.

**`FormatOutbound` moves to the one surviving door** — the delivery tools'
`text` argument — so `reply` and `send` render exactly as the prose path did.
`appendAndDeliver` and `silentRefusalRe` are deleted; `appendAndDeliver` has one
production caller (`routd/turns.go:253`) and exists only for the fallback, and
the guard has nothing left to guard once no turn is delivered by accident. That
is a net deletion, not a new layer.

## What replaces the 2026-06-08 guard

Three things, and none of them is a fallback.

**The path that broke is gone.** The outage was routd dropping `req.Result`.
With `req.Result` no longer a delivery path, that break has no way to express
itself. The equivalent break under this contract is the tool path failing, and
that one is loud on both ends: a failed send comes back to the agent as a tool
error and writes a failure row (`ipc/ipc.go:1216-1220`), and routd sees no bot
row.

**The silent state already has a notice.** `routd/dispatch.go:583` counts a
turn that delivered nothing and `:584` sends `silentTurnNotice` to the chat.
This contract extends that trigger to "delivered nothing after the nudge round".
The user never sits in silence; the operator sees the counter and a log line
(today the fallback fires with neither).

**The verification rule is already written.** `1/R-prompt-format.md`
§"Per-turn output" says this path has broken silently twice and must be verified
by asserting a bot row lands, never by asserting the turn completed.
`tests/e2e_turn_test.go:411` asserts bot-row counts. The fallback never caught
the 2026-06-08 outage — users did; the assertion is what catches it.

**Order matters.** `recordTurnResult` marks the turn `done` and publishes
`round_done` after the delivery step. The nudge round belongs inside that
window: routd holds the turn open until the nudge resolves, and the nudged round
submits under **the same turn id**, so one turn context, one `round_done` and one
retry budget cover both rounds. A nudge round under a fresh id would repeat the
suffixed-id loss above and hang every SSE subscriber waiting on the first
`round_done`.

## The nudge: a wire object that already exists

A nudge is **one tagged line of text prepended to a prompt**. The shape is in
the tree, in four producers and three transports:

| Producer                      | Shape                               | When          |
| ----------------------------- | ----------------------------------- | ------------- |
| `container/runner.go:538`     | `[solve] …` → `Input.Annotations`   | before a turn |
| `container/runner.go:520`     | `[pending migration] …` → same      | before a turn |
| `container/runner.go:369-383` | `⚠️ SYSTEM: …` → the IPC input dir  | mid-turn      |
| `runed/docker.go:246`         | `writeIPCInput` → the IPC input dir | mid-turn      |
| `ant/src/index.ts:145`        | `NUDGE_TEXT` → `STEER_SPOOL`        | mid-turn      |

Container side there are two consumers: the `steering` policy row
(`ant/src/policy.ts:82`) injects the spool into a live turn on `PostToolUse`,
and `checkIpcMessage` (`ant/src/index.ts:238`) drains both queues between turns
and starts another round on the same session. `[solve]` rides on every prompt
the fleet runs, so the mechanism is proven at full scale.

This spec adds **no transport and no field**. It fixes what is already there:

- **One host-side writer.** `container/runner.go:369-383` hand-rolls the same
  temp-plus-rename `runed/docker.go:246` already provides, with its text inline.
  The soft-deadline warning moves onto `writeIPCInput`; two writers for one
  format is the drift the project forbids.
- **The tag is the nudge's identity.** `drainSteering` drops a stale progress
  nudge by exact string equality with `NUDGE_TEXT` (`ant/src/index.ts:232`).
  That shape holds exactly one nudge kind. The tag replaces it: a nudge declares
  whether it may START a round. `[progress]` may not — it is only meaningful
  inside a running turn. `[deliver]` must.
- **The delivery nudge enters through `submit_turn`'s response.** routd is not
  on the container's filesystem — the IPC input dir belongs to the host side
  that spawned the container (runed), and routd's only reach into a live turn is
  the MCP socket it serves. The response is already there and already carries a
  body (`ipc/ipc.go:584`, `{"ok": true}`). ant writes the returned nudge to the
  steer spool, and `checkIpcMessage` runs it. One extra model round trip, same
  container, same session, no new spawn.

The `[deliver]` nudge text is composed by routd from the turn's own facts —
the trigger kind, whether a status was delivered, whether a reply was owed. It
is the **one renderer** of the delivery rule; the sentences it composes leave
`ant/CLAUDE.md` in the same change (§"What this subtracts").

## The detector: state, not text

The proposal was regexes over the turn's inbound and outbound. Honest reading:

**Over inbound: no.** Whether this turn was addressed is a fact routd already
holds — the route decision, the trigger kind (`verb=mention`, `<observed>`,
`timed-*`), the engagement window of `5/G`. A regex over the message body would
be a second classifier beside the router's own, deciding the same question from
worse evidence. That is the parallel path the project bans: the router's answer
is the one every other surface reads, so a second one can only agree or lie.

**Over outbound, as a situation detector: no.** The L329 sentence is the proof.
A detector for "the agent is narrating a refusal" needs a vocabulary list, and
`silentRefusalRe` is that list — seven alternatives, and it catches two of the
four measured shapes. Deliberation is unbounded text.

**Over outbound, as an argument check at the door: yes,** and this is what the
inversion buys that the nudge does not. With one door, a rule about what may
appear in delivered text becomes a check on a tool argument — exact, zero false
positives, refused before the user sees it, and loud to the agent because a
refused tool call returns its reason. Two current prompt rules are exactly this
shape: a literal `$WEB_HOST` in the text, and a container path (`~/…`,
`/var/lib/www/…`) offered as a link. Both are silent failures today and both are
string equality.

**State detectors are the general form.** The facts routd owns — a bot row
exists or does not, a status row was delivered, the trigger was a mention, the
tool list lacks a grant — are exact and cannot be phrased around. Every nudge
this spec ships fires on one of them. A nudge with no state behind it is a
prompt rule wearing a new coat, and belongs in the file it came from.

**What the state detector cannot see:** it cannot tell a good answer from a bad
one, it cannot tell whether silence was correct, and it cannot fire before the
turn ends. The cost of the last one is the round trip, which is what §"The gate"
prices.

## What this subtracts from `ant/CLAUDE.md`

The file is 198 lines / 16,482 bytes / 22 top-level sections (measured
2026-09-20). `6/34` already classified every section and set the rule: a
section stays resident only if breaking it fails silently, **and it leaves when
a mechanism makes the failure loud**. This is that mechanism, for the delivery
subset only.

| Lines   | L / B   | Section                    | Goes where                                                                                                                                                  |
| ------- | ------- | -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 73–77   | 5 / 468 | When to respond            | deleted — the reply-when-addressed and mention-must-answer rules become the `[deliver]` nudge's own trigger conditions, composed by routd from `tc.Trigger` |
| 85–88   | 4 / 509 | When to respond            | deleted outright — "only your FINAL text block is delivered" and "after a tool call, stay silent" describe a contract that no longer exists                 |
| 124     | 1 / 510 | Status updates             | deleted — "emit a `<status>` → you OWE a final reply" is a state routd holds (a status row, no bot row); the nudge says it                                  |
| 139–142 | 4 / 143 | Environment (`$WEB_HOST`)  | the delivery tools' argument check                                                                                                                          |
| 188–191 | 4 / 288 | Local paths vs public URLs | the delivery tools' argument check                                                                                                                          |

**18 lines, 1,918 bytes — 9.1 % of the lines, 11.6 % of the bytes.** That is
the whole shrink this mechanism earns, and it is not the bulk of the file. The
honest reason is that `6/34` already did the bulk (the file went 388 → 198
lines), and what
survived is resident under its rules 1, 3 and 4 — the envelope, the map, and
sections a skill names as its source. A nudge cannot replace any of those: they
are needed before the reader has a question, and no state detects their absence.

Sections that stay, and why the nudge does not take them:

- **Response Style** (21 lines / 2,322 B, the largest section) stays. No state
  detects a register breach, and length already belongs to the output style by
  `5/Y`. Firing on delivered text would nudge after the user has read it.
- **Task discipline**'s `AskUserQuestion` rule stays. `6/34` already assigned it
  a different mechanism — a `PreToolUse` deny — and that one stops the call
  instead of catching its aftermath. Two mechanisms for one concern is drift;
  the deny wins.
- **Reactions**' floor rule stays. A reaction is a delivery tool call and does
  satisfy the contract, but `6/34` guard 1 blocks the move: no `anteval` case
  exercises it.
- **When blocked**'s "never answer I don't know without `/recall-memories`" is
  the next plausible state detector (no memory tool called, a not-found answer
  delivered) and is **not built here**: it needs a vocabulary list for the
  second half, which is the detector this spec rejected.

### The conformance case this ships

`6/34` guard 1: a section leaves residency only when an `anteval` case exercises
the behaviour. `anteval/cases/anteval.toml` covers the owed answer
(`rest-roundtrip`) and the public-URL rule (`link-is-public-url`). It has **no
case for the L329 shape**. This change adds one: a message in a chat the folder
watches but is not addressed in, asserting **zero** bot rows for that turn — the
inverse assertion to `rest-roundtrip`'s. The `$WEB_HOST` rule needs no case: a
refused tool call is not contingent on the agent choosing to load anything,
which is the contingency guard 1 exists to cover.

## The gate

Nobody knows how often a turn calls no delivery tool. The branch at
`routd/turns.go:252` fires with no log line and no counter, so the inversion's
cost — one extra round trip per nudged turn — is unpriced. **The instrumentation
ships first, alone, and the inversion waits on its number.**

`arizuko_prose_fallback_turns_total` (`obs/metrics.go`) counts the branch, per
folder, at `routd/turns.go`. It is a SEPARATE counter from
`arizuko_silent_turns_total` rather than a label on it, and the reason is the
name: `obs/README.md` publishes that series as "how many answered nothing" and
names it the one to drive to zero. A prose-fallback turn answered. Folding the
two together makes the published name false for anyone querying it and moves a
healthy path into a series an operator reads as a defect — the counter's name
is the declaration here, and two meanings under one name is the coupling this
project splits.

Window: krons, ≥ 2,000 turns, ≥ 5 folders, ≥ 7 days. Ratio: `prose_only` over
turns that delivered anything at all.

| Measured      | Verdict                                                                                                                                                                                                                                          |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **< 10 %**    | Invert. One extra round trip on fewer than one turn in ten.                                                                                                                                                                                      |
| **10 – 40 %** | The rate is a declaration problem before it is a design problem: `ant/CLAUDE.md:85` teaches prose and `ipc/ipc.go:1194` teaches the tool. Fix the contradiction, re-measure, decide.                                                             |
| **> 40 %**    | The inversion is the wrong trade. Prose is the contract in practice, and the answer is the opposite fix: drop the `reply` tool's "THE DEFAULT" claim, make prose the single declared path, and close `L329` with a widened boundary guard alone. |

**The expected result is the high band, and that is the spec's own risk.**
`ant/CLAUDE.md:85` instructs the agent to answer in its final block, so a
well-behaved agent under today's manual produces `prose_only` on most answering
turns. If the number lands there, this spec's inversion is not the cheap change
it looks like — it re-runs the majority of turns — and the correct sequence is
manual first, measurement second, inversion third.

Second gate, after the inversion: **nudge effectiveness.** If more than 5 % of
nudged rounds also deliver nothing, the nudge does not teach, the notice path
carries the load, and the chat is worse than the fallback's. That number ends
the experiment.

## Declined

- **A nudge layer beside `ant/CLAUDE.md`.** Rejected: a nudge that restates a
  rule the file still carries is the second path this project forbids. Each
  nudge in the table above deletes its source lines in the same change.
- **Regex situation-detection as a general steering strategy.** Rejected for
  inbound (the router already classifies) and for outbound as a detector (the
  L329 sentence beats a vocabulary list). Kept only as exact-literal argument
  checks at the delivery door.
- **Nudging on style or length.** Rejected: it fires after the user has read the
  reply, and `5/Y` already owns per-surface length through the output style.
- **Widening `silentRefusalRe` as the fix.** It is the cheap patch for `L329`
  and it is a symptom patch: the leak is that deliberation reaches a chat at all,
  not that one phrasing slipped a list. It becomes the fix of record only under
  the `> 40 %` branch above, where the prose path stays.

## Pointers

- `routd/turns.go:55` `appendAndDeliver`, `:252` the fallback branch — the prose
  path, one production caller, deleted by this contract.
- `router/router.go:163` `silentRefusalRe`, `:178` `FormatOutbound` — the guard
  that goes, and the renderer that moves to the tool argument.
- `routd/mcp.go:252` `mcpDeliver`, `:520` `CurrentTurnID` — the surviving door.
- `ipc/ipc.go:1194` the `reply` description, `:1216` the send error the agent
  sees, `:584` the `submit_turn` response — the contract's other declaration,
  and where the nudge rides back.
- `store/messages.go:930` `TurnHasBotReply`, `routd/turns.go:135`
  `deliverStatus` — the predicate and the reason a status is not an answer.
- `container/runner.go:369` and `runed/docker.go:246` — two writers, one format.
- `ant/src/index.ts:145` `NUDGE_TEXT`, `:232` the exact-text filter, `:238`
  `checkIpcMessage`; `ant/src/policy.ts:82` the `steering` row;
  `ant/src/spool.ts:19` `STEER_SPOOL`.
- `obs/metrics.go:135` `RecordSilentTurn`; `routd/dispatch.go:583` its one call
  site, `:745` `silentTurnNotice`.
- [`34-resident-prompt.md`](34-resident-prompt.md) — the residency rule and the
  three guards this change must satisfy.
- [`../1/R-prompt-format.md`](../1/R-prompt-format.md) — the stdin/stdout
  contract the nudge enters through.
- `BUGS.md` `L329` — the measurement table this spec starts from.
