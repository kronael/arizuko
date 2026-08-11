---
status: shipped
depends:
  [32-acl-unified, 18-onboarding-model, 1-auth-standalone, W-webhook-routes]
---

# specs/5/31 — identity pairing (channel identity → verified account)

## Problem

A channel user is anonymous. `telegram:user/123` holds no grants and no way to
acquire any, because grants attach to verified accounts and nothing binds the
two.

Both halves already shipped. The bridge is `5/32`'s JID claim — an
`acl_membership(child=<channel JID>, parent=<canonical sub>)` edge that
`expandPrincipals` (`auth/authorize.go:123`) follows transitively.

But that write used to be reachable from exactly one place: a route **miss**, on
an `ONBOARDING_PLATFORMS` platform, with `ONBOARDING_ENABLED`, whose success
path creates a world. A user in an already-routed chat, or one who wants to act
as themselves rather than provision a tenant, could never get an edge.

Pairing was not missing — it was **trapped inside onboarding**. This spec
extracts it.

## The one rule

A channel identity holds **no grants, ever**. The only bridge to authority is
the membership edge, and the only way to write one is for the human who owns the
account to authenticate and consent.

Pairing grants nothing — it makes the channel identity resolve to the human, who
holds whatever they already held. A pairing token that could also grant would be
an invite, and that mechanism exists (`5/5`).

## Design

Pairing is a **kind of route token**, not a table of its own.

`route_tokens.kind` is `'route'` for a `/chat/` + `/hook/` delivery bearer and
`'pair'` for a pairing link (`store/migrations/0078-route-token-kind.sql`,
`routd/migrations/0026-route-token-kind.sql`). Resolution is kind-scoped in both
directions — route delivery accepts only `'route'`
(`store/route_tokens.go:129`), pairing redemption only `'pair'`
(`store/pairing.go:123`) — so neither can be redeemed as the other.

That is the whole schema change, and it is what removes the failure mode a
separate table would have carried: the token would live in `onbod.db` and the
edge in `routd.db` with no cross-DB transaction, so a crash between them strands
the user and the write ordering has to be reasoned about. In `routd.db` both
writes are one transaction (`store/pairing.go:58`) and the question does not
arise.

## Shipped surface

| step                                                                                | where                                                                                                   |
| ----------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| one minter, both callers                                                            | `store.IssuePairingLink` (`store/pairing.go`), taking a `rowExecer`                                     |
| mint `issue_pairing_link(jid)`, agent MCP only                                      | `routd/route_tokens_resource.go` `issue_pair`, declared `MCPOnly` at `resreg/resources/route_tokens.go` |
| mint from onbod's greeting, `owner_folder` NULL                                     | `promptUnprompted` (`onbod/main.go`); `routd/migrations/0032` relaxed the column                        |
| admission observes the edge on the poll tick                                        | `observePairings` -> `admitJID` (`onbod/main.go`)                                                       |
| agent gate: the JID must route to the caller's folder, which becomes `owner_folder` | `routd/route_tokens_resource.go` `pairingTargetFolder`                                                  |
| `GET /pair/{token}` — side-effect-free confirm page                                 | `webd/pair.go`, mounted `webd/server.go`                                                                |
| `POST /pair/{token}` — redeem in one routd transaction                              | `webd/pair.go` -> `store.RedeemPairing`                                                                 |
| anonymous visitor bounced through OAuth and back                                    | `compose/compose.go` (`/pair/` is `Auth: "user"`) -> `authd/oauth.go` `consumeReturn`                   |
| success page carries the user on to `/onboard`, which routes the JID                | `webd/pair.go` `pairContinueURL`                                                                        |
| unpair                                                                              | `routd/membership_resource.go`, `resreg/resources/membership.go`                                        |

`webd` serves the browser half because it already opens `routd.db`, so
redemption is local. onbod is not in the redemption path at all — it mints and
it observes, and both go through `routd.db` directly.

`GET` must stay side-effect-free: chat platforms unfurl links, and an unfurl bot
must not spend a pairing.

Missing, expired, consumed and malformed tokens share one "link unavailable"
response; a conflicting existing parent is the only distinct error, because it
is the only one the user can act on.

