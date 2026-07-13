---
status: draft
moved-from: specs/17/4-hitl-firewall.md
---

# specs/5/19 — HITL firewall: hold a tool call for human approval

A per-folder gate that suspends a marked tool call until an operator approves it
in chat (or the dashboard), then lets the ORIGINAL agent run it. Built on
existing rails — one new resource, one injected gate function, one send-field —
per the codex + fable minimality review (`.ship/hitl-fable-review.md`,
`.ship/codex-hitl.log`).

## Shape (one sentence)

A `hold:mcp:<tool>` acl rule makes `CheckHold` short-circuit the call at the MCP
choke point, writing a `pending_actions` row + a chat notice with Approve/Reject
buttons; on approval routd re-enqueues the folder and the agent re-issues the
call, which the gate now lets through one-shot.

## Hold policy — acl rows, not a new config surface

Hold rules are ordinary `acl` rows (routd migration `0007-acl`): action
`hold:mcp:<tool>`, the existing `params` column carrying the `(param=glob)`
predicate. `deriveFolderGrants` (routd/mcp.go:492-509) also returns `holdRules`
from `hold:mcp:`-prefixed rows; a hold matches when
`grants.CheckAction(holdRules, tool, params)` (grants/grants.go:127) — the
locked predicate, no new evaluator, no CEL. A bare rule always holds; params
make it conditional.

> HAZARD: never store a hold as `effect='hold'` on a plain `mcp:<tool>` action.
> `auth.AuthorizeWith` treats every non-`deny` effect as ALLOW
> (auth/authorize.go:88-92), so such a row would silently grant the tool. The
> `hold:` action-namespace keeps hold rules invisible to the allow/deny
> evaluator by construction.

## The gate — one injected function at one choke point

`StoreFns.CheckHold` is injected beside `StoreFns.Authorize` (ipc/ipc.go:237-240,
wired at routd/mcp.go:480). At the `tools/call` interception in `serveConn`
(ipc/ipc.go:426-444, where the `mcp_tool` span already hooks) the gate parses
name+args and either returns `{pending:true, id}` to the agent (call held) or
lets the call reach `srv.HandleMessage` (:441). nil `CheckHold` ⇒ zero overhead,
today's behaviour untouched.

This single site covers every tool on the socket — hot tools, resreg facade
tools (routd/mcp.go:546-553), and timed-triggered turns — so "no bypass" holds
by construction. Hold runs BEFORE authz, which is safe: approval never
substitutes for authz — the released call re-enters the normal path (below) and
every in-handler grant/JID gate still runs. (A held call an operator approves
that authz then denies is noise, not a hole.)

## pending_actions — one resreg resource

`pending_actions(id, group_folder, caller_agent, tool, args JSON, args_final
JSON, status, chat_jid, created_at, reviewed_by, reviewed_at, reviewer_note,
result JSON, error, expires_at)`. Status: `held → approved|rejected|expired →
released`. Registered as a resreg `Resource` (REST + derived MCP + dashd), per
the cold-tier invariant (specs/CLAUDE.md). result/error are written by the gate
at release (single writer); outcomes also land in `audit_log` via the existing
emit path (ipc/ipc.go:884-911). Expiry is lazy — status computed from
`expires_at` on read / gate-hit, no GC job.

There is deliberately NO separate `prompts` resource: a prompt has no state not
already in `messages` + the reply correlation. `pending_actions` is the one
genuinely new entity.

## Resolution — the agent re-issues; no dispatcher

