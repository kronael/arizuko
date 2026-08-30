---
status: draft
depends:
  [
    ../5/19-hitl-firewall,
    ../5/13-ext-mcp,
    ../5/31-identity-pairing,
    ../5/32-acl-unified,
    ../5/35-hermes-integration,
    ../5/A-primitives-framing,
    27-git-channel,
  ]
---

# specs/6/26 — personal mode: one human, one terminal, one approval per call

> A single operator wants their agent (`atlas`) without a chat platform: talk
> to it from a terminal, let it act on their GitHub, and answer each risky call
> with **yes / no / always / never** — in the channel or in a browser.
>
> Almost all of it already ships. This spec records what that is, refuses two
> things that look like the answer and are not (MITM, a bearer approval link),
> and names the three gaps still open: a terminal REPL, the auth work behind
> the standing verdicts (`CheckHold` cannot express an exception, `add_acl`
> cannot write a shaped row, and the authz path drops arguments), and the
> generic options fold in the adapters.

## Why not run Hermes directly

Because the harness is the cheap half. [`5/35`](../5/35-hermes-integration.md)
measured it: Hermes's adapter interface and `chanlib.BotHandler` are the same
verb set differently spelled, and 26,102 of its 39,005 gateway lines are
platform adapters. What a harness does not carry is what a single human needs
the day after the first turn works — persistence with search
([`5/C`](../5/C-message-mcp.md)), an audit row per state transition written in
the mutation's own transaction ([`5/I`](../5/I-tool-call-logging.md)), a grant
checked on every tool call ([`5/32`](../5/32-acl-unified.md)), credentials
brokered on the host so they never enter the agent's environment
([`5/13`](../5/13-ext-mcp.md)), per-folder egress, and the hold gate
([`5/19`](../5/19-hitl-firewall.md)).

Personal mode is **not a smaller arizuko**. It is the same platform with one
folder and one human, and what you pay for is the approval gate and the
credential broker, not the chat.

## Decision 1 — personal mode is a topology, not a build flag

[`5/A`](../5/A-primitives-framing.md) decision 6 (user-locked 2026-08-09)
settles this: the smallest real deployment is `routd` + `dashd` + `onbod` +
`timed` with **no channel adapter at all**, and what crosses the boundary
crosses as REST or MCP. Personal mode adds no flag, no profile, no second code
path — it is that topology with one group folder. Slack is not removed; it is
simply not started.

## Decision 2 — the terminal is an MCP client, not a new daemon

`POST /chat/{token}/mcp` already serves `send_message`, `get_round` and
`get_round_status` over streamable HTTP (`webd/server.go:110`,
`webd/chat_mcp.go`), authorized by a `route_tokens` bearer that is
append-at-ONE-JID ([`5/W`](../5/W-webhook-routes.md)). Any MCP client — Claude
Code on the laptop included — attaches to the bot with a URL and a token, from
anywhere, with no operator shell and no DB access.

Two host-local paths stay beside it: `arizuko send <inst> <folder> "<msg>"
--stream` (operator-direct, writes the inbound straight to the DB) and
`arizuko chat <inst>` (an interactive Claude Code on the ROOT MCP socket — an
operator tool, not a conversation with the persona).

**Gap 1 — `arizuko talk`**: a REPL that is a thin client of `/chat/<token>/mcp`
and nothing else. It holds no DB handle and knows no folder; it sends, then
streams `/chat/<token>/<id>/sse`. The third consumer of one surface, never a
fourth path into the queue.

### The `talk` interface (decided 2026-08-30, auth model corrected same day)

Command-line first. The common case takes no arguments:

```
arizuko talk                    # REPL
arizuko talk -p "what broke?"   # one turn: send, print, exit
arizuko talk -p                 # same, prompt from stdin
arizuko talk @work -p "…"       # a named entry instead of the default
arizuko talk -g eng/sre         # a folder other than the resolved default
arizuko talk -t bugfix          # a different thread, same folder
```

**`-p` is one iteration of the REPL loop, not a second code path.** Same send,
same read, same printer — otherwise the one-shot and interactive forms drift,
which is what this gap's own "never a fourth path into the queue" forbids.