New authority is live on the paired identity's **next tool call**, not its next
spawn — `Authorize` reads the DB live (`5/32` §"No caching"). Unpair has the
same next-call effect.

## Consent is the security boundary

The edge is directional and the direction decides who bears the risk.
`acl_membership(child=telegram:user/999, parent=google:alice)` means whoever
controls that Telegram account can act as alice. Alice bears the whole risk.

So the attack is **consent phishing**: Mallory mints a link for _her own_
Telegram account (the mint gate permits nothing else) and sends it to Alice. If
Alice authenticates and the edge is written, Mallory's account holds Alice's
authority.

The only defence that sits where the risk sits is an explicit confirm at the
browser step, because that is the only step the account owner performs. The page
names the channel identity and states the consequence in one sentence: _anyone
who controls that account will be able to act as you._ The write is a POST
carrying a double-submit CSRF token (`auth/csrf.go`).

Pasting the URL into a different channel is **not** a separate threat: the token
names the JID it binds, so relocating it changes nothing — it only discloses the
secret, which is the phishing case above. The 10-minute TTL
(`store/pairing.go:24`) narrows the window but is not the defence; Mallory mints
on demand.

## One parent per channel identity

`expandPrincipals` unions. A channel JID with two parent subs would silently
hold the union of two humans' authority, and nobody would have agreed to it. So
a channel-identity child has **at most one parent**.

Enforced by the check inside the redemption transaction, **not** by a table
constraint: `acl_membership` legitimately holds multi-parent rows for role
membership, so the constraint is about what pairing may write. Role parents are
excluded from the check — a JID may hold a pairing edge and a role membership at
once (`4e831f10`).

## Unpair

Unpairing is deleting one `acl_membership` row. The resource already existed but
declared no `Endpoints` and no `MCPNames`, so an edge was removable only through
`arizuko apply`. It gains exactly one action — delete, scoped to
`added_by='pairing'` so it cannot reach role membership.

Either endpoint of the edge may call it, and each face proves it IS that
endpoint through its own injected containment: the agent (MCP `unpair`) is
authorized against the folder the child JID routes to — the same rule the mint
applies; the REST caller's JWT sub must BE the parent. Both directions are
de-escalation, and there is no inverse verb, because adding an edge requires
consent at the browser step. Operators already reach the row through manifests
and need no third path.

Unredeemed tokens are not separately revocable — they expire in ten minutes, and
minting again is the same operation. Listing outstanding pairings is not an
ability anyone needs; enumerating ten-minute bearer attempts is surface.

## Onboarding — the fold, shipped

Pairing's core has no dependency on onbod, `ONBOARDING_ENABLED`,
`ONBOARDING_PLATFORMS`, or route-miss handling, and none of this touches that.
What follows finishes the reverse direction: onbod's greeting stops running a
second write path into `acl_membership` and consumes the pairing link this spec
already shipped. `BUGS.md` P1b investigated a straight swap and correctly
stopped at three blockers, each an addition, not a deletion. It also closes
`5/18`'s Open-blocking Q1 ("whether `onboarding.token` folds into the pairing
carrier"); `5/18`'s own verdict on the three token mechanisms was already
"fold it".

As-built map:

| the fold                       | where                                                               |
| ------------------------------ | ------------------------------------------------------------------- |
| one pairing minter, two shapes | `store.IssuePairingLink` (`store/pairing.go`), `rowExecer`          |
| onbod's greeting mints one     | `promptUnprompted` (`onbod/main.go`), `ownerFolder = ""`            |
| `owner_folder` NULLable        | `routd/migrations/0032` + `store/migrations/0083`                   |
| admission observes the edge    | `observePairings` → `admitJID` (`onbod/main.go`), on the tick       |
| refusal reaches the chat       | `admitJID`'s `status='refused'` + `sendReply` + `onboarding.refuse` |
| the greeting stops locking out | `store.InsertOnboarding`'s re-arm (below)                           |

