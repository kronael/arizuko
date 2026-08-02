---
status: partial
depends:
  [
    B-route-mode-ingestion,
    L-mention-promotion,
    G-engagement,
    E-routd,
    S-jid-format,
    5-tenant-self-service,
    29-worlds-guests-oauth,
    5/32-acl-unified,
    5/33-paths-roles,
  ]
supersedes: []
---

# Onboarding: routing an unrouted JID into a world

## Problem

A chat enters the system by acquiring a **route row**. Until it has one the
default posture is **silence**. Witnessed on krons 2026-07-09:
`telegram:group/5567410596` posted for 40 minutes, every message stored in
`routd.db`, zero trace surfaced anywhere — the operator had to grep the DB for
the JID. Onboarding is the act that ends that silence.

## Two axes, both fail-closed

An inbound message resolves against two independent unknowns. Each is resolved
by exactly one explicit act, and neither implies the other:

| axis                 | unknown           | resolving act                                       | result                   | owner  |
| -------------------- | ----------------- | --------------------------------------------------- | ------------------------ | ------ |
| **identity** (who)   | sender is a guest | pairs via an OAuth URL in the chat                  | an `acl_membership` edge | `5/31` |
| **location** (where) | JID has no route  | a user with routing authority over the target world | a `routes` row → folder  | here   |

An anonymous sender in an unrouted JID yields **nothing** — no turn, no reply,
no grant. That is structural: routing never consults the ACL (`5/B`), and the
ACL never creates routes.

**This spec owns the location axis only.** It CONSUMES an authenticated session
and never mints one; the pairing act belongs to [`5/31`](31-identity-pairing.md).

## The authority rule

> A caller may write a route whose `target` is T **iff** they hold routing
> authority over T.

Approval is **world-scoped, never global**: the question is not "is this an
operator" but "does this user hold authority over _this_ target". A user with no
worlds approves nothing. The grant shape behind the predicate is
[`5/33`](33-paths-roles.md)'s; this spec consumes it.

Two shipped facts constrain it:

- The check already exists in the right shape —
  `auth.MatchGroups(userFolders(sub), target)` in `handleAddRoute`
  (`onbod/main.go:1487`).
- **World creation grants the subtree.** `createWorldTx` (`onbod/main.go:778`)
  writes `acl(sub, admin, <folder>/**)`, so "a subgroup of W" is reachable. It
  could not be widened until its reader stopped comparing strings: the folder
  lookup was `JOIN acl a ON a.scope = g.folder`, and scopes are **patterns**, so
  equality matched neither `acme` against `acme/eng` nor `acme/**` against
  anything. The reader now asks `auth.Authorize` per group — one evaluator,
  looped — rather than a second matcher that could drift from it.
- Invite join-mode still writes a bare folder scope, `role:operator` is still
  unscoped and global, and neither create-path sets `grant_option=1`, so nothing
  created today is re-delegable.

## The target flow

Eight steps, each with exactly one trigger: **listen** (adapter presence grants
nothing) → **miss** (`routd/loop.go:529` `routeMiss`, unless an `#observe`
catch-all consumes it) → **record** (routd federates `POST /v1/onboarding {jid}`
to onbod, idempotent on the JID PK) → **greet** (onbod replies in-channel with a
single-use 24h link that proves the bearer can read the chat and names no folder)
→ **authenticate** (proxyd OAuth; **seam: the identity axis**) → **choose**
(onbod shows the proven JID and the worlds the caller holds routing authority
over; empty set is a terminal page — **never auto-pick**) → **route** (one
handler binds `target` to the caller's authority and `match` to a JID they have
paired) → **confirm** (the next message routes; history does not move).

## Staging: the other posture

Where the operator controls bot presence, a catch-all captures instead of
greeting. This is route-table data, not code — a low-precedence
`platform=telegram → staging#observe` row under the specific `room=` rows.

- **Capture**: every un-promoted chat lands in the staging folder in observe
  mode — `is_observed=1`, no turn, no reply (`5/B`).
