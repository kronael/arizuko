---
status: partial
depends: [../5/19-hitl-firewall, ../5/32-acl-unified, 26-personal-mode]
---

# specs/6/49 — approvals: one line to ask, one rule to stop asking

An approval is one chat line that asks a human for one tool call, with three
buttons, and a standing answer that stops the question for that kind of call.
This spec fixes the text of the line, what `/always` writes, the one renderer
that owns both, and how the seeded approval floor reaches an instance that
already runs.

## Problem — nine holds, four dead rules, seventy lines

Measured on krons 2026-10-20 at `71fe79d5`, with `arizuko get krons
pending_actions` and `arizuko group krons grants` built from that tree.

- `pending_actions` holds 10 rows. 9 belong to the folder rhias/nemo, and
  every one is a `like` call. Seven are from 2026-10-20, between 19:38:24 and
  19:42:55; the owner answered each in 2 to 41 seconds, with `/always` on the
  first, the second and the sixth. One row from 2026-10-14 is `expired`:
  nobody answered, the agent's turn waited the full `HITL_WAIT` of 10 minutes,
  and only then could it tell the user "NOT RUN (id 854967a6)". That text is
  still in the agent's session three sessions later.
- One row holds every one of them: `folder:rhias/nemo hold:mcp:like rhias/nemo`
  with effect `async`, written 2026-08-25. The folder is not a world and holds
  no `hold:mcp:*`. `store.EffectAllowed` (`store/acl.go:112`) accepts `allow`
  and `deny` only, and `auth.namespacedAllows` (`auth/authorize.go:639`) reads
  every effect but `deny` as a hold, so the row holds and cannot be rewritten
  as it is.
- `/always` wrote four rows, each `chatJid=<chat>,reaction=👀,targetId=<n>`.
  `routd.withMatcher` (`routd/pending_actions.go:511`) pins every scalar
  argument, and `targetId` names one message, so each rule matched that one
  message and no later call. The owner stopped pressing `/always` after the
  third.
- One notice is 8 lines, about 400 characters, plus 3 buttons (`notifyHeld`,
  `routd/pending_actions.go:558`; `alwaysOffer`, `:592`). An `/always` answer
  adds 3 lines that quote a `remove_acl` call with five fields
  (`standingWritten`, `routd/steer.go:632`). Seven holds and their answers put
  about 70 lines into one chat in five minutes.
- The skip-approval floor (`store.skipApprovalFloor`, `store/seed.go:54`)
  already names `like`, `dislike`, `edit` and `delete`. krons never received
  it: `SeedRoutdFloor` (`store/seed.go:74`) writes into an empty `acl` only,
  and krons has carried rows since May. The 22 missing rows went in by hand on
  2026-10-20 (BUGS `L473`).

Three defects follow. The notice is long because it explains the rule it would
write. The rule is dead because it names the message. The floor that makes a
reaction free never reaches a running instance.

## Decision 1 — the gate asks, in the agent's words

The gate asks, not the agent. The call is suspended at the socket
(`5/19-hitl-firewall §"Blocking is the default (2026-09-02)"`), so the agent
cannot speak until the verdict. A question the agent asked before the call
would be answered twice: once to the agent, once on the gate's notice. A chat
answer to the agent is not a verdict: a verdict needs an `admin` grant on the
held row's folder (`routd/pending_actions.go:346`), and the agent's socket has
no face of `pending_actions`.

The line reads as the agent's request, not as a log line. The tool declares
how one of its calls reads (§"The hold shape"), and the renderer
writes the folder, that text, and the id:

    ⏸ rhias/nemo wants to react 👀 to message 156 — ok? (647ec18e)

The agent still reports a refusal in one sentence (`verdictRefusal`,
`routd/pending_actions.go:486`); nothing changes there.

## Decision 2 — `/always` pins the kind of call, not the call

A standing answer covers a kind of call. Each tool declares which arguments
name the kind, its always keys. `/always` writes a skip-approval row whose
params pin exactly those keys to the held call's values, and it leaves every
other argument free. `like` declares `reaction`, so `/always` on the first
hold writes `hold:mcp:like(reaction=👀)` at `deny` in rhias/nemo, and every
later 👀 reaction in that folder runs. A tool that declares no key writes a
tool-wide row, and the second line says so: `Always: every schedule_task call
in rhias/nemo runs without asking.`

A value stays exact, never a glob. A value with `,`, `*` or outer spaces still
refuses `/always` (`auth.ParamsSpec`, `auth/authorize.go:515`), and a
non-scalar argument is never pinned. A key the call carries no scalar value
for refuses `/always`. The row, its `deny` effect, its batch
with the verdict and its revocation do not change
(`6/26-personal-mode §"The mechanics behind `always`"`).

`/always` is offered on every hold, not after N holds. The owner pressed it on
the first hold and on the second. The rule's shape failed, not its timing, and
a counter is state nobody asked for.

