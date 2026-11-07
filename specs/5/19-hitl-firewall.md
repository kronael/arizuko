---
status: shipped
shipped: 2026-08-07
moved-from: specs/17/4-hitl-firewall.md
---

# specs/5/19 — HITL firewall: hold a tool call for human approval

A per-folder gate suspends a tool call until a human approves, rejects or lets
the request expire. Approval releases the original call inside the same turn.

## Shape

```text
┌────────────┐   ┌───────────┐ approved ┌────────────┐
│ MCP socket ├──►│ hold gate ├─────────►│ tool authz │
└────────────┘   └───────────┘          └────────────┘
```

`ipc.StoreFns.CheckHold` intercepts eligible `tools/call` requests before
`HandleMessage`. `routd.holdGate` records the call, sends the notice and waits
for the verdict. The gate covers tools reached through that socket, including
connector tools. A nil gate adds no hold check. Elevated `/root` turns have no
hold gate.

Approval never substitutes for authorization. The original request still meets
the normal tool and target checks after the gate releases it. A policy read or
pending-row write failure blocks the call and returns an error.

Sources: `ipc/ipc.go` `serveConn`, `routd/pending_actions.go` `holdGate`,
`routd/mcp.go` `ServeTurnMCP`.

## Hold policy — acl rows, not a new config surface

Hold rules use `action=hold:mcp:<tool>` and the ACL row's `scope`, `params` and
`predicate`. Accepted effects are `allow` and `deny`:

| effect  | meaning on a hold action      |
| ------- | ----------------------------- |
| `allow` | suspend the call for approval |
| `deny`  | skip approval                 |

**Hazard 1 — a hold is not a tool grant.** Keep hold policy in the `hold:`
namespace. A rule on `mcp:<tool>` changes tool authorization.

**Hazard 2 — ordinary action grants must not hold calls.** `CheckHold` reads
policy through `Policy.Allows`. `actionCovers` matches only the exact hold
action or `hold:mcp:*`; an operator's `*` grant does not cover hold actions.

Matching local rows decide before release defaults from `auth/defaults.go`.
Deny wins within the selected layer. A local hold can override a default skip.
The same parameter matcher serves hold policy and ordinary authorization.

Sources: `auth/authorize.go` `CheckHold`, `auth/defaults.go`,
`store/acl.go` `EffectAllowed`, `auth/checkhold_test.go`.

## `pending_actions` — one resreg resource

`routd.PendingAction` stores the original arguments, caller, folder, chat,
notice id, verdict and reviewer. The row records the decision, not the tool's
result. Its states are `held`, `approved`, `rejected` and `expired`.
`expires_at` also applies on read. The waiter conditionally writes `expired`
when its wait ends.

The gate creates rows. The resource exposes list, approve and reject; it
exposes neither create nor delete. The agent socket does not mount the
resource, so the held agent cannot approve itself.

`GET /v1/pending_actions` requires `pending_actions:read`.
`POST /v1/pending_actions/{id}/approve|reject` requires
`pending_actions:write`. `service:dashd` holds those scopes and forwards the
proxyd-verified human as `reviewer`. The queue and count include only folders
that human may administer.

Sources: `routd/pending_actions.go` `PendingAction`,
`resreg/resources/pending_actions.go`, `routd/pending_actions_http.go`,
`dashd/approvals_page.go`.

## Resolution — one verdict for the original call

Chat `/approve`, `/reject` and `/always` and dashboard verdicts use
`resolveHoldInto`. `CheckGrant` requires `admin` on the held row's folder.
An `always` answer also proves authority for its skip-approval row. The
verdict and standing row commit in one batch with the policy proof.

The verdict write requires `status=held`, so concurrent answers admit one
winner. Verdict commands bypass the agent queue and do not enter the turn
feed. The suspended gate reads the verdict directly from the row.

`/always` approves the call and writes `deny` on its `hold:` action. The
renderer supplies the exact matcher from the tool's declared keys. Tools
without declared keys refuse `/always`. Removing the row revokes the standing
answer. Approval text and matcher ownership live in `6/49-approval-ask.md`.

Sources: `routd/pending_actions.go` `resolveHoldInto`, `routd/steer.go`,
`routd/pending_actions_http.go`.

## Blocking is the default

`awaitVerdict` suspends the original call for `HITL_WAIT`, 10 minutes by
default. Keep that wait below `RUNED_RUN_TIMEOUT` so the turn can receive its
answer before the guest stops.

Approval releases the original request with its original arguments. The agent
receives the tool's result and must never reissue the approved call. Rejection
or expiry returns a `NOT RUN` tool result that tells the agent to report the
refusal and not retry. A rejection includes the reviewer's note.

The waiter's conditional expiry preserves a verdict that wins the same race.
An expired request rejects later verdicts. An approval creates no new turn and
provides no release for a future call.

Sources: `routd/pending_actions.go` `awaitVerdict`, `expirePendingAction`,
`verdictRefusal`.

## Buttons

`SendRequest.Options` carries a label and command data. Native buttons send
that command as ordinary inbound text under the presser's verified identity.
`chanreg.HTTPChannel.SendOptionsCtx` folds options into typed commands for
adapters without buttons. Both reach the same resolution handler and require
`admin` on the held folder.

Slack buttons require the app's Interactivity Request URL at
`/slack/interactivity`.

## Reactions — the universal answer

A green or red reaction on a held notice becomes `/approve <id>` or
`/reject <id>` at ingress. `chanlib.VerdictEmoji` excludes thumbs-up and
thumbs-down, which are ordinary social reactions.

The lookup binds the chat, `pending_actions.notice_id` and held status. It
takes the last segment of a reaction's target id to match the bare notice id.
The command uses the same reviewer check as buttons and typed verdicts.

## Acceptance

- A matching hold writes a pending row and notice while the tool call waits.
- Approval runs the original call through normal authorization once.
- Rejection or expiry returns `NOT RUN`; the tool does not run.
- A reviewer needs `admin` on the held folder; `/always` also requires
  authority for the standing row.
- An operator's `*` grant does not hold every tool.
- Buttons, typed verdicts and supported reactions use one resolution path.
- A failed policy read or pending-row write blocks the call.
