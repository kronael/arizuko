---
status: partial
depends:
  [
    44-delivery-contract,
    46-turn-progress,
    34-resident-prompt,
    ../5/24-live-tasklist-status,
    ../5/G-engagement,
  ]
---

# specs/6/47 — the voluntary reply: a turn speaks through a tool call, thinks in prose, and is nudged once from one place

A turn-output contract. The only text that reaches a user is the argument of a
delivery tool call; everything the model prints is its thinking and reaches
nobody. routd decides at one point, from facts it recorded, whether a reply was
owed and is missing, and hands the agent one nudge. A Stop hook on a harness
that has one asks routd the same question before the turn ends; it saves a
round, it does not make the rule true.

`status: partial`: the addressed fact, shared nudge decision, submission
inversion, harness hooks and explicit delivery contract are built. Fresh
follow-ups wait until the active run ends, then use their own turn identity
and MCP binding. Fleet acceptance remains pending.
`44-delivery-contract` holds the analysis and is `reference`.

## Problem

Three declarations of "how text reaches the user" disagree
(`44-delivery-contract.md` §"Problem: one contract, three declarations"), and
the one in code is the prose fallback: `routd/turns.go:218` delivers
`req.Result` whenever a turn produced no bot row. Measured costs at `23faf1dc`:

| Cost                                                                                                                 | Where                                                                                                      |
| -------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| An agent's deliberation reaches the chat it decided not to join; the guard is a vocabulary list                      | `router/router.go:163` `silentRefusalRe`, `:174` `trailingRefusalRe`; BUGS `L329`                          |
| One `<status>` text has two readers: ant streams it, routd lifts it again from the final result                      | `ant/src/index.ts:174` `streamStatuses`, `routd/turns.go:65`; `L438`, closed by making `Update` idempotent |
| A turn that answers before a tool call loses the answer; 34 destroyed answers in 16 sessions                         | `L149`; the manual at `ant/CLAUDE.md:95` teaches the shape that loses                                      |
| A turn that correctly stays silent is indistinguishable from one that forgot; both end with no bot row and no notice | `routd/dispatch.go:530` fires only when `submit_turn` never came                                           |
| An unmentioned GitHub comment runs a turn, posts nothing, and the silence is correct — silence is common, not rare   | `L445`, measured 2026-10-18: 160 cents, zero comments                                                      |

## The contract

Four terms, each a fact routd already records or derives from one it records.

| Term          | Definition                                                                                                                                                                                                           | Declared at                                                                                                             |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| **delivered** | a bot row stamped with the turn id whose verb is not `status`, or an existing successful `message.*` social-action audit record for that exact turn and folder; social mutations deliberately append no chat message | `routd/turns.go` `turnDelivered`; `store/messages.go` `TurnHasBotReply`; `audit/audit_log.go` `HasSuccessfulTurnAction` |
| **addressed** | the turn fired because a person spoke to this folder (§"Addressed")                                                                                                                                                  | proposed: `resolution.Addressed` in `routd/loop.go` `resolve`, stored on the turn                                       |
| **owed**      | addressed, or the turn delivered a `verb=status` row (`ant/CLAUDE.md:134`: a status promises an answer)                                                                                                              | `routd/db.go:631` `LastStatusRow`                                                                                       |
| **thinking**  | every assistant text frame the harness yields; kept in the harness transcript, never on the wire                                                                                                                     | `ant/src/backend.ts:46` `Frame` `status`; `groups/<folder>/.claude/projects/-home-node/<session>.jsonl`                 |

The agent's only outward surfaces are the delivery tools above and the
`<status>` block (§"Narration"). There is no `<think>` tag: all prose is
thinking, so a tag that marks some of it as thinking marks nothing.

### Outcomes at turn end — proposed

`recordTurnResult` (`routd/turns.go:204`) is the one point every harness passes
through, because every harness ends a turn with `submit_turn`
(`ipc/README.md` § submit_turn ends the turn). It decides from three recorded
facts and nothing else:

| delivered | owed | nudged | Outcome                                                                                                                           |
| --------- | ---- | ------ | --------------------------------------------------------------------------------------------------------------------------------- |
| yes       | –    | –      | done; the turn is recorded                                                                                                        |
| no        | no   | –      | done, silent by design; one Info log line; nothing is sent, nothing is counted as a defect                                        |
| no        | yes  | 0      | **held**: the result is not recorded, the response carries the nudge (§"The nudge"), the turn's `nudged` count becomes 1          |
| no        | yes  | 1      | done; `silentTurnNotice` (`routd/dispatch.go:637`) to the chat; `arizuko_nudged_turns_total{outcome="silent"}`; progress ends `✗` |

A `status: error` submit takes the existing failure path unchanged
(`routd/dispatch.go:468`). A `timed_out` submit with no bot row is answered by
routd with `timedOutNotice`, a second constant beside `silentTurnNotice`: the
harness synthesises no reply text for a timeout, because no path could
deliver it (§"What leaves").

The isolated-cron exemption (`routd/turns.go:217`) disappears as a special case:
an isolated trigger is not addressed by the table in §"Addressed", so the
second row covers it. The session-lineage rule for isolated turns
(`routd/turns.go:226`) is a different fact and stays.

A held turn is still `running`; `callbackClosed` (`routd/turns.go:34`) closes
only when the run returns, and the nudge round runs inside the same container
run. A container that dies between the hold and the second submit reaches
`routd/dispatch.go:530` with no result recorded, which already sends the
notice and marks the turn failed. That floor is not new.

## Addressed

One declaration, in `resolve` (`routd/loop.go:632`), where every trigger
converges. The value travels `processGroupMessages` → `runTurn` →
`PutTurnContext` (`routd/dispatch.go:59`, `:126`, `:353`) and lands as a
turn-context attribute beside `trigger_sender` (`store/turn_context.go:36`).
Two readers: `recordTurnResult` and `check_turn`. Nothing re-derives it from the
JID or the message — `identity_derived_not_read` is the bug class.

| The turn fired because                                                                                   | addressed | Reason from the code                                                                                                              |
| -------------------------------------------------------------------------------------------------------- | --------- | --------------------------------------------------------------------------------------------------------------------------------- |
| a `web:` JID resolved directly (`routd/loop.go:696` `directFolder`)                                      | yes       | a person typing in the chat widget; `anteval` `rest-roundtrip` asserts a visible answer                                           |
| a `hook:` JID resolved directly                                                                          | no        | an event, not a person; the link's `<link-context>` carries its silence policy (`ant/skills/self/chat-link.md`)                   |
| a bare-folder JID resolved directly                                                                      | no        | a system turn with no channel; `routd/turns.go:146` dead-letters anything sent there                                              |
| a route rule matched with mode trigger (`rt.Mode != "observe"`), whether or not the chat is also engaged | yes       | the operator's rule says "fires a turn (you reply)" (`ant/skills/self/chat-routing.md` §"Modes")                                  |
| engagement alone (`routd/loop.go:636`, `:650`): the rule is observe, or there is no rule                 | no        | `../5/G-engagement.md` keeps the agent in a conversation it was once addressed in; `L445` is this row, and its silence is correct |
| the trigger sender is `timed`, `timed-*` or `system*` (`routd/dispatch.go:667` `turnCallerSub`)          | no        | a task is not a person; a task that wants a message says `send` in its text; `timed-isolated:` never speaks                       |

The envelope's `verb="mention"` (`router/router.go:133`) is not this predicate:
a bare route fires on every message and a DM carries no mention. The manual
tells the agent "reply when addressed"; the table is what routd means by it.

## The enforcement point, and the hook beside it

The rule is harness-independent, so the code that makes it true sits where
every harness passes: `recordTurnResult` in `routd/turns.go`. One function
renders the nudge from the turn's facts; two readers ask for it at two times.

