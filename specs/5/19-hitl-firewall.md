---
status: shipped
shipped: 2026-08-07
moved-from: specs/17/4-hitl-firewall.md
---

# specs/5/19 — HITL firewall: hold a tool call for human approval

A per-folder gate that suspends a marked tool call until an operator approves it
in chat (or the dashboard), then lets the **original agent** run it. Built on
existing rails: one new resource, one injected gate function, one send field.

**Shipped 2026-08-07** — see §"What shipped" for what landed and what was
deliberately left for the adapters.

## Shape

A `hold:mcp:<tool>` acl rule makes `CheckHold` short-circuit the call at the MCP
choke point, writing a `pending_actions` row plus a chat notice with
Approve/Reject buttons. On approval routd re-enqueues the folder, the agent
re-issues the call, and the gate lets it through one-shot.

## Hold policy — acl rows, not a new config surface

Hold rules are ordinary `acl` rows (`routd/migrations/0007-acl.sql`): action
`hold:mcp:<tool>`, with the existing `params` column carrying the
`(param=glob)` predicate. A bare rule always holds; params make it conditional.

> **HAZARD 1 — never store a hold as `effect='hold'`** on a plain `mcp:<tool>`
> action. `auth.Authorize` treats every non-`deny` effect as ALLOW
> (`auth/authorize.go:64`), so such a row would silently _grant_ the tool. The
> `hold:` action namespace keeps hold rules invisible to the allow/deny
> evaluator by construction.
>
> **HAZARD 2 — do not evaluate the hold through `auth.Authorize`.** Its action
> lattice has `*` cover everything (`actionCovers`), and `role:operator` holds
> `(*, **)`, so routing the hold question through `Authorize` would hold _every_
> tool for the operator. `CheckHold` must be a **direct scoped read of
> `hold:mcp:`-prefixed rows** for the caller's principal set, matching `params`
> the same way `Authorize` does — one predicate, no new evaluator, no CEL.

## The gate — one injected function at one choke point

`StoreFns.CheckHold` is injected beside `StoreFns.Authorize` (`ipc/ipc.go:236`,
wired in `routd/mcp.go` `StoreFns{…}`). At the `tools/call` interception in
`serveConn` (`ipc/ipc.go:437`, where the `mcp_tool` span already hooks) it
parses name + args and either returns `{pending:true, id}` to the agent or lets
the call reach `srv.HandleMessage`. A nil `CheckHold` is zero overhead and
today's behavior untouched.

This single site covers every tool on the socket — hot tools, resreg facade
tools, and timed-triggered turns — so "no bypass" holds by construction.

**"On the socket" is the boundary, and arizuko is the only server on the
agent's map.** `ant/src/mcp-servers.ts` `injectMcpEnv` returns exactly one
server: socat to the per-turn gated socket. The settings.json extension point
that once loaded agent-declared servers beside it was DELETED 2026-08-09 (BUGS
`J1`, `F76`): those tools went subprocess-to-subprocess, no `hold:mcp:<tool>`
rule could suspend them, and the agent writes that file itself, so it was also a
self-grant. Third-party MCP now arrives only as a connector
(`StoreFns.Connectors` / `ExtTools`), which traverses the socket and is covered.

One residual is NOT closed: Claude Code natively reads `.mcp.json` from the
session cwd — the agent's own home — and the SDK query passes neither
`strictMcpConfig` nor an empty `settingSources`, so a `~/.mcp.json` the agent
writes still loads servers the socket never sees. BUGS `J14`; the fix is
`strictMcpConfig: true`, which also closes plugin and agent-frontmatter MCP and
therefore needs a decision, not a patch.

Hold
runs BEFORE authz, which is safe: approval never substitutes for authz. The
released call re-enters the normal path and every in-handler grant and JID gate
still runs. (A held call an operator approves that authz then denies is noise,
not a hole.)

## `pending_actions` — one resreg resource

`pending_actions(id, group_folder, caller_agent, tool, args JSON, args_final
JSON, status, chat_jid, created_at, reviewed_by, reviewed_at, reviewer_note,
result JSON, error, expires_at)`, status `held → approved|rejected|expired →
released`. Registered as a resreg `Resource` (REST + derived MCP + dashd), per
the cold-tier invariant (`specs/CLAUDE.md`). `released` is a row's terminal
state: `result`/`error` are declared but NEVER written, and this spec now buys
that rather than the writer it first specified. The gate consumes the approval
before the call reaches `HandleMessage`, so no site holds both the released id
and the call's outcome; `RecordPendingOutcome` was written for it, had zero
callers and passed its SQL arguments in the wrong order, and was deleted rather
than left as a shell (BUGS `J6`). Correlating an outcome back would mean
carrying the released id down to the tool-result site — a cross-layer change
whose only payoff is a field, on a table whose point is the DECISION. Expiry is
lazy (computed from `expires_at` on read), no GC job.