Keys for the tools with a measured case; the full set lives in the
declarations:

| tool           | ask                                    | always keys |
| -------------- | -------------------------------------- | ----------- |
| `like`         | react {reaction} to message {targetId} | `reaction`  |
| `dislike`      | dislike message {targetId}             | none        |
| `edit`         | edit message {targetId}                | none        |
| `delete`       | delete message {targetId}              | none        |
| any other tool | run `<tool>` and its scalar arguments  | none        |

## Decision 3 — two lines and three buttons

The notice is the ask line, one line that says what `/always` means, and the
three verdicts as buttons. The reaction hint and the dashboard link leave the
chat: a green or red reaction still resolves the row
(`5/19-hitl-firewall §"Reactions — the universal answer (2026-09-03)"`), and
`/dash/approvals/` lists every held row with the portal banner counting them.
All nine measured holds were answered, or expired, in the chat.

    ⏸ rhias/nemo wants to react 👀 to message 156 — ok? (647ec18e)
    Always: every like call with reaction=👀 in rhias/nemo runs without asking.
    [✅ Approve] [⛔ Reject] [♾ Always]

When no exact rule exists, the second line is `Always is not offered: <reason>`
and the third button is absent. An adapter without buttons receives the same two lines, and
`chanreg.foldOptions` (`chanreg/httpchan.go:161`) appends one typed command
per verdict.

Each verdict is acknowledged in one line:

    ✅ ok (647ec18e) — the agent runs it now.
    ⛔ rejected (647ec18e).
    ✅ ok (647ec18e). Always: every like call with reaction=👀 in rhias/nemo runs without asking. Undo: revoke the hold:mcp:like skip row on the grants page.

The `remove_acl` call with its five quoted fields leaves the acknowledgement.
The grants page (`dashd/grants_admin.go:86`) and `arizuko group -i <inst> grants`
show the row with its coordinate and a revoke control.

## Decision 4 — one declaration, one renderer, every sink reads strings

Three artifacts own the approval, each in one place:

- **Hold policy** stays in `acl` rows read by `auth.CheckHold`
  (`auth/authorize.go:589`) and in the seed floor (`store/seed.go:32-58`).
  Nothing here changes.
- **The hold shape** — how a call reads, and which arguments `/always` pins —
  is declared once, in `holdShapes` beside the renderer
  (§"The hold shape").
- **The renderer** is one function in routd that fills the derived fields of
  the pending row: `ask`, `matcher`, `rule_text` and `matcher_error`.
  `withMatcher` is that function; `alwaysOffer`, `approvalURL` and
  `standingWritten` are gone.

Every sink shows the renderer's strings and derives nothing: the chat notice,
the acknowledgement, the `/v1/pending_actions` row, and the approvals page,
which drops its own copies `alwaysRuleText` and `alwaysCaveat`
(`dashd/approvals_page.go:214,223`). The agent's refusal text is unchanged.

The agent-side text ships with the change as a new migration under
`ant/skills/self/migrations/`. The current one, `197-hitl-firewall.md`, tells
the agent to re-issue an approved call; the gate runs the approved call
itself (`holdGate`, `routd/pending_actions.go:430`).

## Decision 5 — the floor reaches a running instance, one batch at a time

A floor batch is the set of rows the seed writes under one `GrantedBy` stamp
with one fixed instant: `system` the operator root, `system:4r` the member
verbs of 2026-08-01, `system:5r` the skip-approval rows of 2026-09-12
(`store/seed.go:80-96`). routd records each batch it applied, and at every boot
it writes the rows of each unrecorded batch with `putIfAbsent`, then the
record (§"Proposed — the floor record").

- A row an operator removed after its batch was recorded stays removed: the
  batch is recorded, so the boot writes nothing for it. The seed's reason for
  the empty-store gate holds.
- An instance born before the record exists counts a batch as applied when at
  least one `acl` row carries its stamp. On krons that rule applies `5r`,
  which no row carried before 2026-10-20, and skips `4r`. A row added by hand
  shares the coordinate `store.aclKey` builds, so `putIfAbsent` writes nothing
  over it and restamps nothing.
- The residual: an operator who removed every row of one batch before the
  record exists receives that batch once more, and removes it once more.
- An empty `acl` is the same rule: every batch is unrecorded and applies.

A new batch is a new stamp and a new instant in `store/seed.go`; nothing else
has to change for it to reach the fleet at the next deploy.

## Decision 6 — a reaction, an edit and a delete never wait

The floor already lists `like`, `dislike`, `edit` and `delete`
(`store/seed.go:35`), and Decision 5 delivers it. A folder that must hold one
of them writes its own `hold:mcp:<verb>` row at `allow`, and the member skip
row beats it only when the operator keeps that skip; BUGS `L273` records that
trade and stays open.