There is no out-of-turn dispatcher (the agent MCP server is per-turn,
routd/mcp.go:511-553 — nothing to re-enter, and a second hand-rolled path over
GatedFns would skip ipc's grant/audit discipline, the dual-path CLAUDE.md bans).
On approval:

1. Operator resolves — a chat `/approve <id>` / `/reject <id>` command, OR a
   dashd/REST `POST /v1/pending_actions/{id}/approve|reject`. Both funnel to one
   resolution handler; the row → `approved` (with `args_final`) or `rejected`.
2. routd injects a resolution message into `chat_jid` and enqueues the folder —
   the `PutMessage`+`Enqueue` pattern of cmdRoot/delegate (steer.go:237-251).
   The message carries tool + `args_final` + verdict; it IS the next-turn
   trigger (no `<pending-resolutions>` autocalls block needed).
3. The agent re-issues the call in its own next turn — its container, session,
   folder grants, per-operator secrets (routd/mcp.go:471-477), untouched:
   "runs as the ORIGINAL agent", literally.
4. `CheckHold` finds the matching `approved` row (tool + canonical-JSON hash of
   `args_final`), consumes it ONE-SHOT (pendingElevation precedent,
   dispatch.go:102-108), lets the call through, writes result/error. Any args
   deviation ⇒ hash miss ⇒ held again (edited-args enforcement by construction).

## Operator interaction — generic across channels, minimal

The Approve/Reject prompt is not dashboard-bound; it rides a small generic send
capability reusable by any agent question:

- `SendRequest.Options []Option{label, data}` (data defaults to label); no new
  route, rides `POST /send`. Agent surface: an optional flat `options: []string`
  param on the existing `send`/`reply` tools — mechanically identical to sending
  a message, so a param, not a new verb (NO `ask` verb).
- One fold, every sink: `chanreg.HTTPChannel.SendCtx` (httpchan.go:138-147, the
  existing `HasCap` site) renders options as numbered text `1) …` when
  `!HasCap("buttons")`; adapters advertising `buttons` render natively.
- Inbound: a button press maps to an `InboundMsg` (Content=option `data`,
  ReplyTo=prompt msg id, Sender=presser's adapter-verified id) on the existing
  handleMessages ingest — precedent: reactions map platform callbacks the same
  way (chanlib.go:49-56). No `/v1/answers`, no new event kind.
- HITL notice buttons carry `data="/approve <id>"` → routd's RESERVED command
  stub (steer.go:149-150, today `"HITL not configured"`); the `/approve` handler
  gates on `IsOperator` (steer.go:225-227), same as `/root`. Button and typed
  command converge on one parser (handleCommand, steer.go:127), consumed before
  any turn — approvals never burn a model call.

Net-new (recon-confirmed absent today): per-adapter native button render +
callback→inbound mapping in teled/discd/slakd, and the dashd pending-review
page. Everything else reuses an existing rail. An agent asking "staging or
prod?" needs zero new state — the answer is an ordinary reply promoted to a
mention (routd/server.go:462-466).

## Out of scope (v1)

Reviewer arg-editing beyond re-hold (a changed call is a new call → re-held);
folder-scoped reviewer sets (later acl refinement — v1 gates on `IsOperator`);
Telegram `callback_data` >64B (use short ids; long generic labels need an
index scheme inside teled only).

## Consumers

`product-creator` (approve-before-publish) and `product-socials` unblock on
this. `aws-devops`/argus turns its prose "read-before-write, wait for the
operator's go" (ant/examples/aws-devops/CLAUDE.md) into real
`hold:mcp:<destructive-tool>` rules.

## Acceptance

- `hold:mcp:<tool>(param=glob)` acl row → the call returns `{pending:true,id}`;
  a `pending_actions` row lands (status held) + a chat notice with buttons.
- `/approve <id>` (chat or button) by an operator → resolution message enqueued;
  the agent's re-issued call passes CheckHold one-shot; result written to the row.
- Non-operator `/approve` is rejected (IsOperator gate).
- `hold` absent ⇒ inline execution (today's behaviour); nil CheckHold ⇒ no-op.
- Adapter without `buttons` cap → options render as numbered text; a
  numeric/label reply resolves the same row.
- `make build && make lint && go test ./... -short` green; tests in the same commit.