There is deliberately **no separate `prompts` resource**: a prompt has no state
not already in `messages` plus the reply correlation. `pending_actions` is the
one genuinely new entity.

## Resolution — the agent re-issues; no dispatcher

There is no out-of-turn dispatcher: the agent MCP server is per-turn
(`routd/mcp.go:526` `ServeTurnMCP`), so there is nothing to re-enter, and a
second hand-rolled path would skip ipc's grant and audit discipline — the
dual-path CLAUDE.md bans.

1. An operator resolves via a chat `/approve <id>` / `/reject <id>` **or**
   `POST /v1/pending_actions/{id}/approve|reject`. Both funnel to one handler.
   (The REST half is not mounted yet — see §"What shipped". Chat works.)
2. routd injects a resolution message into `chat_jid` and enqueues the folder —
   the `PutMessage`+`Enqueue` pattern of `cmdRoot`/delegate. The message carries
   tool + `args_final` + verdict and IS the next-turn trigger; no
   `<pending-resolutions>` autocalls block is needed.
3. The agent re-issues the call in its own next turn — its container, session,
   folder grants, and per-operator secrets untouched: "runs as the ORIGINAL
   agent", literally.
4. `CheckHold` finds the matching `approved` row (tool + canonical-JSON hash of
   `args_final`) and consumes it **one-shot** — the `pendingElevation` precedent
   (`routd/dispatch.go:121` `LoadAndDelete`). Any args deviation is a hash miss
   and is held again: edited-args enforcement by construction.

## Operator interaction — generic across channels

The Approve/Reject prompt is not dashboard-bound; it rides a small generic send
capability reusable by any agent question.

- `SendRequest.Options []Option{label, data}` (data defaults to label). No new
  route — it rides `POST /send`. The agent surface is an optional flat
  `options: []string` param on the existing `send`/`reply` tools: mechanically
  identical to sending a message, so a param, **not** a new `ask` verb.
- **One fold, every sink**: `chanreg.HTTPChannel.SendCtx` renders options as
  numbered text at the existing `HasCap` site when `!HasCap("buttons")`;
  adapters advertising `buttons` render natively.
