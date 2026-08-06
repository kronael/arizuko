---
status: shipped
depends:
  [
    B-route-mode-ingestion,
    L-mention-promotion,
    G-engagement,
    E-routd,
    S-jid-format,
    5-worlds-agents-sessions,
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
single-use `PairingTTL` pairing link that proves the bearer can read the chat
and names no folder)
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

## As-built — all eight steps ship

- **1–5 as specified, and step 5 is now the whole identity axis.** onbod's poll
  tick claims each unprompted row with an atomic
  `UPDATE … WHERE prompted_at IS NULL` (`promptUnprompted`), mints a **pairing**
  token through `store.IssuePairingLink`, and sends `/pair/<token>` via routd's
  `/v1/outbound` under a `service:onbod` bearer. Redemption is webd's
  `POST /pair/{token}` → `store.RedeemPairing`, which writes the
  `acl_membership` edge in routd's own transaction; its success page carries the
  user to `/onboard`. onbod's second writer into that table is gone, along with
  its own token, its landing handler and the `onboard_jid` cookie
  (`5/31` § "Onboarding — the fold"). Admission moved off the browser: a poll
  observer (`observePairings` → `admitJID`) evaluates gates over the committed
  edge and queues, approves, or refuses **to the chat**.
- ~~**6 — no picker.**~~ **SHIPPED** (`d9e57288`). `firstAdminFolder` became
  `adminFolders` — the same `auth.Authorize`-per-group evaluator, returning the
  whole set — and `renderWorldPicker` posts to the existing `handleAddRoute`.
  One world routes directly with no page; zero administrable worlds renders an
  explicit dead end rather than an empty picker.
- ~~**7 — the route write is a side effect, not an act.**~~ **SHIPPED**
  (`120d5461`). `routes.added_by`/`added_via` name who exercised routing
  authority and by which act (`picker` / `sole_world` in onbod, the tool name in
  routd). A COLUMN, not an audit row keyed to the route: onbod writes `routes`
  into routd.db while its `audit.Init` targets onbod.db, so no transaction spans
  the two and an audit row can commit while its route rolls back.
  `acl_membership.added_by` is the precedent, and `UnpairTx` is the proof it
  earns more than forensics. Two columns because who and by-which-act are
  independent — one column is `acl_membership`'s ambiguity, where the value is
  sometimes a principal and sometimes a mechanism.
  `createWorldTx` and invite redemption write no routes at all now; both redirect
  to `/onboard`, whose step-6 branch routes the ONE unrouted JID. The loop was
  load-bearing nowhere. Its seq-0 rows outranked any higher-seq route a chat
  already had (`seq ASC, id ASC`), so redeeming an invite moved chats the caller
  never named. onbod is left with a single `routes` INSERT.
  Existing rows keep NULL — "no actor recorded", not backfilled: nothing reads
  the columns as a precondition. Still NULL-writing: `store.AddRoute`/
  `PutRouteRow` (dashd, CLI) and resreg's manifest `Apply`, which rebuilds
  `routes` wholesale.
- ~~**8 — dead end.**~~ **SHIPPED** (`c28160bd`), and it made `approved` mean
  something for the first time. `mayCreateFirstWorld` is the second authority
  beside the invite's `pending_target` cookie, and it is a **conjunction**: an
  `onboarding` row in `status='approved'` **and** at least one enabled
  `onboarding_gates` row.

  The gate half is the whole decision, not a detail. `admitJID` approves EVERY
  paired identity when `loadGates` is empty, and `arizuko create` seeds no
  gates, so `approved` on its own would have flipped every existing deployment
  from invite-only world creation to open signup — a posture change the platform
  does not get to make for the operator. Requiring a gate keeps today's
  behaviour exactly where none is configured and makes the queue verdict
  load-bearing only where the operator opted in by configuring one. The
  admission queue thereby acquires the purpose it never had: the gate selects
  who may queue, `limit_per_day` paces the drain, and the resulting `approved`
  is what buys a world.

  Two properties the implementation holds deliberately:
  - **The gate count reads `loadGates`, not `COUNT(*) FROM onboarding_gates`.**
    One reader, so the `enabled=1` rows that make `admitJID` selective are
    exactly the rows that make its verdict grant something. Counting the table
    would unlock the picker on a DISABLED gate while admission stayed
    indiscriminate — two predicates for one question, drifting.
  - **It is a precondition on the WRITE.** `handleCreateWorld` calls it;
    `handleDashboard` calls the SAME function only to choose what to render. A
    caller who guesses the POST is refused there, so the picker is a
    convenience and never the authorization — the same split step 6's world
    picker has against `handleAddRoute`.

  Containment is structural rather than checked: `parent` derives from the
  cookie alone and the approved branch runs only when the cookie is absent, so
  an approved admission can name no subtree but the root. (The cookie's own
  parent is NOT bound to the caller's authority — a pre-existing hole on the
  invite path, filed as BUGS `F50`.)

`handleAddRoute` is the binding: `MatchGroups(folders, target)` **and**
`userOwnsMatch(sub, match)`, one check per axis of step 7's rule. Step 6's picker
posts to it, so the browser reaches it; the sole-world branch skips the form (one
world is not a choice) but not the authority — its target comes from
`adminFolders` and its match from `unroutedJID`, both membership-derived.

