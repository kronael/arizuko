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

**Gap 1 — `arizuko talk <url> --token <t>`**: a REPL that is a thin client of
`/chat/<token>/mcp` and nothing else. It holds no DB handle and knows no
folder; it sends, then streams `/chat/<token>/<id>/sse`. The third consumer of
one surface, never a fourth path into the queue.

## Decision 3 — GitHub arrives as a connector, not as a channel

Two shapes exist and they are different products:

- **Connector (shipped, use this).** `template/connectors/github.toml` declares
  the upstream GitHub MCP server; [`5/13`](../5/13-ext-mcp.md) shape 2 spawns
  it per call, narrows the folder's secrets to the keys the connector declared,
  gates the call on `mcp:<tool>` and scrubs secret values from the result.
  `auth/surrogate/providers/github.toml` supplies the user-consented token
  ([`5/15`](../5/15-surrogate-oauth.md)), so the agent acts as the human rather
  than as a shared PAT. GitHub as a **tool the agent calls**.
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

### Why the CLI route is refused, and it is the operator's own requirement

The tempting shortcut is to ship `GH_TOKEN` into container env so `gh` works in
Bash. It is refused, and not on purity grounds:

- **It cannot be gated.** `gh pr review --approve` is a shell string. arizuko's
  gate is `CheckHold` at the one `tools/call` interception; a Bash command is
  one tool call carrying opaque text, so "block PR approval" degrades to
  matching substrings, which `${A}${B}` defeats. An operator who asks for both
  a shell token and a block on approvals has asked for two things that cannot
  both hold.
- **The connector route gives the block for free.** Each remote tool is its own
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

Not decided here. Wanting the badge is a real requirement and it argues for the
App; nothing else in personal mode does.

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

| Verdict    | The call in hand                       | Also writes                                                             | Next identical call    |
| ---------- | -------------------------------------- | ----------------------------------------------------------------------- | ---------------------- |
| **yes**    | `approved` (one-shot, args-hash keyed) | —                                                                       | held again             |
| **no**     | `rejected`                             | —                                                                       | held again             |
| **always** | `approved`, exactly as `yes`           | skip-approval: `hold:mcp:<tool>(<params>)`, `effect='deny'`             | runs inline, no notice |
| **never**  | `rejected`, exactly as `no`            | skip-approval **plus** `mcp:<tool>`, `effect='deny'` — BOTH, atomically | denied, never held     |

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

### Gap 2 — three things must change before `always` or `never` can be written

Found by review 2026-08-24 (codex), each confirmed in code. None is large; all
three are load-bearing, and shipping the buttons without them ships a lie.

1. **`CheckHold` cannot express an exception.** `auth/authorize.go:398`
   returns `true` on the FIRST `hold:` row matching action, scope, predicate
   and params; it never reads `Effect`. Give it the deny-wins two-pass loop
   `Authorize` already has: scan every matching row, and a matching row with
   `effect='deny'` means do not hold. Deny-wins is order-independent, so a
   broad `hold:mcp:*` plus a narrow skip-approval row behaves correctly — but
   it is also absolute: any inherited matching deny beats every hold rule
   regardless of specificity, and a later, narrower hold cannot restore the
   approval. Revocation is removing the skip-approval row, and the operator
   surface must say so.
2. **`add_acl` cannot write a shaped row, and its delegation check is already
   bypassable.** `grantACLTx` (`routd/acl_resource.go:305`) INSERTs `params`
   and `predicate` as empty literals, so no rule this decision writes is
   expressible at all. The same writer carries a shipped defect the same review
   found: the candidate row handed to `auth.Delegate` hardcodes
   `Effect: "allow"` (`acl_resource.go:219`) while the INSERT takes the
   caller's own `effect` argument, so `Delegate`'s outright ban on delegating a
   deny row (`auth/delegate.go:51`) never sees a deny (BUGS `L93`). Fix both in
   one pass: the writer must carry `params`, and `Delegate` must judge the row
   that is actually written — effect included.
3. **A shaped `never` cannot work yet, so v1 must not offer one.** The hold
   path passes arguments (`stringArgs(args)`), but the authz path does not:
   `authorizeCall(name, nil)` (`ipc/ipc.go:1006`) hands `Authorize` a nil
   params map, and `paramsMatch` requires each named key to be PRESENT, so a
   deny row shaped `mcp:<tool>(repo=x/*)` matches nothing and denies nothing.
   A shaped `never` would therefore look recorded and refuse no call.
   **v1: `always` may be shaped, `never` is tool-wide only**, and the notice
   states that difference in words. Shaped `never` waits on the authz path
   carrying arguments — its own change, with every tool in its blast radius.

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

### The predicate is part of the verdict

A held call carries concrete arguments; `always` over ALL arguments is a large
widening from one sample. The notice states the row it will write — principal,
action, scope, and the exact parameter matcher — and writes exactly that:

```
always → skip approval for
  github_create_pull_request(repo=kronael/*)  in folder solo
  (skips approval only; the mcp: grant still has to allow the call)
```

Two properties of the matcher must be in that text, because both widen it
beyond what an operator assumes: a rule constrains only the parameters it
NAMES, so unnamed arguments are unconstrained, and non-scalar arguments are
skipped entirely by `stringArgs` and can never be matched. An `always` whose
full effective match was not shown and accepted is a grant nobody agreed to,
and the surface refuses to write it.

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

Still to build:

- `arizuko token <inst> issue chat <folder>` → `arizuko talk <url> --token <t>`
  holds a conversation with the persona from a second machine, with no DB
  access and no operator scope (gap 1).
- A `hold:mcp:<tool>` row plus a `hold:mcp:<tool>(repo=x/*)` row with
  `effect='deny'`: the matching shape runs inline, every other shape is still
  held, a broad `hold:mcp:*` does not resurrect the exception, and
  `TestCheckHold_OperatorStarDoesNotHoldEverything` stays green (gap 2.1).
- `add_acl` writes a row carrying params, and `auth.Delegate`'s subset check
  runs on the SHAPED row — a caller who may grant `mcp:t(repo=mine/*)` cannot
  write `mcp:t` (gap 2.2).
- `always` writes exactly the matcher the notice displayed, and the surface
  refuses to write one it did not display (gap 2.2).
- `never` writes BOTH the skip-approval row and the `mcp:<tool>` deny in one
  transaction; the next call is refused and is NOT held again — the test fails
  if either row is missing (gap 2.1).
- The surface offers no shaped `never` while `authorizeCall` passes nil params;
  a test pins that, so removing the restriction requires removing the reason
  (gap 2.3).
- An adapter with `buttons` renders four native options and a press resolves
  the row; an adapter without renders numbered text and a numeric reply
  resolves the same row; whapd renders a four-option poll (gap 3).
- The github connector opens a PR under a surrogate-OAuth token; the token
  never appears in container env or in the tool result.
- `make build && make lint && go test ./... -short` green; tests in the same
  commit.

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