- **Inbound**: a button press maps to an `InboundMsg` (Content = option `data`,
  ReplyTo = prompt msg id, Sender = the presser's adapter-verified id) on the
  existing ingest — the same way reactions already map platform callbacks. No
  `/v1/answers`, no new event kind.
- HITL notice buttons carry `data="/approve <id>"` → routd's reserved command
  stub (`routd/steer.go:146`, today `"HITL not configured"`); the handler gates
  on `db.IsOperator`, same as `/root`. Button and typed command converge on one
  parser, consumed before any turn — approvals never burn a model call.

Net-new (recon-confirmed absent): per-adapter native button render +
callback→inbound mapping in teled/discd/slakd, and the dashd review page.
Everything else reuses an existing rail. An agent asking "staging or prod?"
needs zero new state — the answer is an ordinary reply promoted to a mention.

## Out of scope (v1)

Reviewer arg-editing beyond re-hold (a changed call is a new call → re-held);
folder-scoped reviewer sets (v1 gates on `IsOperator`); Telegram `callback_data`

> 64B (use short ids — long generic labels need an index scheme inside teled
> only).

## Consumers

`product-creator` (approve-before-publish) and `product-socials` unblock on
this. `aws-devops`/argus turns its prose "read-before-write, wait for the
operator's go" into real `hold:mcp:<destructive-tool>` rules.

## Acceptance

- A `hold:mcp:<tool>(param=glob)` row → the call returns `{pending:true,id}`; a
  `pending_actions` row lands (held) with a chat notice + buttons.
- `/approve <id>` by an operator → resolution message enqueued; the re-issued
  call passes `CheckHold` one-shot; the row's terminal state is `released`.
- A non-operator `/approve` is rejected.
- No hold rule ⇒ inline execution; nil `CheckHold` ⇒ no-op.
- An operator's `(*, **)` row does NOT hold every tool (Hazard 2).
- Adapter without `buttons` → numbered text; a numeric/label reply resolves the
  same row.
- `make build && make lint && go test ./... -short` green; tests in the same
  commit.

## What shipped (2026-08-07)

Both hazards are guarded by a test that fails when the guard is removed, which
is the only reason to believe them:

- `auth.CheckHold` (`auth/authorize.go`) matches the action EXACTLY, with
  `hold:mcp:*` the one wildcard. `TestCheckHold_OperatorStarDoesNotHoldEverything`
  first asserts the operator IS authorized for the tool, then that the same
  `(*, **)` row does not hold it — routing the check through `actionCovers`
  fails it, exactly as Hazard 2 predicts.
- Hazard 1 is structural: `hold:` is an action namespace, so a hold rule is
  invisible to the allow/deny evaluator and a grant row cannot read as a hold
  (`TestCheckHold_PlainAllowRowIsNotAHold`).

`ipc.StoreFns.CheckHold` fires at the one `tools/call` interception in
`serveConn` — before `HandleMessage`, so no tool routes around it. A RECORDED
hold returns a tool RESULT, not a JSON-RPC error: the call did not fail, it is
waiting, and the agent must be able to say so rather than retry a "failure" in
a loop. Nil `CheckHold` is zero overhead.

`routd.holdGate` consumes an approved row BEFORE testing the hold rule — that
ordering IS the one-shot release, since the rule still matches on re-issue.
Argument deviation misses the canonical-JSON hash and is held again, so
edited-args enforcement needs no separate comparison. An elevated `/root` turn
gets no gate: the operator holding their own call for their own approval is a
deadlock, not a safeguard. A failure to RECORD the row holds anyway — failing
open there would silently defeat the gate the operator asked for — but it is the
one hold that answers with a JSON-RPC error and a chat notice instead of the
pending result: there is no row and no id, so `pending:true` would send the
agent to wait for an `/approve` nobody can type (BUGS `J3`).

`/approve <id>` / `/reject <id>` replace the `"HITL not configured"` stub,
gated on `IsOperator` (the same `**` test as `/root`). Approval writes the
resolution message and enqueues the folder — cmdRoot's `PutMessage`+`Enqueue`
— so the agent re-issues in its own turn, its container, session, grants and
secrets untouched.

`pending_actions` is a resreg `Resource` (`resreg/resources/pending_actions.go`)
with `list`/`approve`/`reject` and no create or delete: the gate writes the row,
and deleting one would erase the record of a decision someone made. The table
ships in both `routd/migrations/0033` and `store/migrations/0085`.

**The REST face and the dashd review page shipped 2026-08-08** (BUGS `F66`,
REST half). `routd/pending_actions_http.go` mounts `GET /v1/pending_actions` +
`POST /v1/pending_actions/{id}/approve|reject` via `resreg.RegisterREST`
(scope-gated: `pending_actions:read`/`:write`, held only by service:dashd —
authd's ceiling test pins it). Both the chat command and the REST verdict
funnel through `routd.resolveHoldTx`: the verdict and its resolution message
commit in ONE transaction, into the HELD call's own chat (an approval typed in
another chat used to trigger the wrong agent), and the loop's poll dispatches
the committed trigger. `/dash/approvals/` (dashd `approvals_page.go`,
operator-only) lists the held queue with per-row approve/reject + note and
recent verdicts; the portal banners the held count.

**The agent-socket MCP face stays unmounted, deliberately**: `approve` on the
held agent's own socket would let it approve its own call, and the only socket
that exists is the per-turn agent one (`ServeTurnMCP`, folder principal). So
this is an OPEN QUESTION, not a defect — `F66` was closed against this
paragraph on 2026-08-09. Closing it needs an operator MCP socket whose identity
is distinct from the held folder; until that exists, chat `/approve` and the
`/dash/approvals/` REST face are the two resolution paths, and both funnel
through `resolveHoldTx`.

### Deliberately not in this release

Per-adapter NATIVE button rendering (teled/discd/slakd callback→inbound). The
generic `SendRequest.Options` fold was the vehicle; without it the notice is
plain text carrying the two commands, which is what the acceptance criterion
"adapter without `buttons` → numbered text" already describes as correct
behavior. The gate, the resource and the resolution paths do not depend on it.