**Authenticate the caller; let the caller choose the destination.** `talk` holds
an authd-minted ES256 bearer and speaks `POST /mcp` — the surface that already
offers `list_groups`, `send` (gated by `auth.MatchGroups(grants, folder)`) and
`get_history`. It does NOT carry a route token.

> **An earlier revision of this section had `talk` hold a `route_tokens` bearer
> against `/chat/<token>/mcp`, and said "the group is never a parameter". That is
> REVERSED.** A capability token pins one JID and carries no identity, so every
> destination needs its own token and nothing can enumerate them. Authenticating
> the human and authorizing the folder is the mechanism arizuko already has
> everywhere else; a second one for the CLI is the duplication this spec exists
> to avoid. See BUGS `L139` for the wider collapse this implies.

It works today, unchanged: `proxyd.tryAuth` accepts `Authorization: Bearer
<ES256>`, verifies against authd's JWKs and stamps `X-User-*`; `webd.identified`
then requires that stamp plus `auth.ProxydTransit`. So `talk` points at the
PUBLIC proxyd URL, never at webd directly, and the token is minted by an existing
command — `arizuko token <inst> issue bearer <folder> --scope`.

**Config — minimal; the common case has no section header.**

```toml
# ~/.config/arizuko/talk.toml
url   = "https://krons.fiu.wtf"
token = "…"            # authd ES256 bearer, not a route token

[work]
url   = "…"
token = "…"
```

Precedence: flag > `@name` > top-level > `ARIZUKO_CHAT_TOKEN` > an error that
prints the file to write. Refused unless mode is `0600` — it holds a bearer, the
reason `psql` refuses a group-readable `.pgpass`.

**Folder resolution, in order:** `-g` > a `group` key on the entry > if
`list_groups` returns exactly one, that one, silently > otherwise an error
listing what the caller can reach. That last rule is what makes "one chat per
user needs no arguments" true without hardcoding a default.

**The thread default is the fixed literal `talk`** — no last-used cache, so there
is never a "which thread am I in?".

**Not in v1: an OAuth device flow.** The bearer is minted by the operator and
pasted once. A `gh auth login`-shaped handshake that obtains and refreshes its
own token is the natural next step and sits beside an OAuth server that already
runs (proxyd proxies `/auth/*` to authd, and a refresh-token cookie flow exists
for browsers). It is not needed to make `talk` work.

## Decision 3 — GitHub arrives as a connector, not as a channel

Two shapes exist and they are different products:

Decision 3 stands as the design and is runnable as of 2026-08-30. It is not the
ONLY route: the shell credential shipped 2026-08-29 (below) and is what atlas
used while the connector could not run.

- **Connector (the design; RUNNABLE since 2026-08-30).** The connector moved
  from a stdio subprocess to GitHub's HOSTED MCP endpoint
  (`https://api.githubcopilot.com/mcp/`), which is what closed BUGS `L132`: the
  subprocess spawned inside routd, whose image has no node and no
  `github-mcp-server`, and routd holds no docker socket by design. An endpoint
  needs no runtime. `template/connectors/github.toml` declares it;
  [`5/13`](../5/13-ext-mcp.md) shape 2 discovers its tools per folder with that
  folder's credential, narrows the folder's secrets to the keys the connector
  declared, gates each call on `mcp:<tool>`, renders the token into the
  Authorization header, and scrubs secret values from the result.
  `auth/surrogate/providers/github.toml` supplies the user-consented token
  ([`5/15`](../5/15-surrogate-oauth.md)) into the same `GITHUB_TOKEN` slot, so
  the agent acts as the human rather than as a shared PAT. GitHub as a **tool
  the agent calls**.
- **`gitd` (draft, [`6/27`](27-git-channel.md)).** GitHub as a **channel
  that calls the agent** — each repo a folder, each PR and issue a thread. The
  right design for a different want: an agent that watches a repo, not a human
  who asks their agent to open a PR.

Personal mode takes the connector. `6/27` is neither superseded nor folded
here.

### The host `.env` is the wrong door, and it fails silently

