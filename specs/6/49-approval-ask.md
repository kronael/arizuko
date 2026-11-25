---
status: shipped
---

# specs/6/49 — approvals: one line to ask, one rule to stop asking

An approval is one chat line that asks a human for one tool call, with three
buttons, and a standing answer that stops the question for that kind of call.
This spec fixes the text of the line, what `/always` writes, the one renderer
that owns both, and how release defaults reach a running instance.

The hold renderer, declared always keys, notices, verdicts, REST fields and
binary defaults have tests in `routd/hold_always_test.go`,
`routd/hold_e2e_test.go`, `dashd/approvals_page_test.go` and `auth/layers_test.go`.

## Approval text and standing rules describe one call

A standing rule for `like` must pin `reaction` and leave `targetId` free.
Pinning the message id covers only that message. The notice must state the
rule the verdict writes. Every surface must use that same text.

Release defaults must reach running instances without stored copies that hide
later policy changes. Local exceptions remain the operator's data.

## Decision 1 — the gate asks, in the agent's words

The gate asks, not the agent. The call is suspended at the socket
(`template/web/pub/arizuko/reference/grants.html`), so the agent
cannot speak until the verdict. A question the agent asked before the call
would be answered twice: once to the agent, once on the gate's notice. A chat
answer to the agent is not a verdict: a verdict needs an `admin` grant on the
held row's folder (`routd/pending_actions.go:346`), and the agent's socket has
no face of `pending_actions`.

The line reads as the agent's request, not as a log line. The tool declares
how one of its calls reads (§"The hold shape"), and the renderer
writes the folder, that text, and the id:

    ⏸ rhias/nemo wants to react 👀 to message 156 — ok? (647ec18e)

The agent reports a refusal from `verdictRefusal` in `routd/pending_actions.go`.

## Decision 2 — `/always` pins the kind of call, not the call

A standing answer covers a kind of call. Each tool declares which arguments
name the kind, its always keys. `/always` writes a skip-approval row whose
params pin exactly those keys to the held call's values, and it leaves every
other argument free. `like` declares `reaction`, so `/always` on the first
hold writes `hold:mcp:like(reaction=👀)` at `deny` in rhias/nemo, and every
later 👀 reaction in that folder runs. `/always` exists only for a tool that
declares its always keys. A tool that declares none gets no `Always` button,
and the second line says so: `Always is not offered: schedule_task declares
no standing rule; an operator writes one.` The operator writes that rule with
`arizuko group grant` or on the grants page.

A value stays exact, never a glob. A value with `,`, `*` or outer spaces
refuses `/always` (`auth.ParamsSpec`, `auth/authorize.go:515`), and a
non-scalar argument is never pinned. A key the call carries no scalar value
for refuses `/always`. The row passes `auth.CheckGrant` like every other
write to the rules (`auth/README.md`). The row uses `deny` and commits with
the verdict
(`reference/grants.html` of the operator docs).

`/always` is offered on every hold of a tool that declares keys.

Keys for the tools with a measured case; the full set lives in the
declarations:

| tool           | ask                                    | always keys |
| -------------- | -------------------------------------- | ----------- |
| `like`         | react {reaction} to message {targetId} | `reaction`  |
| `dislike`      | dislike message {targetId}             | none        |
| `edit`         | edit message {targetId}                | none        |
| `delete`       | delete message {targetId}              | none        |
| any other tool | run `<tool>` and its scalar arguments  | none        |

A tool with `none` gets no `/always`.

## Decision 3 — two lines and three buttons

The notice is the ask line, one line that says what `/always` means, and the
three verdicts as buttons. A green or red reaction resolves the row
(`template/web/pub/arizuko/reference/grants.html`), and
`/dash/approvals/` lists every held row with the portal banner counting them.

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

The grants page (`dashd/grants_admin.go:86`) and `arizuko group -i <inst> grants`
show the row with its coordinate and a revoke control.

## Decision 4 — one declaration, one renderer, every sink reads strings

Three artifacts own the approval, each in one place:

- **Hold policy** stays in `acl` rows read by `auth.CheckHold`
  (`auth/authorize.go:589`) and in the release defaults, one file
  `auth/defaults.go` (`auth/README.md`).
- **The hold shape** — how a call reads, and which arguments `/always` pins —
  is declared once, in `holdShapes` beside the renderer
  (§"The hold shape").
- **The renderer** is one function in routd that fills the derived fields of
  the pending row: `ask`, `matcher`, `rule_text` and `matcher_error`.
  `withMatcher` is that function. Ask placeholders use `expand.Expand`.

Every sink shows the renderer's strings and derives nothing: the chat notice,
the acknowledgement, the `/v1/pending_actions` row, and the approvals page,
which reads `ask` and `rule_text` from routd.