Today the two writers are distinguishable and it costs something: onbod stamps
`acl_membership.added_by='linkJID'`, so an edge onboarding created is **not**
reachable by `unpair`, scoped to `added_by='pairing'`. After the fold every
edge — onboarding-sourced or agent-minted — is written by `RedeemPairing` and
stamped `'pairing'` uniformly. The distinction, and the gap it caused, both go
away.

### One mint, per-caller target resolver

`pairingTargetFolder` (`routd/route_tokens_resource.go:245`) is called twice
inside routd — once from the Gate closure (`:222`), once from the handler
(`:164`) — because resreg's Gate authorizes but cannot hand its result to the
handler; both calls serve the _same_ caller (the agent, over the MCP socket)
enforcing the _same_ rule (the JID must already route to the caller's
folder). onbod's greeting is not a third call site in that pair — it is a
**different process**. It cannot reach `s.routeTokensHandler`, the Gate
closure, or any per-request `resreg.Execution`, whatever shape the injection
takes. A `Resource`-level plugin point (a field, a second `Gate`) built to
reach across a boundary Go closures cannot cross would be a mechanism solving
a problem two functions in the same file don't have.

The mint already resolves this correctly: `issueRouteTokenTx`
(`routd/route_tokens_resource.go:299`) takes `target` as a plain argument and
never computes it — resolution happens entirely in the caller, before the
call. That is where "per-caller resolver" already lives, and the fold keeps it
there. What moves is _which callers can reach the mint at all_: pull the
32-byte/`sha256`/`INSERT … kind='pair'` body out of `issueRouteTokenTx` into
`store/pairing.go`, next to `RedeemPairing`, as

```go
func IssuePairingLink(ctx context.Context, x rowExecer, jid, ownerFolder string) (rawToken string, err error)
```

satisfied by both `*sql.Tx` (routd's resreg mutation tx, unchanged agent-mint
path: `target = pairingTargetFolder(s.db, jid)`) and `*sql.DB` (onbod's `xdb`,
the same raw handle it already writes `acl_membership`/`routes` through
today, `onbod/main.go:101`). This mirrors `rowQuerier` two functions down in
the same file (`store/pairing.go:42`), which exists for the identical reason:
`PeekPairing` runs outside a transaction, `RedeemPairing` runs inside one, and
one function serves both by taking the executor as an argument instead of
assuming a `*Tx`.

onbod's resolver is not a route-ownership check — it is the constant
`ownerFolder = ""`. `InsertOnboarding` only ever fires from the route-**miss**
branch (`routd/loop.go:557`); "this JID routes nowhere" is proved by the
row's existence, not re-derived at greet time.

### `owner_folder` must go nullable

`""` will not insert. `route_tokens.owner_folder` is `NOT NULL REFERENCES
groups(folder) ON DELETE CASCADE`
(`store/migrations/0069-fk-route-tokens-owner-folder.sql:16`) — true today
because a delivery-kind token always has a real owning folder. A greeting
sent before any human is known has no folder to reference, by construction;
that is the whole reason onboarding exists apart from pairing. Neither
`PeekPairing` nor `RedeemPairing` reads `owner_folder` for a `kind='pair'`
row — the column is write-only on this path — and the pair kind is already
excluded from `ListRouteTokens` (`store/route_tokens.go:135`) and revoke
(`revokeRouteTokenTx`, `routd/route_tokens_resource.go:319`), both filtered to
`kind=route`. So `NULL` costs nothing functionally. A follow-up migration
relaxes the column to nullable; SQLite's FK check is a no-op on `NULL`, so
onbod's mint satisfies the constraint by having nothing to reference, not by
inventing a placeholder folder (the alternative — a permanent sentinel row in
`groups` — is rejected below).

### Deleting "greet once, ever"

`promptUnprompted` (`onbod/main.go:383`) claims `WHERE prompted_at IS NULL` —
a row it has ever prompted never matches again, `jid` is the table's primary
key, and the 10-minute pairing TTL means the link is almost always dead by the
time anyone revisits the row. That combination is a permanent lockout, not a
rate limit; the only escape today is `RepromptOnboarding`
(`store/onboarding.go:182`) behind an operator dashboard action
(`handleDashReprompt`, `onbod/dash.go:119`) most deployments don't route
anyone to. `5/18` names the same gap directly: "an expired token cannot be
re-requested from the chat."

