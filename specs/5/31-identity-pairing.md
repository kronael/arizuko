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
`expandPrincipals` (`auth/authorize.go:120`) follows transitively.

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

| step                                                                                    | where                                                                                             |
| --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| mint `issue_pairing_link(jid)`, agent MCP only                                          | `routd/route_tokens_resource.go:159`, declared `MCPOnly` at `resreg/resources/route_tokens.go:36` |
| gate: the JID must route to the caller's folder, and that folder becomes `owner_folder` | `routd/route_tokens_resource.go:245` `pairingTargetFolder`                                        |
| `GET /pair/{token}` — side-effect-free confirm page                                     | `webd/pair.go:35`, mounted `webd/server.go:145`                                                   |
| `POST /pair/{token}` — redeem in one routd transaction                                  | `webd/pair.go:60` → `store.RedeemPairing`                                                         |
| anonymous visitor bounced through OAuth and back                                        | `compose/compose.go:338` (`/pair/` is `Auth: "user"`) → `authd/oauth.go:135` `consumeReturn`      |
| unpair                                                                                  | `routd/membership_resource.go:56`, `resreg/resources/membership.go`                               |

`webd` serves the browser half because it already opens `routd.db`, so
redemption is local. onbod is not in the path at all.

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

## Onboarding — extracted from, not yet folded onto

Pairing has **no** dependency on onbod, `ONBOARDING_ENABLED`,
`ONBOARDING_PLATFORMS`, or route-miss handling. That independence is the point
of the extraction and it holds today.

The reverse direction has **not** shipped: onbod still mints its own
`onboarding.token`, still posts `/onboard?token=…`, and still carries the JID
across the OAuth round-trip in the unsigned `onboard_jid` cookie into
`linkJID` — a second write path into the same edge, which can drift from this
one. Folding onbod's greeting onto a pairing link (and deleting that cookie) is
the follow-up; it is a change to onboarding's flow, not to pairing.

The two writers are distinguishable and that has a live consequence: onbod
stamps `added_by='linkJID'`, so an edge created by onboarding is **not**
reachable by `unpair`, which is scoped to `added_by='pairing'`. Deliberate —
unpair must not become a general `acl_membership` delete — but it means an
onboarding-era link is still removable only through `arizuko apply` until the
fold lands.

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
from. `5/29` — a guest is a paired channel identity; distinct from that spec's
"account linking", which is **surrogate** OAuth (`5/15`), not identity pairing.
`5/1` — the OAuth login this redeems behind. `5/W` — the token table it joins.