The agent-side text belongs in `ant/skills/self/mcp.md`. The gate runs the
approved call itself (`holdGate`, `routd/pending_actions.go`).

## Decision 5 — the skip list reaches a running instance with the binary

`auth/defaults.go` declares the skip list as default policy. `Policy.Allows`
reads matching local rows first. Defaults decide only when no local row
matches. The binary supplies the defaults at each deploy.
`SeedRoutdFloor` writes no ACL rows. A stored copy of a default is a local row
and decides before the default, so an operator removes it with
`arizuko group ungrant` (§"The stored rows on krons and sloth").

## Decision 6 — a reaction, an edit and a delete never wait

The floor lists `like`, `dislike`, `edit` and `delete`
(`auth/defaults.go`), and Decision 5 delivers it. A folder that must hold one
of them writes its own `hold:mcp:<verb>` row at `allow`; a local row decides
before the skip list (`auth/README.md`). A stored copy of a skip row defeats
that local hold, so an instance must hold none
(`reference/grants.html` of the operator docs).

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
(`ipc/ipc.go:1469`). The default allows `delete` inside those platform bounds, and the agent
has to be careful with it. An option to deny deleting messages the agent did not send is
possible and not built.

## The stored rows on krons and sloth

Read with `arizuko group -i <instance> grants` at arizuko v0.75.28 on
2026-11-25.

| Instance | Stored rows | Copy of a default                          | Local hold rules                                                                                                           |
| -------- | ----------- | ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| krons    | 35          | `role:operator * **` with the grant option | `folder:rhias/nemo`: `hold:mcp:like`, `hold:mcp:dislike`, `hold:mcp:edit`, `hold:mcp:delete` at `deny`, scope `rhias/nemo` |
| sloth    | 50          | `role:operator * **` with the grant option | none                                                                                                                       |

- Neither instance stores a `role:member` row. The 23 skip rows and the
  member verbs come from `auth/defaults.go`.
- No stored row has the effect `async` or a `targetId` param.
- The four `rhias/nemo` rows are a local exception. They skip approval for
  that folder whatever role the folder holds.
- The stored operator row is a local row with the grant option. Without it an
  operator who also holds a narrower local row cannot delegate inside that
  row's scope (BUGS `L937`), so it stays.
- Each `role:member` folder, 11 on krons and 20 on sloth, answers `CheckHold`
  false for the 23 skip tools and `Authorize` true for the member verbs.

## The hold shape

One plain map in `routd/pending_actions.go`, `holdShapes`, beside the renderer
that is its only reader:

```go
type holdShape struct {
	ask  string   // "react {reaction} to message {targetId}"
	keys []string // the arguments /always pins; nil offers no /always
}
```

- The map holds the four social verbs. `ipc.socialAct` and `resreg.Resource`
  do not carry it: routd is the one reader, and the field would widen two
  types for a fact only routd uses. A second reader is the moment to move it.
- A tool with no entry, a connector tool included, takes the default: `run`,
  the tool name, then its scalar arguments as `name=value`, each value cut at
  40 characters on one line, and no key.
- A placeholder names a scalar argument of the call; one the call does not
  carry stays visible as `{name}`. `TestWorldHeldToolAsksFillPlaceholders` checks every held world tool against its input schema.

The pending row carries four derived fields, filled by the renderer and never
stored: `ask`, `matcher` (the params spec the row would carry), `rule_text`
(the second line) and `matcher_error`. The matcher pins the declared keys only.

## Pointers

- `routd/pending_actions.go` — the renderer, `holdShapes` (the ask and the always
  keys per tool), the notice and the buttons.
- `routd/steer.go` — the one-line acknowledgements.
- `dashd/approvals_page.go` — shows `ask` and `rule_text`, derives nothing.
- `auth/defaults.go` — the skip list (`auth/README.md`).
- `template/web/pub/arizuko/reference/grants.html` §"Holding a call for a
  human" and `routd/README.md` — the operator text.
- `ant/skills/self/mcp.md` — the agent text delivered by the core package.

## Acceptance

- A held `like` renders one ask line under 100 characters, one `Always` line,
  and three buttons. `/always` on it writes `hold:mcp:like(reaction=👀)` at
  `deny` in the folder; the next 👀 reaction in that folder runs with no
  notice, and a ❤️ reaction is held.
- A held call to a tool with no keys shows no `Always` button, the second line
  says why, and `/always` on it is refused with the same text.
- The approvals page and the `/v1/pending_actions` row show the strings the
  chat showed, and dashd holds no function that reads `args`.
- An instance whose `acl` holds no skip row answers `CheckHold` from the skip
  list (`auth/README.md`).
- `TestWorld_HoldsOnlyWhatAPersonShouldSee` stays green.
- `make build && make lint && go test ./... -short` green, tests in the
  shipping commit, and the agent text in the packaged manual.