`onboarding.token_ref`, `token_expires` and `idx_onboarding_token_ref` went
inert with the fold — nothing wrote them — and onbod migration `0006` dropped
all three (BUGS `F40`, closed 2026-08-09), along with their last readers:
`OnboardingRow.TokenExpires`, `ListOnboarding`'s SELECT, `RepromptOnboarding`'s
`token_ref=NULL`, and the archive document's two token fields. `prompted_at` is **not** deleted — `5/18` keeps it
explicitly ("the remaining row is `(jid, prompted_at, user_sub)` plus
throttles") — but its meaning changes from _ever_ to _last_.

**The cooldown's trigger is the inbound MISS, not a timer.** The design here
first read: claim on
`status='awaiting_message' AND (prompted_at IS NULL OR prompted_at < now-PairingTTL)`.
That is not bounded, and the live instances say so. Its own prose named the
right trigger — "a user who **messages again** after their old link has
silently expired gets a fresh one" — but the SQL expressed only elapsed time,
and the poll scans every row, not the ones anyone is waiting on. On krons,
twelve rows sit at `awaiting_message` with long-dead links; **three of them
route today** and carry 212, 773 and 1172 messages since their last greeting.
A timer-only cooldown greets those three actively-used, already-onboarded group
chats every ten minutes, forever.

So the re-arm lives in `store.InsertOnboarding`, which routd calls from the
route-**MISS** branch (`routd/loop.go`) — once per inbound message no route
table claimed:

```sql
INSERT INTO onboarding (jid, status, created) VALUES (?, 'awaiting_message', ?)
ON CONFLICT(jid) DO UPDATE SET prompted_at = NULL
WHERE onboarding.status = 'awaiting_message'
  AND onboarding.prompted_at IS NOT NULL
  AND onboarding.prompted_at < ?   -- now - PairingTTL
```

`promptUnprompted` keeps its unchanged `prompted_at IS NULL` claim. Two bounds
fall out, and neither is a new mechanism: the caller must have MISSED, so a
since-routed chat is never greeted again and a silent chat is never greeted
again; and the last greeting must be older than `PairingTTL`, so at most one
live link exists at a time. One constant still governs two things that used to
be unrelated numbers — how long a link stays redeemable, and how long before
the greeter hands out another.

Residual, and named rather than hidden: a chat that keeps talking, still routes
nowhere, and never redeems is offered a fresh link every `PairingTTL`. That is
the posture `ONBOARDING_PLATFORMS` opts into — the bot says nothing else in
that chat — but it is a rate, not a one-shot, and `F41` tracks whether the
constant should be larger for the Nth greeting.

`RepromptOnboarding`, `handleDashReprompt` and the dashboard button **survive**,
against this spec's earlier "dead once this ships". The cooldown is the routine
reprompt; the button is the operator's bypass for a chat that is not messaging
again on its own, and it is still the only way to re-greet one. It became a
FULL row reset (`user_sub`/`gate`/`queued_at`/`admitted_at` cleared too): the
observer only advances a row with `user_sub IS NULL`, so leaving the old verdict
behind would re-greet the chat and then never admit it — the `O1` stall shape,
freshly minted.

## Admission observes; redemption does not refuse

`linkJID` (`onbod/main.go:834`) does three things in one call: writes the
`acl_membership` edge, evaluates gates and advances the onboarding row (queue
or approve), and — on no gate match or an existing different parent —
refuses to the user's face via `errLinkRefused` → 403 (`writeLinkErr`,
`onbod/main.go:960`). `RedeemPairing` (`store/pairing.go:58`) does only the
first of those — it has no concept of a gate, and its one refusal
(`ErrPairingConflict`) is a fact about the edge itself (this identity already
belongs to someone else), not about admission policy. Splitting redemption
from admission means the edge write can no longer fail for a gate reason:
every pairing that resolves to a live token and a fresh identity succeeds,
unconditionally, at the browser. Gate evaluation moves entirely to after that
write, over data the write already committed.