An operator reaching for "let atlas use `gh`" puts `GH_TOKEN` in the instance
`.env`. That reaches **nothing**: `readSecrets` (`container/runner.go`) iterates
`store.EnvProfileKeys` and passes only those five model credentials into a
container. Every other name in `.env` is an infra credential that stays on the
host by construction (`5/14` tier 3). Measured on marinade 2026-08-28 —
`GH_TOKEN` and `BK_TOKEN` are both present in `.env` and neither reaches the
agent. Nothing logs a warning, which is what makes it worth writing down.

A capability credential lives in the `secrets` table, folder- or user-scoped,
and reaches a connector subprocess through the broker per call
(`ipc.CallConnectorTool`), which is also what puts a `secret_use_log` row under
every use. `template/connectors/github.toml` and `buildkite.toml` carry the
wiring; the token goes in with `arizuko secret set` or `/dash/me/secrets`.

### The CLI route SHIPPED as a narrowed grant (v0.66.0, 2026-08-29)

This section refused it outright, and the operator overruled that: `gh` now
works. What ships is not the shortcut this spec argued against — a
`shell:env:<KEY>` acl row names ONE secret that may enter the container's shell
env, the broker resolves it per spawn, and no row means nothing crosses. Model
credentials are excluded even under `shell:env:*`, and the action is
exact-match, so an operator's `**` row does not confer it
(BUGS `L134` for the channel-scoping limit, `grant_action_needs_a_door` for why
`grantableAction` had to learn the namespace first).

**The three objections below still hold, and are now the stated bound rather
than the reason to refuse.** They are why the grant is per-key and why the PAT's
own scope is the real lock:

- **It cannot be gated.** `gh pr review --approve` is a shell string. arizuko's
  gate is `CheckHold` at the one `tools/call` interception; a Bash command is
  one tool call carrying opaque text, so "block PR approval" degrades to
  matching substrings, which `${A}${B}` defeats. An operator who asks for both
  a shell token and a block on approvals has asked for two things that cannot
  both hold.
- **The connector route gives the block for free** — and now runs. Each remote tool is its own
  broker tool name, so `hold:mcp:github_<tool>` suspends it for human approval
  (`5/19`, shipped) and a deny row refuses it outright. This is why Decision 3
  picks the connector: not because a CLI is inelegant, but because per-call
  authority is only expressible when the call has a name.
- **It widens the shell blast radius.** Anything in container env is readable
  from the agent's shell on pi and, unless scrubbed, on claude (BUGS `L131`).

**The real enforcement is the token's own scope.** An `acl` row refuses a tool;
only the credential bounds what that tool may do once called. A fine-grained PAT
without `pull_requests: write`, or a Buildkite read-only token, is the bound
that holds even if every arizuko gate is wrong.

### Open — bot identity is a decision this spec has not made

Decision 3 says the agent "acts as the human rather than as a shared PAT",
because `5/15` surrogate OAuth mints a user-consented token. That is the right
default for attribution and the wrong one for an operator who wants every
agent-made comment to carry a **bot badge**: GitHub renders that only for a
GitHub App installation, never for a PAT or a user OAuth token.

The two cannot both be true, so this is a fork, not a setting:

- **user-consented token (today)** — actions are attributable to a person,
  audit joins cleanly to `user_sub`, no bot badge.
- **GitHub App (net new)** — a real bot identity with a badge, per-permission
  scoping (approval can be withheld at the app), and installation-scoped repo
  access. Costs an app registration, an installation-token refresh path beside
  `5/15`'s user-token one, and it breaks the "the agent acts as you" framing
  this spec opens with.

**DECIDED 2026-08-28 (operator): both, in order — user-consented token now, App
later.** Ship the connector route so atlas can act at all, and revisit the App
once the tool set it actually uses is known, accepting that identity changes at
that point. The badge requirement is real and stands; it is deferred, not
dropped. Two consequences to keep visible:

- Until the App lands, every agent action attributes to the consenting human.
  Anyone reading the repo cannot tell atlas from its operator.
