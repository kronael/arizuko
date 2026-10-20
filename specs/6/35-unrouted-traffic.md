---
status: shipped
depends:
  [
    ../5/18-onboarding-model,
    ../5/33-paths-roles,
    28-cockpit-index,
    29-dashd-hub,
  ]
---

# specs/6/35 — unrouted traffic: held and visible, never delivered

> A chat no route claims is **held**, not delivered: routd stores its messages
> under no folder, fires no turn, and the root operator sees it on the routes
> page and claims it with the route write that already exists. The safe default
> destination for unclaimed traffic is the operator's screen, not a group.

## Problem

An adapter that nobody routes still receives. On a default instance
(`ONBOARDING_ENABLED=false`, `core/config.go:257`) a route miss stores the
message, advances the chat's cursor (`routd/loop.go` `routeMiss`) and does
nothing else. Nothing on any operator surface says the chat exists. The
operator's own first message to their own bot vanishes the same way a
stranger's does; `5/18` measured 40 minutes of that silence on krons.

Measured on krons 2026-09-05: every route row is an explicit single-chat match
(`room=group/5229089286 → rhias/nemo`, `room=1203…@g.us → mayai`). No wildcard,
no catch-all. A small operator who does not write those rows has an instance
that answers nothing and shows nothing.

Two wants, in the maintainer's words: a sane zero-setup default, and a view of
"channels that route nowhere" so the root operator can see it and set it up.

## The rule

> **No route, no folder, no turn.** A held chat reaches no group until a caller
> holding `admin` on the target writes a `routes` row for it. The hold is
> visible to the root operator; claiming it IS that write.

"Zero setup" means nothing to configure before the first message arrives, and
one click after it. It does not mean the platform picks a group.

## Why a catch-all is not the default

A folder is not an inbox. Reaching a folder as a trigger confers everything
`5/33` resolves from `folder:<folder>` at dispatch: the group's files and
`MEMORY.md`, its folder-scoped secrets, the `role:member` verb floor, and the
mount / egress / web-publish grants decided once per spawn
(`routd/dispatch.go`, `5/33` §"Where the axis deliberately does NOT reach").
None of that reads who sent the message — an unpaired sender's identity axis
contributes ∅ and the turn authorizes exactly as the folder does.

Who can reach a bot is the platform's decision, not the operator's. Anyone on
Telegram can message any bot by username; anyone in a Discord server the bot
joined can post; anyone with the WhatsApp number can write. So a seeded
**trigger** catch-all (`match='' → main`) makes "can find the bot" equal to
"drives the operator's agent with the operator's files, secrets and egress".
That is the tenancy hole, and the reason the admission queue exists at all.

A seeded **observe** catch-all (`main#observe`) fires no turn but is not neutral
either: every stranger's text enters `main`'s `<observed>` window on its next
trigger turn and its `inspect_messages` result (`ROUTING.md` § A target fragment sets the mode), and the observe branch
consumes the miss before the greeting branch runs, so onboarding on that
platform is dead the day an operator turns it on (`5/18` §"Staging: the other
posture" — mutually exclusive by construction). `5/18` already decided that
`arizuko create` seeds no catch-all because the trade-off is the operator's.
This spec keeps that decision; both catch-alls remain one route row away for an
operator who wants one, with the view below showing what the row would claim.

## What ships

### routd — the held set is derived, not stored

`GET /v1/routing/unrouted` lists every chat whose latest inbound message the
loop would drop now. The reader (`routd/unrouted.go` `Loop.UnroutedChats`) walks
the chats that carry inbound rows and asks the loop's own `resolve` — direct
address, route table, engagement — so the answer is the miss decision itself,
not a second evaluator that can drift from it. A chat leaves the set the moment
a route claims it; a route-table read failure is an error to the caller, never
"everything is unrouted".

No schema, no marker column, no new state: the messages already sit in
`messages` with `routed_to=''`, and the cursor already advanced past them. Held
means exactly what the loop already does; this spec names it and shows it.

The handler is a plain read beside `/v1/routing/errored`, not a resreg
resource, for the reason `routd/dash_reads_http.go` records: it must not become
an agent tool. A held chat belongs to no folder, so the endpoint answers only an
unscoped (root / service) bearer — a folder-scoped token owns none of these
chats and gets 403, not an empty list that reads as "nothing is arriving".

### dashd — the view is on the routes page

`/dash/routes/` gains an **Unrouted chats** section, rendered for the operator
only (`callerScope` → `operator`), read through routd's `/v1` with the
service:dashd bearer (`6/28` read path; `routes:read` is already in dashd's
ceiling, `authd/http.go`). Per chat: platform, JID, chat name, message count,
first and last seen, last sender, and a **claim** form.

Claim posts to the existing `POST /dash/routes/` with `seq=0`,
`match=chat_jid=<jid>` and a target picked from the group list. Nothing new is
written by anything new: `handleRouteCreate` gates on `admin` over the target
(`dashd/routes_admin.go`), so a non-operator who forges the POST is refused
there, and the route lands with the operator on the audit row (`AsUser`).

A whole platform is claimed the way `ROUTING.md` already documents — a
`platform=<name>` row in the add form beneath the section. The section says so,
and says that history does not move: only messages after the claim reach the
group (`5/18` step 8).

An unreachable routd renders an error banner for the section, never an empty
table: "no unrouted chats" is a statement the operator acts on.

### Relation to onboarding (`5/18`)

Two paths, one write. Onboarding is the self-serve path on the identity axis:
the chat's own user pairs, proves authority over a world, and `handleAddRoute`
writes the row. This view is the operator's path on the location axis: root
sees the hold and writes the same row from the other side. A greeted chat that
never pairs stays in the view; a chat routed by either path leaves it. Neither
path consults the other, and neither needs to.

## Code pointers

- `routd/unrouted.go` — the reader on `Loop` (it needs `resolve`) and the
  `/v1/routing/unrouted` handler; `routd/api/v1/types.go` `UnroutedChat`.
- `dashd/routes_admin.go` — the section and the per-row claim form, above the
  add form so the operator sees what is waiting before writing a rule.
- `dashd/stub_routd_test.go` — the stub answers the endpoint from the seeded
  `routd.db` by the same resolution, so the page test reads the real shape.