**How admission observes the edge — poll, not a hook.** A hook would
live in webd's `handlePairPost` (`webd/pair.go:60`, the only place redemption
happens) calling onbod synchronously once `RedeemPairing` succeeds. That is
new cross-daemon surface in both directions: webd has no relationship to
onbod today — no URL, no service token, no failure policy (does a stalled
admission call fail the pairing the user is staring at, or get silently
dropped and rely on the poll anyway?) — and it puts routd's identity-write
path one HTTP round trip from onbod's uptime. onbod already holds a direct
handle onto `routd.db` (`xdb`, `onbod/main.go:101`) and already reads/writes
`acl_membership`/`routes` through it — the same FS-mounted direct-access
pattern `linkJID` uses today. The poll needs nothing new: extend the tick
that already runs `promptUnprompted`, scanning `onboarding WHERE
status='awaiting_message' AND user_sub IS NULL` (still unclaimed) against
`acl_membership WHERE added_by='pairing'` (now written by a webd-driven
redemption instead of onbod itself) for a JID paired since the last look. On a
match it runs exactly `linkJID`'s admission half — gate lookup, queue or
approve, audit row — minus the edge write, which already happened. No new
coupling, no new failure mode, no new state: the dedup is `onboarding`'s
existing `jid` primary key and `user_sub IS NULL` guard, unchanged.

Latency is bounded by `cfg.pollInterval` — 10s by default, the same tick
`promptUnprompted` already runs on, an order of magnitude tighter than
`admitFromQueue`'s existing 60-second admission-from-queue cadence
(`onbod/main.go:208`) that nobody has treated as too slow.

**What happened to `errLinkRefused`'s loud 403: it moved to chat and did not
disappear.** The refusal is now discovered on a poll
tick, after the browser page that used to show it is long gone — the fix
cannot be "keep the 403"; the fail-loud rule (root `CLAUDE.md`) says surface
it to the user, not that it must be HTTP. It reaches them the way the
greeting itself now does: onbod's existing `/v1/outbound` federation to routd
(`sendReply`, `onbod/main.go:1298`), already proven for the greeting message.
The observer sets `status='refused'` — a new terminal status, replacing
`token_used`'s old silent-dead-end role that `5/18` already flagged for
deletion — stamps `user_sub` (so the row stops matching and the refusal is
sent exactly once), and emits `onboarding.refuse` alongside the existing
`onboarding.queue`/`.approve` audit actions (`onbod/main.go:864`, `:891`).
`ErrPairingConflict` is unaffected: it is still discovered synchronously,
inside `RedeemPairing`, and still shown in the browser the user is looking at
(`webd/pair.go:71`).

