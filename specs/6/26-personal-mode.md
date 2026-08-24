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
    ../17/1-git-channel,
  ]
---

# specs/6/26 — personal mode: one human, one terminal, one approval per call

> A single operator wants their agent (`atlas`) without a chat platform: talk
> to it from a terminal, let it act on their GitHub, and answer each risky call
> with **yes / no / always / never** — in the channel or in a browser.
>
> Almost all of it already ships. This spec records what that is, refuses two
> things that look like the answer and are not (MITM, a bearer approval link),
> and names the three pieces still missing: a terminal REPL, deny-wins in
> `CheckHold` — the one auth change, without which "always" cannot be expressed
> at all — and the generic options fold in the adapters.

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
- **`gitd` (draft, [`17/1`](../17/1-git-channel.md)).** GitHub as a **channel
  that calls the agent** — each repo a folder, each PR and issue a thread. The
  right design for a different want: an agent that watches a repo, not a human
  who asks their agent to open a PR.

Personal mode takes the connector. `17/1` is neither superseded nor folded
here.

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
· never**. Two are one-shot rows, two are `acl` rows, and both gates re-read
`acl` on every call ([`5/32`](../5/32-acl-unified.md) § no caching). The agent
therefore stores nothing and remembers nothing — it learns the standing answer
from the OUTCOME of its next call.

| Verdict    | The call in hand                       | Also writes                                      | Next identical call    |
| ---------- | -------------------------------------- | ------------------------------------------------ | ---------------------- |
| **yes**    | `approved` (one-shot, args-hash keyed) | —                                                | held again             |
| **no**     | `rejected`                             | —                                                | held again             |
| **always** | `approved`, exactly as `yes`           | `hold:mcp:<tool>(<params>)` with `effect='deny'` | runs inline, no notice |
| **never**  | `rejected`, exactly as `no`            | `mcp:<tool>(<params>)` with `effect='deny'`      | denied before the hold |

**`always` and `never` are `yes`/`no` plus a rule — never a third verdict on
the row.** `pending_actions` keeps its four states and
`resolvePendingAction` keeps rejecting unknown verdicts
(`TestResolvePendingAction_RejectsUnknownVerdict`); the standing answer lives
in `acl`, where every other standing answer already lives. The verdict and its
acl row commit in the SAME transaction as the resolution message, the way
`routd.resolveHoldTx` already commits the verdict and the trigger together —
otherwise a crash leaves a call approved with no standing rule, or a rule with
no approved call.

Both rows go through the shipped `add_acl`, already subset-checked by
`auth.Delegate` (`routd/acl_resource.go:222`) and audited. `never` needs
nothing new: `Authorize` is already deny-wins.

**Gap 2 — `CheckHold` cannot express an exception, so `always` is unbuildable
today.** `auth/authorize.go:398` returns `true` on the FIRST `hold:` row
matching action, scope, predicate and params; it never reads `Effect`. The
obvious workaround is a trap that `5/19` Hazard 1 already names: `Authorize`
treats every non-`deny` effect as ALLOW (`authorize.go:73`), so writing an
allow row grants the tool while the hold still fires — held forever, and
reading as granted.

The fix is to give `CheckHold` the deny-wins pass `Authorize` already has:
scan every matching row, and a `hold:mcp:<tool>` row with `effect='deny'` means
"do not hold this shape". Same table, same matcher, same `hold:` namespace, so
the rule stays invisible to the allow/deny evaluator and Hazard 1 holds. It is
one two-pass loop plus a test that a deny row beats a matching hold row, and
`TestCheckHold_OperatorStarDoesNotHoldEverything` must stay green.

**The predicate is part of the verdict, and the operator must see it before
pressing.** A held call carries concrete arguments; `always` over ALL arguments
is a large widening from one sample. The notice states the row it will write —
`always: github_create_pull_request(repo=kronael/*)` — and writes exactly that.
An unstated predicate is a grant nobody agreed to.

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
  held, and `TestCheckHold_OperatorStarDoesNotHoldEverything` stays green
  (gap 2).
- `always` writes exactly the predicate the notice displayed, through `add_acl`
  under `auth.Delegate`; `never` writes the `mcp:<tool>` deny and the next call
  is refused BEFORE the hold (gap 2).
- An adapter with `buttons` renders four native options and a press resolves
  the row; an adapter without renders numbered text and a numeric reply
  resolves the same row; whapd renders a four-option poll (gap 3).
- The github connector opens a PR under a surrogate-OAuth token; the token
  never appears in container env or in the tool result.
- `make build && make lint && go test ./... -short` green; tests in the same
  commit.

## Out of scope

Repo-as-folder ingest (`17/1`) · per-folder reviewer sets (`5/19`) · TLS
interception (declined) · a bearer approval link (rejected above) · `mcpfw`
(`6/12`, waits for a third-party-MCP consumer not brokered through the socket)
· the model-credential gateway (`6/23`, orthogonal and independently worth
doing).

## Ties

`5/19` HITL (the gate) · `5/32` (the `acl` row the four verdicts write) ·
`5/13`+`5/14`+`5/15` (the credential path) · `5/31` (capability links are token
kinds) · `5/35` (why not Hermes) · `5/A` decision 6 (the topology) · `17/1`
(the other GitHub) · BUGS `J14`.