| Reader                                | When                                    | Transport                                                                                                                | Harness                                    |
| ------------------------------------- | --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------ |
| `submit_turn` response (proposed)     | after the harness yields its result     | `ipc/ipc.go:566` `handleSubmitTurn` answers `{"ok": true, "nudge": "…"}`; ant runs one more round under the same turn id | all                                        |
| `check_turn` (proposed hidden method) | when the model stops, before the result | a `Stop` hook calls it over the agent socket; a nudge comes back as the hook's `block` reason, and the model continues   | claude; codex if §"Unverified" row 2 holds |

Both readers call the same `turnNudge(tc)`, and routd increments `nudged` when
it hands the text out, whichever reader asked. So one turn gets at most one
nudge: a Stop hook that already prompted leaves `submit_turn` nothing to hand
out, and a harness with no Stop hook gets the same text one round later. The
`stop_hook_active` input
(`ant/node_modules/@anthropic-ai/claude-agent-sdk/sdk.d.ts:9060`) makes the hook skip the call on its
second fire; routd's count is the guard even if it did not.

`check_turn` is a hidden JSON-RPC method beside `submit_turn` and
`submit_status` (`ipc/ipc.go:566`, `:610`): same socket, same `SO_PEERCRED`
credential, no `tools/list` entry, no REST twin. It is a hot-tier agent
action, not a management entity, so no resreg resource.

### Day one, per harness

| Harness | Delivery tools                                   | Early self-correction                                                                                                     | Enforcement                                                                                                                                                              |
| ------- | ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| claude  | MCP (`ant/src/claude.ts:522` `mcpServers`)       | `Stop` binding in `ant/src/policy.ts`, rendered as the SDK hook; ships only if §"Unverified" row 1 measures that it fires | `submit_turn` response round                                                                                                                                             |
| codex   | MCP (`-c mcp_servers.*`, `ant/src/codex.ts:299`) | the same binding written to `hooks.json` by `codexHooksConfig` (`ant/src/codex.ts:260`); ships only if row 2 measures     | `submit_turn` response round                                                                                                                                             |
| pi      | none (`ant/src/pi.ts:216` `tools: 'none'`)       | none                                                                                                                      | **refused**: `selectHarness` throws for a profile with no delivery tool, the same shape as `runnableBy` (`ant/src/policy.ts:106`) refusing a row a harness cannot honour |

pi cannot satisfy a contract whose only door is a tool it does not have. No
instance runs it (`/srv/data/arizuko_krons/.env` and
`/srv/data/arizuko_sloth/.env` carry no `ARIZUKO_BACKEND`; the default is claude, `ant/src/backend.ts:119`). The lift
is a `tools: 'bridged'` profile — `pi.registerTool` over the socket's
`tools/list` — and is its own spec; until then a loud refusal beats a harness
whose every addressed turn ends in a notice.

## The nudge

One renderer, `turnNudge` in `routd/turns.go` (proposed), composed from the
turn's recorded facts and nothing else. Minimal means one sentence, and the
sentence varies only on which fact made the reply owed:

| Fact                      | Text                                                                                  |
| ------------------------- | ------------------------------------------------------------------------------------- |
| addressed                 | `[deliver] You were addressed and delivered nothing. Call reply now (a like counts).` |
| status row, not addressed | `[deliver] You posted a status and delivered nothing. Call reply with the answer.`    |