- **Discovery**: staged traffic is read where anything routed is read (dashd,
  `inspect_messages`, the next turn's `<observed>` window). No DB grep.
- **Precedence** is plain `seq` ordering, first match wins; promotion inserts a
  `seq 0` `room=` row. No flag.
- **Promotion** is the existing group-add (`arizuko group add`, the
  `groups`+`routes` resreg resources, or an agent's `register_group`).

The two postures are **mutually exclusive per platform by construction**: the
observe branch consumes the miss before the greeting branch runs, inside the
single shared `routeMiss`. A platform with a catch-all never greets — which is why
`arizuko create` seeds no catch-all; the trade-off is the operator's.

`web:` JIDs are out of scope: they address folders directly and never consult
the route table; web admission is route tokens (`5/W`).

### Routing is not firing

Routing a JID does not decide who inside it drives the agent — that is
sender-predicate stacking on the same table (`sender=` beside `room=`, with a
lower-precedence `#observe` row catching everyone else). An unauthorized sender
matches only the observe row: context, no turn, no reply, no fallback.
Engagement (`5/G`) then overrides per `(jid, topic)`, so sender rows gate who
_opens_ a conversation, not who speaks inside one. Firing never consults the
ACL; do not add a second check in the loop.

## As-built — steps 1–5 ship, 6–8 do not

- **1–5 as specified.** onbod's poll tick claims each unprompted row with an
  atomic `UPDATE … WHERE prompted_at IS NULL` (`promptUnprompted`,
  `onbod/main.go:384`), mints the token, and sends the link via routd's
  `/v1/outbound` under a `service:onbod` bearer. The landing sets an
  `onboard_jid` cookie and bounces to `/auth/login`; `X-User-Sub` survives only
  when `auth.ProxydTransit` holds; `claimOnboarding` + `linkJID`
  (`onbod/main.go:558`, `:845`) write the `acl_membership` edge.
- **6 — no picker.** `handleDashboard` (`onbod/main.go:572`) auto-picks the
  first `acl`-joined folder SQLite happens to return — no `ORDER BY`, no
  membership walk, no action filter, no user confirmation.
- **7 — the route write is a side effect, not an act.** It happens inside that
  claim, inside `createWorldTx`, and inside invite redemption; the latter two
  loop over _every_ JID the sub has paired and route them all at the target.
  Nothing records who routed what.
- **8 — dead end.** A caller with no world is told to find an invite. The
  username picker renders only behind a `pending_target` cookie, which only
  invite redemption sets. Chat-initiated onboarding cannot produce a world on
  its own: the gate queue admits you, then sends you away.

**The correctly-shaped handler already exists and is tested** —
`handleAddRoute` checks `MatchGroups(folders, target)` **and**
`userOwnsMatch(sub, match)`, exactly step 7's binding. **No HTML form renders
it**: the dashboard emits a read-only routing table. Steps 6–7 are built
server-side and unreachable from a browser.

Loud-vs-silent audit of the failure branches: insert failure, an already-paired
JID, and a no-gate-match all surface (chat notice / `errLinkRefused` → 403). A
route-table read failure is logged, treated as a miss, and the cursor advances —
the message is dropped. A row in an unknown status is logged every poll and
never advances (BUGS O1). An expired token cannot be re-requested from the chat.
Operator "deny" deletes the row, so the JID re-onboards on its next message —
deny is a reset, not a block.

## What the shipped schema cannot express

`onboarding(jid PK, status, prompted_at, created, token, token_expires,
user_sub, gate, queued_at, admitted_at)` is **a single global admission queue
keyed on JID, throttled per identity provider.** It cannot represent "routed
into world W by user U":

- **No target** — no folder or world column anywhere. `gate` switches on the
  `user_sub` prefix alone; it is an identity-provider bucket on the _identity_
  axis and names no location.
- **No approver** — `user_sub` is whoever proved control of the chat, not
  whoever exercised routing authority. They coincide only in the self-serve case.
- **`approved` is instance-global** — the daily cap counts per `gate` across the
  whole instance, so two worlds cannot hold independent admission policy.
- **The routing act is not in the table** — it is a side effect, leaving an
  unattributed `routes` row.

So `approved` grants nothing: nothing reads it as a precondition, and the one
gate (a negative `status='queued'` short-circuit on `/onboard`) runs **after**
the auto-route write. A queued caller's JID is already routed.

**Consequence:** the row must shrink to its pairing half and stop pretending to
be an approval record. The approval that matters is the authority check at step
7, evaluated against the target — not a status column.

## States to delete

Every remaining state must be reachable and load-bearing. These are not:

- **`token_used` is not a state.** It means `user_sub IS NOT NULL`, which the
  column already says. Its one distinct role — the terminal no-gate-matched
  dead-end — is now an explicit user-visible failure (`errLinkRefused` → 403).
  (`resetRow` + `promptCoolDown`, whose predicate `status='token_used' AND
user_sub IS NULL` was unreachable because both writers set `user_sub` in the
  same statement, have been deleted.)
- **`user_sub` is not a claim marker** either: since the pairing edge is written
  BEFORE the token is consumed (`5/31` step 5), a row with `user_sub` set and a
  live token is the legitimate mid-flight state a crash-replay must finish.
- **`status` is fully derivable** from `prompted_at` / `user_sub` / `queued_at` /
  `admitted_at` — one stamp per transition, which is the "exactly one trigger"
  property expressed as schema.
- **`gate` belongs to the identity axis.** It throttles per identity provider
  and is orthogonal to where a JID lands. It survives only as long as
  rate-limiting strangers is wanted, and must never be mistaken for
  world-scoped policy.

## The three token mechanisms

They are not one mechanism:

| token                     | carries           | lifetime        | at rest   | verdict                                          |
| ------------------------- | ----------------- | --------------- | --------- | ------------------------------------------------ |
| `onboarding.token`        | a **JID proof**   | single-use, 24h | plaintext | belongs to the identity axis — fold into pairing |
| `invites.token`           | a **grant**       | `max_uses`, TTL | plaintext | survives; it is authorization delivery           |
| `route_tokens.token_hash` | a **destination** | permanent       | `sha256`  | survives; no identity, no authz (`5/W`)          |

`onboarding.token` does not earn separate existence: it is a pairing nonce
proving "the bearer reads this chat". Strip it and the remaining row is
`(jid, prompted_at, user_sub)` plus throttles, and this spec's flow is
unchanged. **Not a unilateral deletion** — if pairing instead reuses `invites`,
note that an invite carries a _scope_ and an onboarding token carries a _JID_;
they must not be merged by making `target_glob` nullable. Whoever ships first
names the survivor.

## Operator surface — three findings

1. **The queue page is unreachable.** `/dash/onbod/` renders every row with
   approve/deny/reprompt, but dashd's services hub marks onbod `Built=false`
   (`dashd/services.go:35`), so no navigation reaches it.
2. **`onboarding` is not a resreg resource.** onbod's `openapi.json` declares
   only `onboarding_gates` and `invites`; the `/v1/onboarding*` endpoints are
   hand-mounted with no MCP twin. Root `CLAUDE.md` makes that a review-blocker —
   agents can mint invites but cannot see or act on the admission queue.
3. **The approval privilege is global.** Both onbod's dash gate and routd's
   `/invite` + `/gate` demand the unscoped operator role. Under the target flow
   the privilege is world-scoped authority over the target — a different check,
   and one the global role alone must not satisfy.

## Open, blocking

1. **`5/29`** — whether `onboarding.token` folds into the pairing carrier;
   whether `gate` survives as a stranger throttle; whether a stranger who chose
   no world may create one (today: only via an invite).
2. **Here, once that lands** — replace the auto-pick with the picker, render a
   form for the existing `handleAddRoute`, register `onboarding` as a resreg
   resource, drop the derivable `status` column.

## Consolidates

Canonical umbrella for the onboarding narrative; nothing is superseded. `5/B`
(`#observe`), `5/L`/`5/G` (mention promotion, engagement), `5/E` (route-miss
hook), `5/32`+`5/33` (grant vocabulary), `5/5` (invites, gates), `5/29` (the
world roster this routes into), `5/31` (pairing — onboarding redeems the bridge
rather than owning it, which is what makes pairing reachable outside a route
miss), `7/7` (operator queue page), `ROUTING.md` (route-table syntax).