Loud-vs-silent audit of the failure branches: insert failure and an
already-paired JID surface in the browser (chat notice / `ErrPairingConflict` →
409); a no-gate-match is now a terminal `status='refused'` **delivered to the
chat**, because it is discovered on a tick after the browser page is gone. A
route-table read failure is logged, treated as a miss, and the cursor advances —
the message is dropped. A row in an unknown status is logged every poll and
never advances (BUGS O1), and `token_used` joined that set: no writer produces
it any more. An expired link **can** now be re-requested from the chat — the
next route miss past `PairingTTL` re-arms the greeting (`5/31`); the operator
reprompt button survives as the bypass for a chat that has gone quiet.

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
- **The routing act is not in the table** — it never was one of this row's
  facts. The act's record lives on the `routes` row it writes
  (`added_by`/`added_via`, step 7), which is where the target already is.

`approved` used to grant nothing — nothing read it as a precondition. Step 8
(`c28160bd`) gave it exactly one meaning, and the limits above bound what that
meaning can be: it entitles a caller to create a **top-level** world, because
the row names no target it could be scoped to. It is an entitlement on the
identity axis, spent once at the root; it is not, and cannot become, "approved
FOR world W".

**Consequence:** the row is a pairing record plus one instance-global
entitlement, and it must not grow a second. Any approval that names a location
is the authority check at step 7, evaluated against the target — not a status
column.

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

| token                     | carries           | lifetime        | at rest   | verdict                                    |
| ------------------------- | ----------------- | --------------- | --------- | ------------------------------------------ |
| `onboarding.token`        | a **JID proof**   | single-use, 24h | plaintext | GONE — folded into `route_tokens` (`5/31`) |
| `invites.token`           | a **grant**       | `max_uses`, TTL | plaintext | survives; it is authorization delivery     |
| `route_tokens.token_hash` | a **destination** | permanent       | `sha256`  | survives; no identity, no authz (`5/W`)    |

`onboarding.token` did not earn separate existence: it was a pairing nonce
proving "the bearer reads this chat". **Stripped** — `route_tokens` is the
survivor (`5/31`), the remaining row is `(jid, prompted_at, user_sub)` plus
throttles, and this spec's flow is unchanged. The `token_ref`/`token_expires`
columns are inert and drop in a follow-up (BUGS `F40`). `invites` was rejected
as the carrier: an invite carries a _scope_ and an onboarding token carries a
_JID_, and merging them would have meant a nullable `target_glob`.

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

1. ~~**`5/5`** — whether `onboarding.token` folds into the pairing carrier~~
   — **SHIPPED** per [`5/31`](31-identity-pairing.md) § "Onboarding — the
   fold": `token_ref`/`token_expires` folded into `route_tokens`, `prompted_at`
   survives as a cooldown re-armed by the inbound miss, redemption moved to
   `/pair/{token}`, admission (gates/queue/refusal) is observed by a poll.

   ~~Whether a stranger who chose no world may create one~~ — **ANSWERED
   2026-08-06: yes, but only under a configured gate** (`c28160bd`, BUGS
   `F42`). `status='approved'` unlocks the username picker for a caller with no
   worlds **iff** at least one enabled `onboarding_gates` row exists; see
   As-built step 8 for the mechanism.

   The rejected alternative is worth keeping, because it is the better
   mechanism and only the default posture disqualified it. **Option 1 —
   `approved` unlocks the picker unconditionally** — is one condition instead
   of two and needs no gate to be coherent. It was rejected because with no
   gates configured `admitJID` approves every paired identity and `arizuko
create` seeds none, so shipping it would have turned every existing
   deployment into open signup: anyone who messages the bot on an
   `ONBOARDING_PLATFORMS` platform could provision a top-level tenant. That is
   a product decision belonging to the operator, and a platform default is the
   wrong place to make it. **Option 1 remains available as an operator-facing
   change** — an explicit `ONBOARDING_OPEN_SIGNUP`-style opt-in that satisfies
   the gate half — if the posture is ever revisited. Option 3 (delete the queue
   outright) is now foreclosed on this axis: `gate` has a consumer.

   The consequence to accept: with zero gates the queue still approves
   everyone and those approvals still buy nothing, and an operator's explicit
   `store.ApproveOnboarding` fast-path is likewise inert until a gate exists.
   That is the closed default working as intended, not a gap — but it means
   "why did approving them do nothing?" has a real answer an operator can hit.
   Still open from `5/5`: whether `gate` survives as a stranger throttle now
   that it also functions as this switch.

2. ~~replace the auto-pick with the picker, render a form for the existing
   `handleAddRoute`~~ — **SHIPPED** (`d9e57288`). ~~step 7's missing
   attribution~~ — **SHIPPED** (`120d5461`). ~~register `onboarding` as a
   resreg resource~~ — **SHIPPED** (`Z3`). ~~step 8's dead end~~ —
   **ANSWERED**, see Q1. Still open here: stamping the remaining `routes`
   writers (`store.AddRoute`/`PutRouteRow`, resreg `Apply`) so `added_by IS
NULL` means only "pre-attribution", and binding the invite cookie's parent to
   the caller's authority (BUGS `F50`). The derivable-`status` deletion was
   closed as NOT derivable (`P3b`).

## Consolidates

Canonical umbrella for the onboarding narrative; nothing is superseded. `5/B`
(`#observe`), `5/L`/`5/G` (mention promotion, engagement), `5/E` (route-miss
hook), `5/32`+`5/33` (grant vocabulary), `5/5` (invites, gates, the world
roster this routes into), `5/31` (pairing — onboarding redeems the bridge
rather than owning it, which is what makes pairing reachable outside a route
miss), `7/7` (operator queue page), `ROUTING.md` (route-table syntax).