The `[deliver]` tag is the nudge's identity, in the family of `[solve]` and
`[pending migration]` (`44-delivery-contract.md` §"The nudge: a wire object
that already exists"). It may start a round; the chassis's own progress nudge
(`ant/src/index.ts:145` `NUDGE_TEXT`) may not, and that distinction stays as it
is.

On the `submit_turn` path the chassis runs the nudge as the next prompt under
the SAME turn id (`ant/src/index.ts:468` derives `<seed>:<n>` for a new round;
a nudge round keeps the id), with the continuity token of the round just
ended. Same container, same session, one extra model round, no spawn. A round
under a suffixed id would find no turn context (`44-delivery-contract.md`
§"The prose path is already half-broken").

### The Stop hook

A fourth row in `bindings` (`ant/src/policy.ts:59`), `event: 'Stop'`, `returns:
['deny']`, whose policy calls `check_turn` over the socket and answers `deny`
with the nudge as its reason when one comes back, else `allow`. `PolicyEvent`
(`:27`) gains `'Stop'`. The renderers map it:

| Renderer                                    | `deny` on `Stop`                                                                                                                                         |
| ------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `claudeHooks` (`ant/src/claude.ts:275`)     | `{ decision: 'block', reason }` (`ant/node_modules/@anthropic-ai/claude-agent-sdk/sdk.d.ts:9130`); the model continues with the reason as its next input |
| `codexHooksConfig` (`ant/src/codex.ts:260`) | a `Stop` entry in `hooks.json` running `policy-cli Stop <turnID>`                                                                                        |
| `pi-extension.ts`                           | not rendered; the row is logged as unrunnable, as today for `rewrite`                                                                                    |

The hook holds no predicate of its own: it does not count delivery tool calls
it saw, because that would be a second declaration of **delivered** and would
drift from `TurnHasBotReply` the first time a verb is added. It asks.

A Stop is not always the end of the session. Claude Code fires `Stop` when the
model stops, and its input lists `background_tasks` still in flight
(`ant/node_modules/@anthropic-ai/claude-agent-sdk/sdk.d.ts` `StopHookInput`):
the session is paused, not done. The row allows such a Stop without asking
routd, so the check does not run on each Stop while tasks run. The claude
harness ends the session at Claude Code's `idle` state, which the CLI reports
only when no background agent runs and no notification is queued, and yields
its result there. The `submit_turn` reader therefore also runs once, at that
end, and the nudge round it may start is the same one as before. A Stop with no
`background_tasks`, and a harness whose Stop input has none (codex), asks as
before. The chassis's `[deadline]` note, posted one minute before the process
deadline (`BUGS.md` `L535`), may start a round: unlike `NUDGE_TEXT`, it tells
the agent that a reply is due now, so a turn it reaches is not stale.

## Format, checked where it can fail

"The intended format" is checkable only where text is an argument. The door
(`ipc/ipc.go:1250` `reply`, `:1227` `send`, `:1359` `post`, the `caption` of
`:1282` `send_file`) refuses, before any send, with the reason as the tool
error:

| Check                                            | Refusal reason                                                                             |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------ |
| `text` empty after trim                          | `text is empty`                                                                            |
| `text` contains the literal `$WEB_HOST`          | `resolve $WEB_HOST with echo before you send it` (`ant/CLAUDE.md:151`)                     |
| `text` contains `/home/node/` or `/var/lib/www/` | `a container path is not a link; translate it (self/web-routing.md)` (`ant/CLAUDE.md:200`) |

Each is string containment on an argument: exact, zero false positives, loud to
the agent because a refused call returns its reason. Length is not checked
here — the core skill owns it per surface (`ant/skills/self/messages.md`),
and `teled/bot.go:439` already chunks at the platform limit. A check on prose
style is a regex over unbounded text, declined (`44-delivery-contract.md`
§"The detector: state, not text").

## Narration

The agent runs unattended and must be told so once, in the resident manual,
because breaking it fails silently (`34-resident-prompt` is the residency
rule). The section below replaces `ant/CLAUDE.md:125-136` "Status updates" and
`:95-97`; line `:75` loses its `<think>` clause.

```markdown
# Unattended

No one reads this terminal. Text you print is your thinking: it reaches nobody
and is kept only in your transcript. A user hears you only through a tool
call — `reply` (a `like` counts) for the answer, `<status>…</status>` blocks
for progress while you work. Silence is calling neither; it is allowed when
nobody addressed you.

On addressed work, narrate with `<status>` before each major step — under
100 chars, lowercase, no period. For a silent scheduled task whose artifact
is the work, emit no `<status>` and call no delivery tool. A `<status>`
promises an answer: end that turn with `reply`.

When you were addressed and end without a reply, you are prompted once; after
that the user is told you did not answer.
```

`<status>` stays the one interim declaration: ant extracts it once
(`ant/src/index.ts:161` `statusBlocks`) on every harness and `submit_status`
renders it per `46-turn-progress.md` §"The declaration" — one message edited
in place where the channel can edit, nothing elsewhere. routd's second reader
(`routd/turns.go:65`) goes with the prose path, so the `L438` shape cannot
return; no third reader is added. The `TodoWrite` hook
(`../5/24-live-tasklist-status.md`) is unchanged.
A `status` tool was declined (§"Declined"): pi has no tools, and a second
interim path beside the block is the drift `L438` already cost.

The final text is a `reply`; a progress message is never the answer
(`5/24-live-tasklist-status.md` §"Design", the `verb=status` exclusion).

## What leaves

| Deleted                                                                                                                                                | Why it has nothing left to do                                                                                               |
| ------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| `routd/turns.go:50` `appendAndDeliver`                                                                                                                 | one production caller, the fallback branch                                                                                  |
| `router/router.go:163` `silentRefusalRe`, `:174` `trailingRefusalRe`, `:181` `FormatOutbound`, `:192` `ExtractStatusBlocks`, `:207` `StripThinkBlocks` | each has one production caller, `appendAndDeliver` (measured `23faf1dc`)                                                    |
| `Result` on `ipc.TurnResult` (`ipc/ipc.go:121`) and `apiv1.TurnResult`; `result` in `SubmitTurnPayload` (`ant/src/mcp.ts:22`)                          | prose never crosses the wire; the harness transcript holds it                                                               |
| `obs/metrics.go:153` `RecordProseFallbackTurn`, `arizuko_prose_fallback_turns_total`                                                                   | the branch it counted is gone; `arizuko_nudged_turns_total{folder,outcome}` replaces it as the number that prices the round |
| `ant/src/claude.ts:438` `summarizeMaxTurns`; the text of `ant/src/backend.ts:103` `timeoutFrame`                                                       | their product was prose for the prose path; the frame keeps `timedOut: true`, routd sends `timedOutNotice`                  |
| the `<think>` convention in `ant/CLAUDE.md` and the output styles                                                                                      | all prose is thinking                                                                                                       |

`arizuko_silent_turns_total` keeps its meaning — a clean run that never
submitted and never replied, a malfunction — so `obs/README.md:91` stays true.

## Migration

The original proposal used a migration version bump
(`217` at `23faf1dc`) → `218`, with a proposed
`ant/skills/self/migrations/218-v0.71.0-voluntary-reply.md`:

```markdown
## v0.71.0 — the voluntary reply

Your printed text no longer reaches the chat. It is your thinking and stays
in your transcript. The user hears you only through `reply`, `send`,
`send_file`, `send_voice` or `like`.

- Answer with `reply`. A reaction is an answer. Printing the answer delivers nothing.
- Stay silent by calling nothing. `<think>` tags are not read.
- Narrate with `<status>…</status>` while you work; it still renders as the ⏳ message.
- When you were addressed and end without a reply, you get one prompt. Reply
  then, or the user is told you did not answer.

Re-read `~/.claude/CLAUDE.md` § Unattended.
```

The proposed bump would have queued an auto-migrate turn per folder. The
migration skill was removed; that proposed trigger is not part of the current
package flow.

## Tier

Hot tier, agent-facing, MCP-only: `check_turn` sits with `submit_turn` and
`submit_status` as a hidden method on the agent socket. No table, no resreg
resource, no dashboard row — the one new stored fact is two attributes on the
existing turn context (`addressed`, `nudged`), owned by routd like
`retry_count`.

## Acceptance

| Proof                                                                                                                                                                            | Where                                                                             |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| the outcome table, one test per row, each red before the change                                                                                                                  | `routd/fixes_test.go` replaces `TestSubmitTurnResult*` (`:1143-1234`)             |
| the addressed table, one test per row                                                                                                                                            | `routd/loop_test.go`                                                              |
| one nudge per turn across both readers                                                                                                                                           | `routd` `check_turn` then `submit_turn` on one turn: the second hands out nothing |
| the door refuses each format row; mutate the literal and the test goes red                                                                                                       | `ipc/ipc_test.go`                                                                 |
| a nudge round keeps the turn id and the continuity token                                                                                                                         | `ant/src/index.test.ts`                                                           |
| a `Stop` row renders to `block` on claude, to `hooks.json` on codex, to a log line on pi                                                                                         | `ant/src/claude.test.ts`, `codex.test.ts`, `pi-extension.test.ts`                 |
| pi is refused by name                                                                                                                                                            | `ant/src/backend.test.ts`                                                         |
| an addressed web turn answers                                                                                                                                                    | `anteval` `rest-roundtrip` (`anteval/cases/anteval.toml:228`), unchanged          |
| live on sloth: the `L445` comment repeated posts nothing and logs silent-by-design; a mentioned comment answers; a turn told to print its answer is nudged once and then answers | the diary of the deploy, with comment ids and journal lines                       |

## Unverified

| Claim                                                                                                                                                                                                                                                                         | Probe                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | If false                                                                                                                |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| The SDK's bundled binary (`@anthropic-ai/claude-agent-sdk` 0.3.278 in `ant/`, `@latest` in the image at `ant/Dockerfile:189`) fires a `Stop` hook passed in `query()` options, `decision: 'block'` continues the conversation, and the second fire carries `stop_hook_active` | **MEASURED 2026-10-18**, bundled binary 2.1.278 (SDK 0.3.278), `query()` with `settingSources: []` and a `Stop` hook callback writing a marker, against a local mock Messages API (the host has no claude login, so the model's own compliance is not measured). Fires: yes, once per stop. Block continues: yes, `{decision:'block',reason}` ran a second model request carrying the reason as a system message and the result frame was the second reply; `hookSpecificOutput.additionalContext` also continues, delivered as a user `<system-reminder>`. `stop_hook_active`: `false` on fire 1, `true` on fire 2 | claude has no early self-correction; the `Stop` binding is not shipped and the `submit_turn` round carries claude alone |
| The codex in the ant image (`ant/Dockerfile:171`, unpinned) fires a `Stop` entry from `hooks.json` — the host's 0.162.1 binary carries `stop_hook_active`, and the owner's host `hooks.json` binds one                                                                        | **MEASURED 2026-10-18**, temp `CODEX_HOME` with a `Stop` entry in `hooks.json` (no `version` key), `codex exec --dangerously-bypass-hook-trust`, real model. Host 0.162.1 and `arizuko-ant:latest` 0.155.1 give the same answers. Fires: yes, with `last_assistant_message`. Block continues: yes, the model answered the reason (`PROBE-OK`) as a second `agent_message`. `stop_hook_active`: `false` on fire 1, `true` on fire 2. A `hooks.json` with a `version` key fires zero times                                                                                                                            | codex has no early self-correction; `codexHooksConfig` writes no `Stop`                                                 |
| The rate of prose-only turns — the number `44-delivery-contract.md` §"The gate" waits on — was never read; `arizuko_prose_fallback_turns_total` has existed since 2026-09-20 and no window was measured                                                                       | none; the owner's decision replaces the gate                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | the extra round is costlier than priced; `arizuko_nudged_turns_total` prices it after the fact                          |
| Fewer than 5 % of nudged rounds end silent (the second gate of `44-delivery-contract.md`)                                                                                                                                                                                     | `arizuko_nudged_turns_total` after a week on sloth                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | the notice carries the load and the manual text, not the mechanism, is what to fix                                      |
| codex reaches `reply` on every live turn through `-c mcp_servers.*`                                                                                                                                                                                                           | `anteval --smoke` with `ARIZUKO_BACKEND=codex`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | codex is in pi's position and is refused the same way                                                                   |