> **This paragraph is where the design broke; kept for the record.** It read:
> "`handleDashboard`'s auto-route-on-claim (`firstAdminFolder` + `INSERT OR
IGNORE INTO routes`) … moves into the same observer … so an existing admin
> pairing a new channel keeps getting it auto-routed, exactly as today." Every
> pointer in it went false: `firstAdminFolder` became `adminFolders`, and steps
> 6–7 shipped. The observer must NOT route — the picker does, in a browser. See
> "What step 6 broke" below.

Both flows hold. (a) A new user messages an
unrouted chat: route-miss inserts the onboarding row (unchanged) →
`promptUnprompted` mints a `kind='pair'` token with `ownerFolder=NULL` and
sends `/pair/<token>` → the user completes OAuth and consent on webd
(unchanged, generic — webd needs no onboarding awareness at all) →
`RedeemPairing` writes the edge → the success page carries them to `/onboard`,
whose step-6 branch routes the JID → within one poll tick the observer evaluates
gates and the chat receives "queued" / "approved" / the refusal. (b) An
existing user pairs a second channel: entirely the shipped agent-mint path,
untouched — the observer's `user_sub IS NULL` scan only ever matches rows
`InsertOnboarding` created, so an agent-minted pairing is invisible to it.

| onboarding today                                                                           | after the fold                                    |
| ------------------------------------------------------------------------------------------ | ------------------------------------------------- |
| `status`: `awaiting_message → token_used → {queued, approved}`                             | `awaiting_message → {queued, approved, refused}`  |
| `token_ref`, `token_expires`, `idx_onboarding_token_ref`                                   | dropped by onbod `0006` (`F40`)                   |
| `prompted_at`: set once, permanent lockout                                                 | re-armed by an inbound miss past `PairingTTL`     |
| link: `authBaseURL + "/onboard?token=" + token`                                            | `webHost + "/pair/" + IssuePairingLink(...)`      |
| redemption: `handleTokenLanding`/`jidForToken`/`claimByToken`/`onboard_jid` cookie (onbod) | `GET`/`POST /pair/{token}` (webd, unchanged)      |
| edge write + admission: `linkJID`, one call                                                | edge: `RedeemPairing`. admission: poll observer   |
| refusal: `errLinkRefused` → 403 in browser                                                 | `status='refused'` → chat message                 |
| `RepromptOnboarding`, `handleDashReprompt`, reprompt button                                | kept as the operator bypass; now a full row reset |

### What step 6 broke — resolved

The fold above was designed while onbod auto-picked a world on claim. `5/18`
step 6 shipped one day later (`d9e57288`, 2026-08-04) and replaced that with a
**browser choice**: `handleDashboard` (`onbod/main.go:622`) asks
`unroutedJID` → `adminFolders`, then renders `renderWorldPicker`
(`:1261`, a form POSTing to `handleAddRoute`) for two or more worlds,
`insertRoute` for exactly one, and `renderNoWorld` (`:1288`) for none. `5/18`
is explicit that this is the point: "empty set is a terminal page — **never
auto-pick**".

Pairing does not route a JID; it only writes the edge. So the greeted user's
chat is still silent after redemption — which is the exact problem `5/18`
opens with — until step 6 runs. The pre-fold flow reached it because
`handleTokenLanding` set `auth_return=/onboard` and redirected there. Under the
fold the return target is webd's `/pair/{token}`, whose success page was
terminal ("Linked … Your next message from that chat carries your account's
access"), and **nothing carried the user to `/onboard`**.

The poll observer cannot stand in. It can queue, approve, refuse and message
the chat, but the multi-world case is a form a human fills in; a tick has no
browser. So closing this needed one of three:

1. webd's pair page carries the user into onbod's `/onboard`;
2. the observer chat-sends a SECOND link to `/onboard` — a two-link flow,
   specified nowhere;
3. the observer auto-picks — reverts step 6, the thing `5/18` shipped as the
   fix.

**Decided 2026-08-06: option 1, and it is shipped** (`webd/pair.go`
`pairContinueURL`). It does not give webd the onboarding awareness this spec
rejects, because webd states no condition and holds no onboarding state — it
names a destination and stops. `/onboard` decides everything from the DB:
step 6 keys on `unroutedJID` → `membershipJIDs`, which reads `acl_membership`
with **no `added_by` filter**, so an edge `RedeemPairing` wrote is found exactly
like one `linkJID` wrote. That is why the fold needs no hook and no second link:
the picker was already generic over who wrote the edge, and only the user's
route to it was missing.

It is a static link, not a `?next=`-style parameter. The flow's existing
`auth_return` cookie is a _pre-authentication resume_ pointer — proxyd's
`requireAuth` writes it, authd's `consumeReturn` reads, validates and clears it
during OAuth — so by the time the success page renders it is already spent on
`/pair/{token}` itself. Reusing it for a post-action destination would give one
cookie two meanings; carrying a new parameter would be a parallel mechanism.
Neither is needed when the destination is constant.

Options 2 and 3 stay rejected. The route through the picker is the ONLY route:
the fold added no second path to it, and `observePairings` deliberately writes
no `routes` row — the world choice is a form a human fills in, and a tick has no
browser. An agent-minted pairing still lands on a routed chat
(`pairingTargetFolder` refuses a JID that routes nowhere), where `/onboard`
simply shows the user their worlds; a greeting-minted one lands unrouted, which
is exactly the case step 6 exists for.

## Not in scope

- **`anon:<ip-hash>` web-chat senders** (`webd/route_token.go:50`). An IP hash
  is not a durable identity; binding it hands the next holder of that IP the
  account. A web visitor who wants authority logs in.
- **Merging two accounts** — `5/1` makes merging populated canonical users an
  operator action. Pairing adds an edge and never merges.

## Rejected

- **A `pairings` table** — a third token table with the same hygiene, lifecycle
  and revocation story as `route_tokens`, plus a cross-DB write ordering problem
  that only exists because it is a different table in a different daemon's DB.
- **Fold onto `invites` (`5/5`)** — hash-at-rest (`96a1293e`) removed the
  storage distinction, but an invite is issued _by_ a granting principal, targets
  a folder glob, is multi-use, **grants**, and lives in `onbod.db`, which is
  exactly the dependency this spec removes.
- **An operator REST mint face** — no operator mints a pairing for a chat they
  are not in; the agent is the minter.
- **A per-platform link (Telegram Login Widget, Discord OAuth)** — already
  shipped as a _login_ provider minting `sub=telegram:<id>`, a sub that reads
  like the channel principal without being it. One generic mechanism instead.
- **A short numeric code alongside the URL** — the URL is the code; a second
  redemption path for one binding is a second mechanism.
- **`used_count`** — a pairing binds one identity pair, so a second use has
  nothing to do. Single-use makes revocation and consumption the same operation.
- **A `resreg.Gate`-shaped resolver plugin reaching across the routd/onbod
  process boundary** — the two processes cannot share a Go closure; the
  resolver stays a plain argument to a shared `store/` function, which is
  where it already lived for the one caller that shipped.
- **A permanent sentinel row in `groups`** to satisfy `owner_folder`'s FK for
  greeting-originated tokens. Nullable is smaller and it's the truth — there
  is no folder yet. A platform-manufactured group is exactly what "operator
  data fixes belong to the operator; platform stays mechanical" (root
  `CLAUDE.md`) rules out.
- **A synchronous webd→onbod hook at redemption** — new coupling in both
  directions for a latency win the user cannot perceive (the outcome still
  travels over a chat send either way), when onbod already has direct DB
  access to the fact it needs to observe.

## Where this spec was wrong

Three things the spec asserted as already-working turned out not to exist. Each
had to be built before the flow above could work, and each is the kind of claim
a spec cannot make from reading a symbol name.

- **`StateIntent.Return` was unreachable from a bounced request.** The spec said
  an unauthenticated visitor lands back on the pairing URL via
  `StateIntent.Return`. proxyd wrote an `auth_return` cookie
  (`proxyd/main.go:948`) and **nothing read it** — a bounced caller landed on
  `/`. Fixed by folding the cookie into the signed intent in `authd`'s redirect
  (`authd/oauth.go:135` `consumeReturn`), validated by `SafeReturn` and cleared
  on consume so it cannot leak into a later login.
- **webd had no CSRF at all.** The spec said the POST carries "webd's
  double-submit CSRF cookie". webd had none; the only implementation was
  onbod's, private to that package. The helpers were extracted to
  `auth/csrf.go` (`EnsureCSRF` returns the token a renderer must embed) rather
  than copied — and extracting them exposed that onbod's own form never emitted
  the field it checks (BUGS F1, fixed `4def8f36`).
- **`resreg` could not express an agent-MCP-only action.** The spec assumed a
  custom action could be MCP-only. Every `Endpoint` produced a REST route and an
  OpenAPI path, so `issue_pairing_link` would have been given an operator face
  the spec explicitly rejects. Added `Endpoint.MCPOnly` (`resreg/resreg.go:126`),
  skipped in `RegisterREST` (`resreg/resreg.go:260`) and in the emitted doc
  (`resreg/openapi.go:235`).

## Ties

`5/32` — the membership edge, the principal namespace, the no-cache revocation
contract; its Open-Q4 closed here. `5/18` — onboarding, which this extracts
from and whose Open-blocking Q1 (the token fold) closes here. `5/5` — a guest
is a paired channel identity; distinct from that spec's "account linking",
which is **surrogate** OAuth (`5/15`), not identity pairing. `5/1` — the OAuth
login this redeems behind. `5/W` — the token table it joins.