The platforms bind `edit` to the agent's own messages: Telegram
`editMessageText` (`teled/bot.go:704`), Discord `ChannelMessageEdit`
(`discd/bot.go:644`) and Slack `chat.update` (`slakd/bot.go:1567`) edit only
the bot's own messages, and whapd sends `edit` with `fromMe: true`
(`whapd/src/server.ts:413`). `delete` is own-only on Slack, `chat.delete` with
a bot token, and on WhatsApp, `fromMe: true` (`whapd/src/server.ts:395`).
Telegram `deleteMessage` (`teled/bot.go:677`) and Discord
`ChannelMessageDelete` (`discd/bot.go:607`) delete any message when the bot
holds the platform's moderator permission, `can_delete_messages` and
`MANAGE_MESSAGES`. These are the platforms' documented rules, not re-measured
here. No adapter checks authorship, and the tool text says the platform does
(`ipc/ipc.go:1469`). The default allows `delete` inside those platform bounds.

## The hold shape

One plain map in `routd/pending_actions.go`, `holdShapes`, beside the renderer
that is its only reader:

```go
type holdShape struct {
	ask  string   // "react {reaction} to message {targetId}"
	keys []string // the arguments /always pins; nil pins none
}
```

- The map holds the four social verbs. `ipc.socialAct` and `resreg.Resource`
  do not carry it: routd is the one reader, and the field would widen two
  types for a fact only routd uses. A second reader is the moment to move it.
- A tool with no entry, a connector tool included, takes the default: `run`,
  the tool name, then its scalar arguments as `name=value`, each value cut at
  40 characters on one line, and no key.
- A placeholder names a scalar argument of the call; one the call does not
  carry stays visible as `{name}`. A test that renders every held tool of a
  world turn and fails on an unfilled placeholder is not built.

The pending row carries four derived fields, filled by the renderer and never
stored: `ask`, `matcher` (the params spec the row would carry), `rule_text`
(the second line) and `matcher_error`. The field keeps its name `matcher`;
`unmatchable` is gone, since the rule pins the declared keys and no others.

## Proposed — the floor record

One entity in the routd partition, `acl_floor`, one item per batch id with
`applied_at`. `SeedRoutdFloor` reads it, applies each unrecorded batch with
`putIfAbsent`, and writes the item. The bootstrap rule of Decision 5 runs only
when the entity is empty and `acl` is not.

The record is not a manifest resource: `arizuko export` carries the `acl` rows
themselves, and an `apply` that removes a floor row is an operator decision
the record must not undo.

## Pointers

- `routd/pending_actions.go` — the renderer, `holdShapes` (the ask and the always
  keys per tool), the notice and the buttons.
- `routd/steer.go` — the one-line acknowledgements.
- `dashd/approvals_page.go` — shows `ask` and `rule_text`, derives nothing.
- `store/seed.go` — the batches and the floor record.
- `template/web/pub/arizuko/reference/grants.html` §"Holding a call for a
  human" and `routd/README.md` — the operator text.
- `ant/skills/self/migrations/` — the agent text, with the
  `MIGRATION_VERSION` bump.

## Acceptance

- A held `like` renders one ask line under 100 characters, one `Always` line,
  and three buttons. `/always` on it writes `hold:mcp:like(reaction=👀)` at
  `deny` in the folder; the next 👀 reaction in that folder runs with no
  notice, and a ❤️ reaction is held.
- An `/always` on a tool with no keys writes the tool-wide row and the button
  label names every call of the tool.
- The approvals page and the `/v1/pending_actions` row show the strings the
  chat showed, and dashd holds no function that reads `args`.
- An instance with a `system:4r` row and no `system:5r` row receives the 23
  skip rows and the `acl_floor` record at the next routd boot; a second boot
  writes nothing; a floor row removed after that stays removed.
- `TestWorld_HoldsOnlyWhatAPersonShouldSee` stays green.
- `make build && make lint && go test ./... -short` green, tests in the
  shipping commit, and the migration file with the version bump.

## Open questions

1. The tool-wide default for a tool with no keys is an authorization widening
   one button away. The alternative refuses `/always` until the tool declares
   its keys. Decided: tool-wide, and the Always line names every call of the
   tool.
2. Must routd bind `delete` to the agent's own messages, by the `BotMsg` flag
   of the `messages` row the platform id names? The cost is one store read per
   delete, and a message routd never recorded cannot be deleted.
3. The dashboard link leaves the chat line. Decided: it leaves on every
   adapter; `/dash/approvals/` stays the operator's list.
4. `5/19-hitl-firewall §"Blocking is the default (2026-09-02)"` documents an
   `async` effect that `store.EffectAllowed` refuses and routd does not read.
   The paragraph and the CHANGELOG entry need a BUGS entry and a rewrite.
5. Live rows on krons, the operator's: the `async` row on rhias/nemo and the
   four `targetId` rules match nothing useful and can go.
6. Should the tool listing tell the agent which tools wait for a human, so the
   agent can say so before a long action? The cost is one `CheckHold` per
   tool per listing.