## Declined

- **A `status` tool.** pi has no tools; `../5/24-live-tasklist-status.md` moved progress off model-invoked
  mechanisms on purpose; a tool beside the block is a second interim path.
- **A `silent` tool.** Silence is the absence of a call. A tool makes silence
  something the agent can forget to do.
- **A delivery count inside the hook.** A second declaration of **delivered**;
  it drifts from `TurnHasBotReply` on the next verb.
- **A nudge on a turn that was not addressed.** The `L445` silence is correct
  and a nudge there spends a round to confirm it.
- **Regex over the prose to tell silence from forgetting.** The `L329` sentence
  beat a seven-alternative list; the state in §"Addressed" cannot be phrased
  around.
- **Delivering harness-synthesised text.** A timeout summary is prose; routd's
  notice is the one renderer of "the turn did not finish".
- **Widening `chanlib.BotHandler` or `runed.Runtime`.** No channel verb and no
  substrate fact changes; the mark and the edited message stay with
  `46-turn-progress.md`.

## Pointers

- `routd/turns.go:204` `recordTurnResult` — the enforcement point; `:218` the
  branch this contract replaces with the outcome table; `:50` the deleted path.
- `routd/loop.go:632` `resolve` — the one declaration of **addressed**;
  `routd/dispatch.go:353` `PutTurnContext` stores it.
