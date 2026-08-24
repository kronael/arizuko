---
status: draft
depends:
  [
    ../5/19-hitl-firewall,
    ../5/13-ext-mcp,
    ../5/31-identity-pairing,
    ../5/35-hermes-integration,
    ../5/A-primitives-framing,
    ../17/1-git-channel,
  ]
---

# specs/6/26 — personal mode: one human, one terminal, one approval per call

> A single operator wants their agent (`atlas`) without a chat platform:
> talk to it from a terminal, let it act on their GitHub, and approve each
> risky call — in the channel or through a link that opens in a browser.
> Every part but three exists. This spec names the three and refuses the
> fourth.

## Why not run Hermes directly

Because the harness is the cheap half. [`5/35`](../5/35-hermes-integration.md)
measured it: Hermes's adapter interface and `chanlib.BotHandler` are the same
verb set differently spelled, and 26,102 of its 39,005 gateway lines are
platform adapters. What a harness does not carry is what a single human still
needs the day after the first turn works — persistence with search
([`5/C`](../5/C-message-mcp.md)), an audit row per state transition emitted in
the mutation's own transaction ([`5/I`](../5/I-tool-call-logging.md)), a grant
checked on every tool call ([`5/32`](../5/32-acl-unified.md)), credentials
brokered on the host so they never enter the agent's environment
([`5/13`](../5/13-ext-mcp.md)), per-folder egress, and the hold gate this spec
is mostly about ([`5/19`](../5/19-hitl-firewall.md)).

Personal mode is therefore **not a smaller arizuko**. It is the same platform
with one folder and one human, and the reason to pay for it is the approval
gate and the credential broker, not the chat.

## Decision 1 — personal mode is a topology, not a build flag

[`5/A`](../5/A-primitives-framing.md) decision 6 (user-locked 2026-08-09)
already settles this: the smallest real deployment is `routd` + `dashd` +
`onbod` + `timed` with **no channel adapter at all**, and what crosses the
boundary crosses as REST or MCP. Personal mode adds no flag, no profile and no
second code path — it is that topology with one group folder.

A channel adapter stays one way to reach the seam. Slack is not removed; it is
simply not started.

## Decision 2 — the terminal is an MCP client, not a new daemon

`POST /chat/{token}/mcp` already serves `send_message`, `get_round` and
`get_round_status` over streamable HTTP (`webd/server.go:110`,
`webd/chat_mcp.go`), authorized by a `route_tokens` bearer that is
append-at-ONE-JID ([`5/W`](../5/W-webhook-routes.md)). That is the terminal
surface: any MCP client — Claude Code on the laptop included — attaches to the
bot with a URL and a token, from anywhere, with no operator shell and no DB
access.

Two host-local paths exist beside it and stay: `arizuko send <inst> <folder>
"<msg>" --stream` (operator-direct, writes the inbound straight to the DB) and
`arizuko chat <inst>` (an interactive Claude Code bound to the ROOT MCP socket
— an operator tool, not a conversation with the persona).

**Net-new, gap 1: `arizuko talk <url> --token <t>`** — a REPL that is a thin
client of `/chat/<token>/mcp` and nothing else. It holds no DB handle and knows
no folder; it sends, then streams `/chat/<token>/<id>/sse`. It is the third
consumer of one surface, never a fourth path into the queue.

## Decision 3 — GitHub arrives as a connector, not as a channel

Two shapes exist and they are different products:

- **Connector (shipped, use this).** `template/connectors/github.toml`
  declares the upstream GitHub MCP server; [`5/13`](../5/13-ext-mcp.md) shape 2
  spawns it per call, narrows the folder's secrets to the keys the connector
  declared, gates the call on `mcp:<tool>` and scrubs secret values from the
  result. `auth/surrogate/providers/github.toml` supplies the user-consented
  token ([`5/15`](../5/15-surrogate-oauth.md)) so the agent acts as the human,
  not as a shared PAT. This is GitHub as a **tool the agent calls**.
- **`gitd` (draft, [`17/1`](../17/1-git-channel.md)).** GitHub as a **channel
  that calls the agent**: each repo a folder, each PR and issue a thread,
  webhook ingest, `review`/`merge` as chanlib verbs. It is the right design and
  it is a different want — an agent that watches a repo, not a human who asks
  their agent to open a PR.

Personal mode takes the connector. `17/1` is not superseded and not folded
here; whoever wants repo-as-folder builds it there.

## Decision 4 — MITM is declined; HITL is what you want

The two are routinely confused, so state both:

- **MITM — TLS interception of the agent's egress. DECLINED** 2026-08-18,
  reaffirmed 2026-08-20 ([`6/24`](24-crackbox-lightest.md) §"MITM stays
  declined", [`6/index`](index.md)). Header injection was the only consumer,
  and a CA inside every container is a worse hole than the one it closes.
  [`8/Z-egred-mitm.md`](../8/index.md) was deleted for reintroducing what
  `5/13` had already rejected. Metering does not need it either — a gateway is
  configured, not intercepted ([`6/23`](23-llm-gateway.md)).
- **HITL — a human in the middle of a TOOL CALL. SHIPPED** 2026-08-07
  ([`5/19`](../5/19-hitl-firewall.md)). A `hold:mcp:<tool>(param=glob)` acl row
  makes `CheckHold` suspend the call at the one `tools/call` interception in
  `ipc.serveConn`, write a `pending_actions` row, and notify the chat. On
  approval the ORIGINAL agent re-issues the call in its own turn, its
  container, session, grants and secrets untouched; the release is one-shot and
  keyed on the canonical-JSON hash of the args, so edited args are held again.

"Granular approvals" is therefore already the shipped granularity: per tool,
per argument glob, per folder. Do not build a second gate.

## Decision 5 — approval delivery: one decision, two sinks

`5/19` shipped the decision and both resolution paths (`/approve <id>` in chat,
`POST /v1/pending_actions/{id}/approve|reject`, both funnelling through
`routd.resolveHoldTx` in one transaction). What it deliberately did NOT ship is
the DELIVERY the operator sees. Both halves are gap 2 and gap 3:

**Gap 2 — in the channel.** `SendRequest.Options []Option{label,data}` does not
exist in `chanlib` or `core` (verified 2026-08-24: zero hits). It is the
generic vehicle: rendered as numbered text at the existing `HasCap` fold when
the adapter lacks `buttons`, natively where it does; a button press maps to an
ordinary `InboundMsg` on the existing ingest, the way reactions already map
platform callbacks. Button and typed command converge on the one `/approve`
parser, consumed before any turn — an approval never burns a model call.

**Gap 3 — in a browser.** `/dash/approvals/` already exists, is operator-gated,
and already carries the verdict path (`POST /dash/approvals/{id}/resolve`). The
held notice does not name it, so the operator must know the page is there and
find the row by eye.

An earlier draft of this spec proposed `route_tokens.kind = 'approve'` — a
one-shot bearer bound to one `pending_actions.id`. **Rejected on review.** The
held notice is delivered INTO THE CHAT the agent shares with its users, so a
bearer link in that notice hands the verdict to everyone who can read the chat,
which is precisely what `IsOperator` exists to prevent. `5/35` states the rule
this breaks: a URL that travels must never carry verb authority. Approving a
tool call is verb authority.

So the token adds nothing an authenticated page does not already have, and the
fix is the small one: **the notice carries the deep link
`<WEB_HOST>/dash/approvals/?id=<id>`, and the page focuses that row.** The
reviewer signs in once through the OAuth the page already requires; the link
names WHICH decision, the session names WHO. One mechanism, not two.

## Decision 6 — the reviewer stays the operator in v1

`5/19` gates resolution on `db.IsOperator` and left folder-scoped reviewer sets
out of scope. Personal mode is precisely the case where reviewer, operator and
the only human are the same person, so v1 is sufficient by construction and
this spec adds nothing. A team wanting per-folder reviewers reopens `5/19`.

Also unchanged: the agent-socket MCP face of `pending_actions` stays unmounted
— the held agent approving its own call is not a gate. That is an open
question, not a defect.

## Residual hole this does not close

Claude Code natively reads `.mcp.json` from the session cwd — the agent's own
home — and the SDK query passes neither `strictMcpConfig` nor an empty
`settingSources`, so a server the agent writes there is never seen by the
socket and no `hold:` rule can suspend its tools (BUGS `J14`). Every guarantee
above is bounded by that. The fix is `strictMcpConfig: true`, which also closes
plugin and agent-frontmatter MCP and therefore needs a decision, not a patch.

## Acceptance

- `arizuko token <inst> issue chat <folder>` → `arizuko talk <url> --token <t>`
  holds a conversation with the persona from a second machine, with no DB
  access and no operator scope.
- The github connector opens a PR under a surrogate-OAuth token; the token
  never appears in container env or in the tool result.
- `hold:mcp:github_create_pull_request(repo=kronael/*)` → the call returns
  `{pending:true,id}`, a `pending_actions` row lands `held`, and the notice
  carries BOTH the `/approve <id>` text and the `/dash/approvals/?id=<id>` link.
- The link opens the approvals page with that row focused; a non-operator who
  follows it is refused by the gate the page already applies.
- With `WEB_HOST` unset the notice still carries the chat commands and no
  broken URL.
- The agent re-issues and passes `CheckHold` one-shot; the row's terminal state
  is `released`.
- `make build && make lint && go test ./... -short` green; tests in the same
  commit.

## Out of scope

Repo-as-folder ingest (`17/1`) · per-folder reviewer sets (`5/19`) · TLS
interception (declined) · `mcpfw` (`6/12`, waits for a third-party-MCP
consumer that is not brokered through the socket) · replacing the model
credential with a gateway virtual key (`6/23`, orthogonal and independently
worth doing).

## Ties

`5/19` HITL (the gate) · `5/13`+`5/14`+`5/15` (the credential path) · `5/31`
(the link-is-a-token-kind precedent) · `5/35` (why not Hermes) · `5/A`
decision 6 (the topology) · `17/1` (the other GitHub) · `6/23` (the other
"container must not hold the raw secret") · BUGS `J14`.