- "Cannot approve PRs" therefore rests entirely on the token's own scope in
  phase one — mint the PAT without `pull_requests: write`. An `acl` deny row is
  the second lock, not the first, and neither is the badge.

## Decision 4 — MITM is declined; HITL is what you want

The two are routinely confused, so state both:

- **MITM — TLS interception of the agent's egress. DECLINED** 2026-08-18,
  reaffirmed 2026-08-20 ([`6/24`](24-crackbox-lightest.md) §"MITM stays
  declined"). Header injection was its only consumer, and a CA inside every
  container is a worse hole than the one it closes. `8/Z-egred-mitm.md` was
  deleted for reintroducing what `5/13` had already rejected. Metering does not
  need it either — a gateway is configured, not intercepted
  ([`6/23`](23-llm-gateway.md)).
- **HITL — a human in the middle of a TOOL CALL. SHIPPED** 2026-08-07
  ([`5/19`](../5/19-hitl-firewall.md)). A `hold:mcp:<tool>(param=glob)` acl row
  makes `CheckHold` suspend the call at the one `tools/call` interception in
  `ipc.serveConn`, write a `pending_actions` row and notify the chat. The
  original agent re-issues on approval, in its own turn, container, session and
  grants; release is one-shot and keyed on the canonical-JSON hash of the args,
  so edited args are held again.

So "granular approvals" is already granular — per tool, per argument glob, per
folder. Do not build a second gate.

## Decision 5 — four verdicts, and none of them is a memory

The operator answers a held call the way Claude Code asks: **yes · no · always
· never**. Two are one-shot rows, two also write `acl` rows, and both gates
re-read `acl` on every call ([`5/32`](../5/32-acl-unified.md) § "No caching,
deliberately"). The agent therefore stores nothing and remembers nothing — it
learns the standing answer from the OUTCOME of its next call.

| Verdict                 | The call in hand                       | Also writes                                                             | Next identical call    |
| ----------------------- | -------------------------------------- | ----------------------------------------------------------------------- | ---------------------- |
| **yes** (`/approve`)    | `approved` (one-shot, args-hash keyed) | —                                                                       | held again             |
| **no** (`/reject`)      | `rejected`                             | —                                                                       | held again             |
| **always** (`/always`)  | `approved`, exactly as `yes`           | skip-approval: `hold:mcp:<tool>(<params>)`, `effect='deny'`             | runs inline, no notice |
| **never** (NOT SHIPPED) | `rejected`, exactly as `no`            | skip-approval **plus** `mcp:<tool>`, `effect='deny'` — BOTH, atomically | denied, never held     |

`always` is live on both resolution faces: chat `/always <id> [note]`, and
`standing=true` on `POST /v1/pending_actions/{id}/approve` (an ARGUMENT of
approve, never a fourth endpoint — there is no third status to name), which is
what the approvals page's third button posts.

**`always` and `never` are `yes`/`no` plus a rule — never a third verdict on
the row.** `pending_actions` keeps its states and `resolvePendingAction` keeps
rejecting unknown verdicts (`TestResolvePendingAction_RejectsUnknownVerdict`);
the standing answer lives in `acl`, where every other standing answer already
lives. The verdict and its acl rows commit in the SAME transaction as the
resolution message, the way `routd.resolveHoldTx` already commits the verdict
and its trigger together — otherwise a crash leaves a call approved with no
standing rule, or a rule with no approved call.

**Both standing verdicts write the skip-approval row; `never` adds a deny.**
That is not symmetry for its own sake: `CheckHold` fires BEFORE authz
(`ipc/ipc.go:504`, the ordering `5/19` chose so approval never substitutes for
authorization). A `never` that wrote only the `mcp:` deny would leave the hold
rule matching, so the call would be held again and the operator would answer
the same prompt forever. The deny alone does not end the question; the
skip-approval row is what stops re-asking it.

**Name it "skip approval", never "deny the tool".** The row denies the `hold:`
action, not the tool — `deny` is the right effect because `Authorize` treats
every other value as ALLOW (`authorize.go:73`), so inventing a third effect
would be the `5/19` Hazard 1 trap in new clothes. But an operator reading
"deny" next to a button that lets a call through will misread it. The wire
keeps `deny`; every surface says **skip approval**.

### Gap 2 — three things had to change before `always` could be written

Found by review 2026-08-24 (codex), each confirmed in code. **All three are
closed as of 2026-08-30, and `always` ships; `never` does not** — see the next
section for the one reason that is not about this gap.

1. **`CheckHold` could not express an exception. CLOSED 2026-08-29.** It
   returned `true` on the FIRST `hold:` row matching action, scope, predicate
   and params and never read `Effect`. It now shares `namespacedAllows` with
   `ShellEnvKeys` — one deny-wins two-pass loop, so a matching `effect='deny'`
   row means DO NOT HOLD. Deny-wins is order-independent, so a broad
   `hold:mcp:*` plus a narrow skip-approval row behaves correctly, and it is
   absolute: any inherited matching deny beats every hold rule regardless of
   specificity, and a later, narrower hold cannot restore the approval.
   **Revocation is removing the skip-approval row**, which is why `/always`
   answers with the exact `remove_acl` call that undoes it, and why the
   approvals page says the same. Pinned by
   `auth/checkhold_test.go` — the exception, the broad-rule case, both insert
   orders, the inherited skip, and `TestCheckHold_OperatorStarDoesNotHoldEverything`
   still green.
2. **`add_acl` could not write a shaped row, and its delegation check was
   bypassable. CLOSED.** The effect half shipped 2026-08-29 (BUGS `L93`); the
   `params` half 2026-08-30. `grantACLTx` and `revokeACLTx` now take the
   `core.ACLRow`, and the Gate builds its `auth.Delegate` candidate with the
   SAME `aclArgsRow`+`aclDefaults` pair the write uses, so the row judged is
   the row stored — `Delegate` would otherwise have read an empty `params` as
   no constraint (`auth.paramsCover`) and let a caller holding only
   `mcp:t(repo=mine/*)` write the unnarrowed `mcp:t`. `revokeACLTx` matches
   `params` exactly instead of hardcoding `''`, so a row this face can write is
   a row it can remove; leaving that hardcode would have re-created BUGS `L79`
   the day `params` became writable. `auth.ValidateParams` refuses a spec
   `paramsMatch` reads differently from how it is stored — the stored string is
   also the revoke key.
3. **A shaped `never` cannot work, so no surface offers one. PINNED.** The hold
   path passes arguments (`stringArgs(args)`), but the authz path does not:
   `authorizeCall(name, nil)` (`ipc/ipc.go`) hands `Authorize` a nil params
   map, and `paramsMatch` requires each named key to be PRESENT, so a deny row
   shaped `mcp:<tool>(repo=x/*)` matches nothing and denies nothing.
   **v1: `always` may be shaped, `never` is tool-wide only.**
   `routd/hold_shaped_never_test.go` pins the REASON, not just the rule: the
   same shaped deny is inert under nil params and decisive once arguments
   travel, and the hold path is shown carrying them. Removing the restriction
   therefore requires removing the reason. Carrying arguments into the authz
   path is its own change, with every tool in its blast radius.

### Revocation needs provenance, and `acl` has none

Undoing `always` is removing the skip-approval row. Undoing `never` is not
symmetric: remove only the skip row and the tool deny remains, so the call is
refused and no approval can revive it. Remove both and you may delete a deny
that existed BEFORE the verdict and means something else — `acl` rows carry no
verdict identity, so two identical denies are indistinguishable.

So `never` must record which rows its verdict created, and revocation must
remove exactly those. An operator surface listing raw independent `deny` rows
is not safe discovery: it cannot show which deny is a verdict and which is
standing policy. This is the last unresolved piece of the design, and it is why
`never` does not ship alongside `always`.

**`always` shipped 2026-08-30 without it**, because its revocation is not
symmetric: it writes ONE row, on the `hold:` action, and removing that row is
the whole undo — there is no second row whose provenance is in doubt. The two
surfaces say which row: chat answers `/always` with the `remove_acl` call that
undoes it, and the approvals page points at the group's grants table. The
`always` writer records `granted_by = "always:<reviewer>"`, which is a trace,
NOT the provenance this section asks for — nothing reads it, and it does not
survive the row being re-created by hand. Do not build `never` on top of it.

### The predicate is part of the verdict

A held call carries concrete arguments; `always` over ALL arguments is a large
widening from one sample. The notice states the row it will write — principal,
action, scope, and the exact parameter matcher — and writes exactly that:

```
/always ab12 → skip approval for
  github_create_pull_request(repo=kronael/arizuko)  in folder solo
  (arguments it does not name are unconstrained)
  (no rule can constrain files)
  (skips approval only; the mcp: grant still has to allow the call)
```

Two properties of the matcher must be in that text, because both widen it
beyond what an operator assumes: a rule constrains only the parameters it
NAMES, so unnamed arguments are unconstrained, and non-scalar arguments are
skipped entirely by `stringArgs` and can never be matched. An `always` whose
full effective match was not shown and accepted is a grant nobody agreed to,
and the surface refuses to write it.

**The matcher is EXACT VALUES, never a glob** — an earlier draft of this
section wrote `repo=kronael/*`, which no surface can offer honestly: widening
one sample into a pattern is an operator judgement, and a verdict button cannot
make it. So the rendered rule quotes the call's own scalar arguments, and a
value carrying `,` (which would split the rule), `*` (which would glob it) or
surrounding whitespace (which the reader strips) cannot be stated at all —
`always` is REFUSED there and the surface says why instead of showing a button.
An operator who wants a glob writes the row themselves with `add_acl`, where the
widening is a thing they typed. Implementation: `auth.ParamsSpec`.

**One renderer, three sinks.** `routd.withMatcher` derives `matcher` /
`unmatchable` / `matcher_error` from the held row and is the only place they are
computed; the chat notice, the `/v1/pending_actions` row and
`resolveHoldTx`'s writer all read them from there. The writer takes
`p.Matcher` and never builds one, so "refuses to write a rule it did not
display" is structural rather than a habit — and dashd renders routd's matcher
rather than re-deriving one from `args`, which is exactly how a page ends up
showing one grant and writing another.

## Decision 6 — approval delivery: one decision, three sinks

`5/19` shipped the decision and both resolution paths (`/approve <id>` in chat,
`POST /v1/pending_actions/{id}/approve|reject`, both funnelling through
`routd.resolveHoldTx` in one transaction). It deliberately did not ship the
DELIVERY the operator sees.

**In a browser — SHIPPED 2026-08-24.** The held notice carries
`<WEB_HOST>/dash/approvals/?id=<id>`, and the page moves that row to the top;
an id that already has a verdict, or none at all, is reported rather than
silently absent. With `WEB_HOST` unset the notice carries the chat commands and
no broken URL.

> An earlier draft proposed `route_tokens.kind = 'approve'`, a one-shot bearer
> bound to one `pending_actions.id`. **Rejected on review**: the notice lands in
> the chat the agent shares with its USERS, so a bearer there hands the verdict
> to everyone who can read the chat — exactly what `IsOperator` prevents.
> `5/35`'s rule is that a URL which travels never carries verb authority, and
> approving a tool call is verb authority. The link names WHICH decision; the
> page's existing operator gate names WHO.

**Gap 3 — in the channel.** `SendRequest.Options []Option{label,data}` exists
in neither `chanlib` nor `core` (verified 2026-08-24: zero hits), so today
every adapter shows plain text. It is the one generic vehicle: numbered text at
the existing `HasCap` fold when the adapter lacks `buttons`, native where it
has them; a press maps to an ordinary `InboundMsg` on the existing ingest, the
way `chanlib.InboundMsg.Reaction` already carries platform callbacks. Press and
typed command converge on the one `/approve` parser, consumed before any turn,
so a verdict never burns a model call.

Native render is worth having on three of four adapters, and the fourth is why
the text fallback is not a placeholder (measured 2026-08-24):

| Adapter   | Library                      | Native buttons                            | Cost                                                                                                                                                                                                                                                                                                                         |
| --------- | ---------------------------- | ----------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **teled** | telegram-bot-api v6.5.0      | inline keyboard; `CallbackQuery` in types | lowest — `callback_data` caps at 64 B, and `NewPendingID` is already 8 chars for this reason; must call `answerCallbackQuery` or the client spins                                                                                                                                                                            |
| **discd** | discordgo v0.29.0 (vendored) | `components.go` + `interactions.go`       | low — interactions arrive as `INTERACTION_CREATE` on the gateway it already holds; ack in 3 s. **Only on the bot path**: `discd/bot.go:47` also identifies as a user account, and interactions are an application feature                                                                                                    |
| **slakd** | hand-rolled HTTP             | Block Kit out via `chat.postMessage`      | highest — clicks do NOT come back through the Events API. They POST to a separate Interactivity Request URL, form-encoded with a JSON `payload`, needing 200 within 3 s: a second handler beside `POST /slack/events` (`slakd/server.go:41`), a proxyd route, and the Interactivity toggle in the app dashboard              |
| **whapd** | Baileys 6.7.21               | **none**                                  | blocked — the sendable union (`Types/Message.d.ts:141`) has no `buttons` option, only `buttonReply` and `poll`; the proto still parses `buttonsMessage` inbound, so there is a receive path and no send path. Upstream removed it; Meta deprecates Web-MD buttons for the Cloud API. A 4-option **poll** is the honest shape |

Four verdicts fit every one of these: 4 ≤ Discord's 5 buttons per row, a
verdict plus an 8-char id is far under Telegram's 64 B, and a poll takes four
options.

## Decision 7 — the reviewer stays the operator in v1

`5/19` gates resolution on `db.IsOperator` and left folder-scoped reviewer sets
out of scope. Personal mode is the case where reviewer, operator and the only
human coincide, so v1 suffices by construction. A team wanting per-folder
reviewers reopens `5/19`.

Unchanged too: the agent-socket MCP face of `pending_actions` stays unmounted —
the held agent approving its own call is not a gate. An open question, not a
defect.

## A second bound: the hold gate fails OPEN on an unreadable `acl`

`CheckHold` logs and returns `false` when the acl read fails
(`auth/authorize.go:392`) — nothing is held, and every gated tool runs. That is
the opposite of `5/19`'s other failure branch, where a hold that cannot be
RECORDED blocks the call anyway rather than silently defeating the gate the
operator asked for. Reversing it is not free: an acl read error would then
block every held-eligible tool call, which is an availability decision, not a
patch. Recorded as BUGS `L92`; the four verdicts inherit this bound and do not
widen it.

## Residual hole this does not close

Claude Code natively reads `.mcp.json` from the session cwd — the agent's own
home — and the SDK query passes neither `strictMcpConfig` nor an empty
`settingSources`, so a server the agent writes there is never seen by the
socket and no `hold:` rule can suspend its tools (BUGS `J14`). Every guarantee
above is bounded by that. The fix is `strictMcpConfig: true`, which also closes
plugin and agent-frontmatter MCP and so needs a decision, not a patch.

## Acceptance

Shipped 2026-08-24 (verified, tests in the shipping commit):

- `hold:mcp:<tool>(param=glob)` fires → `pending_actions` row lands `held` and
  the notice carries the `/approve <id>` text AND `/dash/approvals/?id=<id>`.
- The link opens the page with that row first; a resolved or unknown id is
  reported, not silently absent; `WEB_HOST` unset yields no broken URL.

Shipped 2026-08-29/30 — gap 2 closed and `always` live (`auth/checkhold_test.go`,
`auth/params_test.go`, `routd/acl_params_test.go`, `routd/hold_always_test.go`,
`routd/hold_shaped_never_test.go`, `dashd/approvals_page_test.go`):

- A `hold:mcp:<tool>` row plus a same-action row with `effect='deny'` and a
  narrowing: the matching shape runs inline, every other shape is still held,
  a broad `hold:mcp:*` does not resurrect the exception, insert order does not
  matter, an inherited skip beats a directly-held hold, and
  `TestCheckHold_OperatorStarDoesNotHoldEverything` stays green (gap 2.1).
- `add_acl` writes a row carrying params and `remove_acl` drops that same row
  by naming the narrowing exactly; `auth.Delegate`'s subset check runs on the
  SHAPED row, so a caller who may grant `mcp:t(repo=mine/*)` cannot write
  `mcp:t`; a narrowed row at scope `**` is an ordinary row and never operator
  membership; a spec the evaluator would read differently from how it is stored
  is refused (gap 2.2).
- `always` writes exactly the matcher the notice displayed — the same
  `withMatcher` output the notice, the REST row and the page all read — and
  refuses outright when no exact rule can be stated, leaving the call held
  rather than approved without its rule (gap 2.2).
- The notice and the approvals page both state the two widenings in words and
  name the arguments no rule can constrain; an unnarrowed rule announces that
  every call to the tool will run (gap 2.2).
- No surface offers a shaped `never` while `authorizeCall` passes nil params,
  and the test pins the REASON — the same shaped deny is inert under nil params
  and decisive once arguments travel — so removing the restriction requires
  removing the reason (gap 2.3).

Shipped 2026-08-30 — gap 1, `arizuko talk` (`cmd/arizuko/talk.go`,
`talk_config.go`, tests in `talk_test.go` + `talk_config_test.go`):

- `arizuko group <inst> grant <sub> '<folder>/**'` +
  `arizuko token <inst> issue bearer <folder> --sub user:<sub>` →
  `arizuko talk` holds a conversation with the persona from a second machine.
  The binary imports no `store`, opens no sqlite file and takes no instance
  argument: it POSTs `/mcp` and reads `/me/chats/<folder>/<thread>/sse`, both
  through the PUBLIC proxyd URL. The grant is a folder subtree, not operator
  scope — `**` is never required.
- `-p` is ONE ITERATION of the same loop the REPL runs: one `promptSource`
  feeds one `turn`, and a test asserts the one-shot form sends exactly one
  message through that same path.
- The thread default is the fixed literal `talk`; `-t` names another and the
  name reaches BOTH the send and the stream (a thread that reached only one
  would split the conversation in half).
- Config `~/.config/arizuko/talk.toml` is refused unless its mode is `0600`;
  precedence is flag > `@name` > top-level > `ARIZUKO_CHAT_*` > an error that
  prints the file to write and the two commands that fill it.

Still to build:

- **The token is minted by hand.** `talk` reads a bearer an operator already
  minted; there is no device flow, so a laptop is set up by copying a token
  across. `newTalkSession` is where an OAuth device grant would attach.
- `never` — blocked on verdict provenance, not on the writer. See § "Revocation
  needs provenance"; do not ship it on `granted_by`.
- An adapter with `buttons` renders the options natively and a press resolves
  the row; an adapter without renders numbered text and a numeric reply
  resolves the same row; whapd renders a poll (gap 3). Until then `always` is
  the chat command and the dashboard button, not a button in the channel.
- The github connector opens a PR under a surrogate-OAuth token; the token
  never appears in container env or in the tool result.
- `make build && make lint && go test ./... -short` green; tests in the same
  commit.

**Bounded by BUGS `L97`.** The one-shot half of every verdict — the agent
re-issuing the approved call — is a no-op today: the re-issue trigger is written
with a non-fixed-width timestamp and sorts behind the cursor that must pick it
up. `always`'s standing half is unaffected (the next call is simply not held),
but its `yes` half inherits `L97` exactly as `/approve` does.

## Out of scope

Repo-as-folder ingest (`6/27`) · per-folder reviewer sets (`5/19`) · TLS
interception (declined) · a bearer approval link (rejected above) · `mcpfw`
(`6/12`, waits for a third-party-MCP consumer not brokered through the socket)
· the model-credential gateway (`6/23`, orthogonal and independently worth
doing).

## Ties

`5/19` HITL (the gate) · `5/32` (the `acl` row the four verdicts write) ·
`5/13`+`5/14`+`5/15` (the credential path) · `5/31` (capability links are token
kinds) · `5/35` (why not Hermes) · `5/A` decision 6 (the topology) · `6/27`
(the other GitHub) · BUGS `J14`.