- `store/turn_context.go:65` `TurnContext`, `:215` `setTurnAttrs` — `addressed`
  and `nudged` land here, beside `retry_count`.
- `store/messages.go:805` `TurnHasBotReply` — **delivered**; `:163`
  `defaultVerb` is why the `verb` attribute is always present for the query.
- `ipc/ipc.go:566` `handleSubmitTurn` — the response gains `nudge`; `:63`
  `RoutdFns.SubmitTurn` returns it; `check_turn` is registered beside `:610`.
- `ipc/ipc.go:1227`, `:1250`, `:1282`, `:1359` — the door and its three checks.
- `ant/src/index.ts:117` `deliverTurn`, `:357` the `result` that leaves the
  payload, `:468` the turn id a nudge round keeps.
- `ant/src/mcp.ts:44` `rpc` — returns the response body so a nudge can ride it.
- `ant/src/policy.ts:27` `PolicyEvent`, `:59` `bindings` — the `Stop` row;
  `ant/src/claude.ts:283` `asHookCallback`, `ant/src/codex.ts:260`
  `codexHooksConfig`, `ant/src/pi-extension.ts:14` `PI_HONOURS` — its three renderers.
- `ant/src/backend.ts:119` `selectHarness`, `ant/src/pi.ts:216` — the pi refusal.
- `ant/CLAUDE.md:75`, `:95-97`, `:125-136` — the manual lines §"Narration" replaces.
- `ant/skills/progress/SKILL.md`,
  `ant/skills/self/mcp.md` §"Messaging" — the skills that teach the door.
- `obs/metrics.go:153` — the counter that leaves; its replacement is named in
  §"What leaves".
- [`44-delivery-contract.md`](44-delivery-contract.md) — the problem analysis,
  the L329 measurement, the nudge-as-wire-object argument, the detector
  argument.
- [`46-turn-progress.md`](46-turn-progress.md) — the renderer `<status>` and
  `TodoWrite` feed; this spec removes its second reader and changes nothing in
  its rendering.
- `BUGS.md` `L329`, `L149`, `L150`, `L445` — the measurements.
